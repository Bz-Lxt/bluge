//  Copyright (c) 2020 Couchbase, Inc.
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

package searcher

import (
	"fmt"
	"sort"
	"strings"

	"github.com/blugelabs/bluge/search/similarity"

	"github.com/blugelabs/bluge/search"
)

// SpanClause describes a single node in a span (positional interval)
// query tree.
//
// A leaf clause matches every occurrence of a single term, and is
// described by a non-empty Term with an empty Clauses slice.  A
// non-leaf (near) clause combines the matches of its nested Clauses
// according to the following rules:
//
//   - every child match must occupy a pairwise non-overlapping token
//     position interval, so no token position may participate in the
//     resulting match more than once
//   - when InOrder is true the child matches must appear in the field
//     in the same order in which the clauses were declared; when it is
//     false they may appear in any order
//   - Slop limits the total number of unmatched token positions
//     between the first and last child match (the sum of the gaps
//     between adjacent child matches); a Slop of zero requires the
//     children to be directly adjacent
//   - unmatched positions internal to a child match (for example
//     positions removed by analysis) are counted by that child's own
//     clause and do not count towards this clause's Slop
//
// All clauses in a tree must target the same Field.  Multiple values
// of a field are separated in the position stream by the field's
// position increment gap, so crossing a value boundary simply spends
// that many positions of Slop.
type SpanClause struct {
	Field   string
	Term    string
	Clauses []*SpanClause
	Slop    int
	InOrder bool
}

func (c *SpanClause) isLeaf() bool {
	return len(c.Clauses) == 0
}

