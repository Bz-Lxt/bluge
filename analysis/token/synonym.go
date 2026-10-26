//  Copyright (c) 2022 Couchbase, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 		http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package token

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	"github.com/blugelabs/bluge/analysis"
)

// phraseKeySep separates the words of a multi-word rule source in the
// internal lookup key. Rule words are parsed with strings.Fields, so they
// can never contain whitespace and joining with a single space is unambiguous.
const phraseKeySep = " "

// SynonymMap holds configurable single-word and multi-word synonym rules.
//
// Rules use the Solr synonym syntax, one rule per line:
//
//	# comment
//	happy, glad, joyful
//	united states, usa
//	i-pod, i pod => ipod
//
// Lines without "=>" declare equivalent word (or multi-word) sequences.
// With expand enabled every sequence is a synonym of every other sequence.
// With expand disabled every sequence except the first is normalized to the
// first sequence. Lines with "=>" are one-way explicit mappings which are
// applied regardless of the expand setting.
//
// The map is safe for concurrent use once all rules have been loaded.
//
// Rule words are matched against token terms exactly, so matching is case
// sensitive; when case should be ignored, place a lower case token filter
// before the synonym filter in both indexing and query analyzers and write
// the rules in lower case.
type SynonymMap struct {
	expand bool
	// rules maps a source sequence (joined with phraseKeySep) to the list of
	// output sequences it produces.
	rules map[string][][]string
	// maxSourceLen is the length, in words, of the longest source sequence.
	maxSourceLen int
}

// NewSynonymMap creates an empty synonym map. When expand is true equivalent
// sets expand in both directions, when false they are normalized to the first
// sequence in the set.
func NewSynonymMap(expand bool) *SynonymMap {
	return &SynonymMap{
		expand: expand,
		rules:  make(map[string][][]string),
	}
}

// LoadFile reads synonym rules from a text file, one rule per line.
func (m *SynonymMap) LoadFile(filename string) error {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}
	return m.LoadBytes(data)
}

// LoadBytes reads synonym rules from memory, one rule per line.
func (m *SynonymMap) LoadBytes(data []byte) error {
	return m.LoadReader(bytes.NewReader(data))
}

// LoadReader reads synonym rules from a reader, one rule per line.
// The first illegal rule terminates loading and is reported together with
// its 1-based line number.
func (m *SynonymMap) LoadReader(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if isBlankOrComment(line) {
			continue
		}
		if err := m.LoadLine(line); err != nil {
			return fmt.Errorf("synonym rule line %d %q: %v", lineNum, strings.TrimSpace(line), err)
		}
	}
	return scanner.Err()
}

// LoadLine parses a single synonym rule and registers it in the map.
// Blank lines and comment lines are ignored, every other malformed rule
// results in an error.
func (m *SynonymMap) LoadLine(line string) error {
	if isBlankOrComment(line) {
		return nil
	}

	lhs, rhs, explicit, err := parseSynonymRule(line)
	if err != nil {
		return err
	}

	if explicit {
		// each comma separated sequence on the left maps to every sequence
		// on the right; the expand setting is ignored
		for _, input := range lhs {
			for _, output := range rhs {
				m.addRule(input, output)
			}
		}
	} else if m.expand {
		// every sequence is a synonym of every other sequence; the original
		// sequence is always emitted by the filter itself, so it does not
		// need to be registered as an output of itself
		for i, input := range lhs {
			for j, output := range lhs {
				if i != j {
					m.addRule(input, output)
				}
			}
		}
	} else {
		// normalize every sequence except the first to the first one
		canonical := lhs[0]
		for i := 1; i < len(lhs); i++ {
			m.addRule(lhs[i], canonical)
		}
	}

	return nil
}

// addRule registers a single source sequence to output sequence mapping.
// Identical output sequences registered for the same source are deduplicated
// so a term can never be emitted more than once at the same position.
func (m *SynonymMap) addRule(input, output []string) {
	key := strings.Join(input, phraseKeySep)
	for _, existing := range m.rules[key] {
		if stringSlicesEqual(existing, output) {
			return
		}
	}
	m.rules[key] = append(m.rules[key], append([]string(nil), output...))
	if len(input) > m.maxSourceLen {
		m.maxSourceLen = len(input)
	}
}

