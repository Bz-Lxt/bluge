//  Copyright (c) 2022 The Bluge Authors.
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

package bluge

import (
	"context"
	"sort"
	"testing"

	"github.com/blugelabs/bluge/analysis"
	"github.com/blugelabs/bluge/analysis/token"
	"github.com/blugelabs/bluge/analysis/tokenizer"
	"github.com/blugelabs/bluge/search"
)

func newSynonymAnalyzer(t *testing.T, expand bool, rules string) *analysis.Analyzer {
	t.Helper()
	synMap := token.NewSynonymMap(expand)
	if err := synMap.LoadBytes([]byte(rules)); err != nil {
		t.Fatalf("failed to load synonym rules: %v", err)
	}
	return &analysis.Analyzer{
		Tokenizer: tokenizer.NewWhitespaceTokenizer(),
		TokenFilters: []analysis.TokenFilter{
			token.NewLowerCaseFilter(),
			token.NewSynonymFilter(synMap),
		},
	}
}

func newHoleSynonymAnalyzer(t *testing.T) *analysis.Analyzer {
	t.Helper()
	synMap := token.NewSynonymMap(true)
	if err := synMap.LoadBytes([]byte("a c, xyz")); err != nil {
		t.Fatalf("failed to load synonym rules: %v", err)
	}
	stopWords := analysis.NewTokenMap()
	stopWords.AddToken("the")
	return &analysis.Analyzer{
		Tokenizer: tokenizer.NewWhitespaceTokenizer(),
		TokenFilters: []analysis.TokenFilter{
			token.NewLowerCaseFilter(),
			token.NewStopTokensFilter(stopWords),
			token.NewSynonymFilter(synMap),
		},
	}
}

func synonymDoc(id, field, body string, an *analysis.Analyzer) *Document {
	doc := NewDocument(id)
	(*doc)[0].(*TermField).StoreValue()
	doc.AddField(NewTextField(field, body).
		SearchTermPositions().
		WithAnalyzer(an))
	return doc
}

func searchSynonymIDs(t *testing.T, reader *Reader, q Query) []string {
	t.Helper()
	req := NewTopNSearch(100, q)
	dmi, err := reader.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("error executing search: %v", err)
	}
	var ids []string
	var next *search.DocumentMatch
	next, err = dmi.Next()
	for err == nil && next != nil {
		err = next.VisitStoredFields(func(field string, value []byte) bool {
			if field == "_id" {
				ids = append(ids, string(value))
			}
			return true
		})
		if err != nil {
			t.Fatalf("error visiting stored fields: %v", err)
		}
		next, err = dmi.Next()
	}
	if err != nil {
		t.Fatalf("error iterating matches: %v", err)
	}
	sort.Strings(ids)
	return ids
}

func expectIDs(t *testing.T, reader *Reader, q Query, want []string) {
	t.Helper()
	got := searchSynonymIDs(t, reader, q)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("query %q expected %v, got %v", q, want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("query expected %v, got %v", want, got)
		}
	}
}