// Validate verifies that the clause tree describes legal positional
// intervals.  Illegal clauses (missing field/term, fewer than two
// near clauses, negative slop, mismatched fields) result in an error.
func (c *SpanClause) Validate() error {
	if c == nil {
		return fmt.Errorf("span clause must not be nil")
	}
	if c.Field == "" {
		return fmt.Errorf("span clause requires a field")
	}
	if c.isLeaf() {
		if c.Term == "" {
			return fmt.Errorf("span term clause requires a non-empty term")
		}
		return nil
	}
	if c.Term != "" {
		return fmt.Errorf("span near clause must not set a term")
	}
	if len(c.Clauses) < 2 {
		return fmt.Errorf("span near clause requires at least two clauses")
	}
	if c.Slop < 0 {
		return fmt.Errorf("span near clause slop must be greater than or equal to 0")
	}
	for i, child := range c.Clauses {
		if child == nil {
			return fmt.Errorf("span near clause at position %d must not be nil", i)
		}
		if child.Field != c.Field {
			return fmt.Errorf("span near clause children must all target field %q, clause %d targets %q",
				c.Field, i, child.Field)
		}
		if err := child.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// spanLeaf identifies one concrete token position participating in a
// span match.
type spanLeaf struct {
	term string
	loc  *search.Location
}

// spanMatch is one concrete interval matched by a clause.  Start and
// End are the minimum and maximum token positions of the participating
// leaves, and Leaves holds exactly the leaves that participate in this
// match, in ascending position order.
type spanMatch struct {
	start  int
	end    int
	leaves []spanLeaf
}

func spanMatchKey(m spanMatch) string {
	var buf strings.Builder
	for i, lf := range m.leaves {
		if i > 0 {
			buf.WriteByte('|')
		}
		buf.WriteString(lf.term)
		buf.WriteByte('@')
		fmt.Fprintf(&buf, "%d:%d-%d", lf.loc.Pos, lf.loc.Start, lf.loc.End)
	}
	return buf.String()
}

// sortedSpanLocations returns the locations of a term sorted by
// position (then offsets) and de-duplicated.  It never mutates the
// locations held by the document match, which keeps enumeration
// stable regardless of the order produced by the underlying segments.
func sortedSpanLocations(tlm search.TermLocationMap, term string) search.Locations {
	src := tlm[term]
	locs := make(search.Locations, len(src))
	copy(locs, src)
	sort.Slice(locs, func(i, j int) bool {
		if locs[i].Pos != locs[j].Pos {
			return locs[i].Pos < locs[j].Pos
		}
		if locs[i].Start != locs[j].Start {
			return locs[i].Start < locs[j].Start
		}
		return locs[i].End < locs[j].End
	})
	// remove exact duplicate occurrences (for example repeated terms
	// merged from multiple segments)
	slow := 0
	for fast := 1; fast < len(locs); fast++ {
		if locs[fast].Pos == locs[slow].Pos &&
			locs[fast].Start == locs[slow].Start &&
			locs[fast].End == locs[slow].End {
			continue
		}
		slow++
		locs[slow] = locs[fast]
	}
	if len(locs) > 0 {
		locs = locs[:slow+1]
	}
	return locs
}

// enumerateSpanMatches returns every interval matched by the clause
// within the provided term location map.  The returned matches are in
// deterministic order and are de-duplicated.
func enumerateSpanMatches(c *SpanClause, tlm search.TermLocationMap) []spanMatch {
	if c.isLeaf() {
		locs := sortedSpanLocations(tlm, c.Term)
		if len(locs) == 0 {
			return nil
		}
		rv := make([]spanMatch, 0, len(locs))
		for _, loc := range locs {
			rv = append(rv, spanMatch{
				start:  loc.Pos,
				end:    loc.Pos,
				leaves: []spanLeaf{{term: c.Term, loc: loc}},
			})
		}
		return rv
	}

	childMatches := make([][]spanMatch, len(c.Clauses))
	for i, child := range c.Clauses {
		cm := enumerateSpanMatches(child, tlm)
		if len(cm) == 0 {
			// a missing child makes the whole near clause unmatched
			return nil
		}
		childMatches[i] = cm
	}

	var rv []spanMatch
	seen := make(map[string]struct{})
	chosen := make([]spanMatch, 0, len(c.Clauses))

	var walk func(level int)
	walk = func(level int) {
		if level == len(c.Clauses) {
			m, ok := combineSpanMatches(chosen, c.Slop, c.InOrder)
			if ok {
				key := spanMatchKey(m)
				if _, dup := seen[key]; !dup {
					seen[key] = struct{}{}
					rv = append(rv, m)
				}
			}
			return
		}
		for _, cand := range childMatches[level] {
			// no token position may be used by two child matches
			if spanMatchesDisjoint(cand, chosen) {
				chosen = append(chosen, cand)
				walk(level + 1)
				chosen = chosen[:len(chosen)-1]
			}
		}
	}
	walk(0)

	return rv
}

// spanMatchesDisjoint reports whether the candidate's participating
// positions are all unused by the already chosen child matches.
func spanMatchesDisjoint(cand spanMatch, chosen []spanMatch) bool {
	for _, ch := range chosen {
		for _, a := range cand.leaves {
			for _, b := range ch.leaves {
				if a.loc.Pos == b.loc.Pos {
					return false
				}
			}
		}
	}
	return true
}

// combineSpanMatches validates the ordering, non-overlap and slop
// constraints for one selection of child matches and, when they hold,
// returns the merged interval with its participating leaves in
// ascending position order.
func combineSpanMatches(chosen []spanMatch, slop int, inOrder bool) (spanMatch, bool) {
	n := len(chosen)

	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	if !inOrder {
		sort.Slice(order, func(a, b int) bool {
			ca, cb := chosen[order[a]], chosen[order[b]]
			if ca.start != cb.start {
				return ca.start < cb.start
			}
			return ca.end < cb.end
		})
	}

	gap := 0
	for i := 1; i < n; i++ {
		prev := chosen[order[i-1]]
		next := chosen[order[i]]
		if next.start <= prev.end {
			// overlapping, nested or co-located intervals would
			// reuse a position and are not allowed
			return spanMatch{}, false
		}
		gap += next.start - prev.end - 1
	}
	if gap > slop {
		return spanMatch{}, false
	}

	leaves := make([]spanLeaf, 0)
	for _, idx := range order {
		leaves = append(leaves, chosen[idx].leaves...)
	}

	return spanMatch{
		start:  chosen[order[0]].start,
		end:    chosen[order[n-1]].end,
		leaves: leaves,
	}, true
}

// SpanSearcher first narrows the candidate documents with a
// conjunction of the distinct leaf term searchers, and then verifies
// that the positional span constraints are satisfied using the term
// locations.  Only locations participating in a valid match are
// reported, so highlighting marks exactly the matched tokens.
type SpanSearcher struct {
	mustSearcher search.Searcher
	currMust     *search.DocumentMatch
	root         *SpanClause
	field        string
	locations    []search.Location
	initialized  bool
}

func (s *SpanSearcher) Size() int {
	sizeInBytes := reflectStaticSizeSpanSearcher + sizeOfPtr

	if s.mustSearcher != nil {
		sizeInBytes += s.mustSearcher.Size()
	}

	if s.currMust != nil {
		sizeInBytes += s.currMust.Size()
	}

	return sizeInBytes
}

// NewSpanSearcher creates a searcher matching the root span clause.
// An invalid clause tree results in an error.
func NewSpanSearcher(indexReader search.Reader, root *SpanClause, boost float64, scorer search.Scorer,
	options search.SearcherOptions) (*SpanSearcher, error) {
	if err := root.Validate(); err != nil {
		return nil, err
	}

	options.IncludeTermVectors = true

	// collect the distinct leaf terms, preserving first-seen order,
	// so that repeated terms do not create duplicate constituents
	terms := make([]string, 0)
	seenTerms := make(map[string]struct{})
	var collect func(c *SpanClause)
	collect = func(c *SpanClause) {
		if c.isLeaf() {
			if _, ok := seenTerms[c.Term]; !ok {
				seenTerms[c.Term] = struct{}{}
				terms = append(terms, c.Term)
			}
			return
		}
		for _, child := range c.Clauses {
			collect(child)
		}
	}
	collect(root)

	var termSearchers []search.Searcher
	for _, term := range terms {
		ts, err := NewTermSearcher(indexReader, term, root.Field, boost, scorer, options)
		if err != nil {
			for _, opened := range termSearchers {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("span searcher error building term searcher: %v", err)
		}
		termSearchers = append(termSearchers, ts)
	}

	mustSearcher, err := NewConjunctionSearcher(indexReader, termSearchers,
		similarity.NewCompositeSumScorer(), options)
	if err != nil {
		for _, ts := range termSearchers {
			_ = ts.Close()
		}
		return nil, fmt.Errorf("span searcher error building conjunction searcher: %v", err)
	}

	return &SpanSearcher{
		mustSearcher: mustSearcher,
		root:         root,
		field:        root.Field,
	}, nil
}

func (s *SpanSearcher) initSearchers(ctx *search.Context) error {
	err := s.advanceNextMust(ctx)
	if err != nil {
		return err
	}

	s.initialized = true
	return nil
}

func (s *SpanSearcher) advanceNextMust(ctx *search.Context) error {
	var err error

	if s.mustSearcher != nil {
		if s.currMust != nil {
			ctx.DocumentMatchPool.Put(s.currMust)
		}
		s.currMust, err = s.mustSearcher.Next(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *SpanSearcher) Next(ctx *search.Context) (*search.DocumentMatch, error) {
	if !s.initialized {
		err := s.initSearchers(ctx)
		if err != nil {
			return nil, err
		}
	}

	for s.currMust != nil {
		rv := s.checkCurrMustMatch()

		err := s.advanceNextMust(ctx)
		if err != nil {
			return nil, err
		}

		if rv != nil {
			return rv, nil
		}
	}

	return nil, nil
}

// checkCurrMustMatch determines if the current candidate document
// satisfies the span constraints, returning the document match (with
// only the participating locations attached) when it does.
func (s *SpanSearcher) checkCurrMustMatch() *search.DocumentMatch {
	s.locations = s.currMust.Complete(s.locations)

	locations := s.currMust.Locations
	s.currMust.Locations = nil

	ftls := s.currMust.FieldTermLocations

	for field, tlm := range locations {
		if field != s.field {
			// span constraints only apply within the clause field
			continue
		}
		matches := enumerateSpanMatches(s.root, tlm)
		for _, m := range matches {
			for _, lf := range m.leaves {
				ftls = append(ftls, search.FieldTermLocation{
					Field: field,
					Term:  lf.term,
					Location: search.Location{
						Pos:   lf.loc.Pos,
						Start: lf.loc.Start,
						End:   lf.loc.End,
					},
				})
			}
		}
	}

	if len(ftls) > 0 {
		rv := s.currMust
		s.currMust = nil
		rv.FieldTermLocations = ftls
		return rv
	}

	return nil
}

func (s *SpanSearcher) Advance(ctx *search.Context, number uint64) (*search.DocumentMatch, error) {
	if !s.initialized {
		err := s.initSearchers(ctx)
		if err != nil {
			return nil, err
		}
	}
	if s.currMust != nil {
		if s.currMust.Number >= number {
			return s.Next(ctx)
		}
		ctx.DocumentMatchPool.Put(s.currMust)
	}
	if s.currMust == nil {
		return nil, nil
	}
	var err error
	s.currMust, err = s.mustSearcher.Advance(ctx, number)
	if err != nil {
		return nil, err
	}
	return s.Next(ctx)
}

func (s *SpanSearcher) Count() uint64 {
	return s.mustSearcher.Count()
}

func (s *SpanSearcher) Close() error {
	if s.mustSearcher != nil {
		return s.mustSearcher.Close()
	}
	return nil
}

func (s *SpanSearcher) Min() int {
	return 0
}

func (s *SpanSearcher) DocumentMatchPoolSize() int {
	return s.mustSearcher.DocumentMatchPoolSize() + 1
}