// SynonymFilter replaces or augments tokens according to a SynonymMap.
//
// The filter performs a single left-to-right pass using the longest matching
// source sequence at every position:
//
//   - multi-word rules only match tokens occupying consecutive positions,
//     position holes (PositionIncr greater than one) can never be spanned,
//   - original tokens are always retained and synonym words are stacked on
//     top of them with PositionIncr zero, keeping the stream usable for
//     phrase queries,
//   - generated tokens are never re-examined, so rule chains or cycles can
//     never loop or expand without bound,
//   - the filter keeps no state between Filter invocations, which means a
//     rule can never span the position increment gap between multiple
//     values of the same field,
//   - duplicate words at the same position are emitted only once.
type SynonymFilter struct {
	synonyms *SynonymMap
}

func NewSynonymFilter(synonyms *SynonymMap) *SynonymFilter {
	return &SynonymFilter{
		synonyms: synonyms,
	}
}

func (s *SynonymFilter) Filter(input analysis.TokenStream) analysis.TokenStream {
	if s.synonyms == nil || len(s.synonyms.rules) == 0 {
		return input
	}

	rv := make(analysis.TokenStream, 0, len(input))
	for i := 0; i < len(input); {
		matchLen, outputs := s.synonyms.longestMatch(input, i)
		if matchLen == 0 {
			rv = append(rv, input[i])
			i++
			continue
		}
		rv = append(rv, s.expandMatch(input[i:i+matchLen], outputs)...)
		i += matchLen
	}
	return rv
}

// longestMatch returns the length, in words, of the longest source sequence
// matching at offset and the output sequences it produces. Zero means no rule
// matched at offset.
func (m *SynonymMap) longestMatch(input analysis.TokenStream, offset int) (int, [][]string) {
	limit := m.maxSourceLen
	if remaining := len(input) - offset; remaining < limit {
		limit = remaining
	}
	for length := limit; length >= 1; length-- {
		if length > 1 && !occupiesConsecutivePositions(input, offset, length) {
			continue
		}
		key := makePhraseKey(input, offset, length)
		if outputs, ok := m.rules[key]; ok {
			return length, outputs
		}
	}
	return 0, nil
}

// occupiesConsecutivePositions reports whether the source words at
// [offset, offset+length) occupy length adjacent positions. The first token
// must advance the position (it may advance by more than one, a hole before
// the match is preserved) and every following token must advance by exactly
// one, so the match can never span a position hole or stacked alternatives.
func occupiesConsecutivePositions(input analysis.TokenStream, offset, length int) bool {
	if input[offset].PositionIncr < 1 {
		return false
	}
	for i := offset + 1; i < offset+length; i++ {
		if input[i].PositionIncr != 1 {
			return false
		}
	}
	return true
}

func makePhraseKey(input analysis.TokenStream, offset, length int) string {
	if length == 1 {
		return string(input[offset].Term)
	}
	parts := make([]string, length)
	for i := 0; i < length; i++ {
		parts[i] = string(input[offset+i].Term)
	}
	return strings.Join(parts, phraseKeySep)
}