// TestSynonymExpansionIndexQueryConsistency verifies that documents indexed
// through an expansion synonym analyzer are found consistently by MatchQuery
// and MatchPhraseQuery using the same analyzer, including multi-word rules,
// and that a phrase never crosses the gap between two values of one field.
func TestSynonymExpansionIndexQueryConsistency(t *testing.T) {
	an := newSynonymAnalyzer(t, true, "united states, usa\nhappy, glad\n")

	w, err := OpenWriter(InMemoryOnlyConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	docs := []*Document{
		synonymDoc("d1", "body", "united states", an),
		synonymDoc("d2", "body", "usa", an),
		synonymDoc("d3", "body", "the united kingdom", an),
		synonymDoc("d6", "body", "happy day", an),
		synonymDoc("d7", "body", "glad night", an),
	}
	// d4 indexes two values into the same field
	d4 := NewDocument("d4")
	(*d4)[0].(*TermField).StoreValue()
	d4.AddField(NewTextField("body", "united").SearchTermPositions().WithAnalyzer(an))
	d4.AddField(NewTextField("body", "states").SearchTermPositions().WithAnalyzer(an))
	docs = append(docs, d4)

	batch := NewBatch()
	for _, d := range docs {
		batch.Insert(d)
	}
	if err = w.Batch(batch); err != nil {
		t.Fatal(err)
	}

	reader, err := w.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()

	matchQ := func(text string) Query {
		return NewMatchQuery(text).SetField("body").SetAnalyzer(an)
	}
	phraseQ := func(text string) Query {
		return NewMatchPhraseQuery(text).SetField("body").SetAnalyzer(an)
	}

	// MatchQuery: single-word and multi-word synonyms expand both ways
	expectIDs(t, reader, matchQ("usa"), []string{"d1", "d2", "d3", "d4"})
	expectIDs(t, reader, matchQ("happy"), []string{"d6", "d7"})
	expectIDs(t, reader, matchQ("glad"), []string{"d6", "d7"})
	// a document indexed as "usa" also contains the states token from
	// index-time expansion, so term queries stay symmetric
	expectIDs(t, reader, matchQ("states"), []string{"d1", "d2", "d4"})

	// MatchPhraseQuery: "united states" and "usa" are interchangeable and
	// match the same documents on both sides of the index/query boundary
	expectIDs(t, reader, phraseQ("united states"), []string{"d1", "d2"})
	expectIDs(t, reader, phraseQ("usa"), []string{"d1", "d2"})

	// a reversed phrase does not match
	expectIDs(t, reader, phraseQ("states usa"), nil)

	// the phrase must not cross the position gap between the two field values
	expectIDs(t, reader, phraseQ("united kingdom"), []string{"d3"})

	// single-word synonyms inside a phrase stay phrase consistent:
	// "glad day" matches the document indexed as "happy day" and vice versa
	expectIDs(t, reader, phraseQ("happy day"), []string{"d6"})
	expectIDs(t, reader, phraseQ("glad day"), []string{"d6"})
	expectIDs(t, reader, phraseQ("glad night"), []string{"d7"})
	expectIDs(t, reader, phraseQ("happy night"), []string{"d7"})
}

// TestSynonymNormalizationIndexQueryConsistency verifies normalization mode
// (expand disabled): non-canonical sequences are stacked on the canonical
// sequence, which keeps index and query analysis consistent.
func TestSynonymNormalizationIndexQueryConsistency(t *testing.T) {
	an := newSynonymAnalyzer(t, false, "tv, tele vision\n")

	w, err := OpenWriter(InMemoryOnlyConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	batch := NewBatch()
	batch.Insert(synonymDoc("d10", "body", "tele vision", an))
	batch.Insert(synonymDoc("d11", "body", "tv", an))
	batch.Insert(synonymDoc("d12", "body", "radio vision", an))
	if err = w.Batch(batch); err != nil {
		t.Fatal(err)
	}

	reader, err := w.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()

	matchQ := func(text string) Query {
		return NewMatchQuery(text).SetField("body").SetAnalyzer(an)
	}
	phraseQ := func(text string) Query {
		return NewMatchPhraseQuery(text).SetField("body").SetAnalyzer(an)
	}

	expectIDs(t, reader, matchQ("tv"), []string{"d10", "d11"})
	expectIDs(t, reader, matchQ("tele"), []string{"d10"})
	expectIDs(t, reader, phraseQ("tv"), []string{"d10", "d11"})
	expectIDs(t, reader, phraseQ("tele vision"), []string{"d10"})
}

// TestSynonymPhraseDoesNotSpanPositionHole verifies that a stop-word induced
// position hole prevents a multi-word synonym from firing, so a phrase can
// neither match across the hole nor pick up the generated synonym token.
func TestSynonymPhraseDoesNotSpanPositionHole(t *testing.T) {
	an := newHoleSynonymAnalyzer(t)

	w, err := OpenWriter(InMemoryOnlyConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	batch := NewBatch()
	batch.Insert(synonymDoc("d20", "body", "a the c end", an))
	batch.Insert(synonymDoc("d21", "body", "a c end", an))
	if err = w.Batch(batch); err != nil {
		t.Fatal(err)
	}

	reader, err := w.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()

	phraseQ := func(text string) Query {
		return NewMatchPhraseQuery(text).SetField("body").SetAnalyzer(an)
	}

	// "a c" is adjacent only in d21
	expectIDs(t, reader, phraseQ("a c"), []string{"d21"})
	// the synonym of "a c" must only be present where the words are adjacent
	expectIDs(t, reader, phraseQ("xyz end"), []string{"d21"})

	// token level: the synonym token is not generated across the hole
	gappy := NewTextField("body", "a the c").WithAnalyzer(an)
	gappy.Analyze(0)
	if tf := gappy.AnalyzedTokenFrequencies()["xyz"]; tf != nil {
		t.Fatalf("expected no xyz token across position hole, got frequency %d", tf.Frequency())
	}
	adjacent := NewTextField("body", "a c").WithAnalyzer(an)
	adjacent.Analyze(0)
	if tf := adjacent.AnalyzedTokenFrequencies()["xyz"]; tf == nil || tf.Frequency() != 1 {
		t.Fatalf("expected xyz frequency 1 for adjacent input, got %v", tf)
	}
}

// TestSynonymNoDuplicateScoring verifies that each term is emitted at most
// once per position, even with repeated rules, so term frequencies used by
// scoring and phrase matching are not inflated.
func TestSynonymNoDuplicateScoring(t *testing.T) {
	an := newSynonymAnalyzer(t, true, "happy, glad\n")

	f := NewTextField("body", "happy").WithAnalyzer(an)
	f.Analyze(0)
	freqs := f.AnalyzedTokenFrequencies()
	if tf := freqs["happy"]; tf == nil || tf.Frequency() != 1 {
		t.Fatalf("expected happy frequency 1, got %v", tf)
	}
	if tf := freqs["glad"]; tf == nil || tf.Frequency() != 1 {
		t.Fatalf("expected glad frequency 1, got %v", tf)
	}

	dupAn := newSynonymAnalyzer(t, true, "z => q\nz => q\nz => q\n")
	f2 := NewTextField("body", "z").WithAnalyzer(dupAn)
	f2.Analyze(0)
	freqs2 := f2.AnalyzedTokenFrequencies()
	if tf := freqs2["q"]; tf == nil || tf.Frequency() != 1 {
		t.Fatalf("expected q frequency 1 despite repeated rules, got %v", tf)
	}
	if tf := freqs2["z"]; tf == nil || tf.Frequency() != 1 {
		t.Fatalf("expected z frequency 1, got %v", tf)
	}
}

// TestSynonymRulesTerminateOnCycles verifies end to end that cyclic rules do
// not expand indefinitely and produce exactly one stacked alternative.
func TestSynonymRulesTerminateOnCycles(t *testing.T) {
	an := newSynonymAnalyzer(t, true, "a => b\nb => a\n")

	f := NewTextField("body", "a").WithAnalyzer(an)
	f.Analyze(0)
	freqs := f.AnalyzedTokenFrequencies()
	if len(freqs) != 2 {
		t.Fatalf("expected exactly 2 distinct terms, got %d: %v", len(freqs), freqs)
	}
	if tf := freqs["a"]; tf == nil || tf.Frequency() != 1 {
		t.Fatalf("expected a frequency 1, got %v", tf)
	}
	if tf := freqs["b"]; tf == nil || tf.Frequency() != 1 {
		t.Fatalf("expected b frequency 1, got %v", tf)
	}
}