// expandMatch linearizes the matched original tokens and all synonym output
// sequences into one dense token stream grouped by position. At every
// position the original token (if any) is emitted first, followed by the
// synonym words for that position, each at most once.
func (s *SynonymFilter) expandMatch(match analysis.TokenStream, outputs [][]string) analysis.TokenStream {
	matchLen := len(match)
	outputLen := matchLen
	for _, output := range outputs {
		if len(output) > outputLen {
			outputLen = len(output)
		}
	}

	rv := make(analysis.TokenStream, 0, matchLen+len(outputs)*outputLen)
	for pos := 0; pos < outputLen; pos++ {
		seen := make(map[string]bool, len(outputs)+1)
		positionTokens := make(analysis.TokenStream, 0, len(outputs)+1)

		// retain the original token at this position, it is the primary token
		if pos < matchLen {
			orig := match[pos]
			seen[string(orig.Term)] = true
			positionTokens = append(positionTokens, orig)
		}

		// stack the synonym words contributed by every output sequence
		for _, output := range outputs {
			if pos >= len(output) {
				continue
			}
			word := output[pos]
			if seen[word] {
				continue
			}
			seen[word] = true
			positionTokens = append(positionTokens, newSynonymToken(word, match, pos))
		}

		for i, t := range positionTokens {
			switch {
			case i != 0:
				// stacked alternative at the same position
				t.PositionIncr = 0
			case pos == 0:
				// preserve the increment of the first matched token so that a
				// hole before the match is left intact
				t.PositionIncr = match[0].PositionIncr
			default:
				t.PositionIncr = 1
			}
			rv = append(rv, t)
		}
	}
	return rv
}

// newSynonymToken builds the synthetic token for one word of an output
// sequence. Words aligned with an original token inherit its offsets, words
// produced beyond the matched span carry a zero width offset at the span end.
func newSynonymToken(word string, match analysis.TokenStream, pos int) *analysis.Token {
	start, end := match[0].Start, match[len(match)-1].End
	if pos < len(match) {
		start = match[pos].Start
		end = match[pos].End
	} else if len(match) > 0 {
		start = match[len(match)-1].End
		end = start
	}
	return &analysis.Token{
		Term:         []byte(word),
		Start:        start,
		End:          end,
		PositionIncr: 0,
		Type:         analysis.Synonym,
	}
}

// parseSynonymRule parses one rule line into comma separated word sequences.
// For explicit mappings (lhs => rhs) the two sides are returned separately,
// otherwise lhs holds the equivalent sequences and rhs is nil.
func parseSynonymRule(line string) (lhs, rhs [][]string, explicit bool, err error) {
	if i := commentStart(line); i >= 0 {
		line = line[:i]
	}

	if !strings.Contains(line, "=>") {
		lhs, err = parseSynonymSide(line)
		if err != nil {
			return nil, nil, false, err
		}
		if len(lhs) == 0 {
			return nil, nil, false, fmt.Errorf("synonym rule must contain at least one word")
		}
		return lhs, nil, false, nil
	}

	sides := strings.Split(line, "=>")
	if len(sides) != 2 {
		return nil, nil, true, fmt.Errorf("only one explicit mapping '=>' allowed per line")
	}
	lhs, err = parseSynonymSide(sides[0])
	if err != nil {
		return nil, nil, true, fmt.Errorf("left side of '=>': %v", err)
	}
	rhs, err = parseSynonymSide(sides[1])
	if err != nil {
		return nil, nil, true, fmt.Errorf("right side of '=>': %v", err)
	}
	if len(lhs) == 0 {
		return nil, nil, true, fmt.Errorf("left side of '=>' must not be empty")
	}
	if len(rhs) == 0 {
		return nil, nil, true, fmt.Errorf("right side of '=>' must not be empty")
	}
	return lhs, rhs, true, nil
}

// parseSynonymSide splits one side of a rule on commas and turns each group
// into its whitespace separated word sequence.
func parseSynonymSide(side string) ([][]string, error) {
	groups := strings.Split(side, ",")
	sequences := make([][]string, 0, len(groups))
	for i, group := range groups {
		words := strings.Fields(group)
		if len(words) == 0 {
			return nil, fmt.Errorf("entry %d is empty", i+1)
		}
		sequences = append(sequences, words)
	}
	return sequences, nil
}

func isBlankOrComment(line string) bool {
	trimmed := strings.TrimLeft(line, " \t\r\n")
	return trimmed == "" || strings.HasPrefix(trimmed, "#")
}

// commentStart returns the byte offset where a comment begins: a '#' at the
// start of the (untrimmed) line, or a '#' preceded by whitespace.
func commentStart(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return i
		}
	}
	return -1
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
