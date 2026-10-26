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

package bluge

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/blugelabs/bluge/analysis"
	"github.com/blugelabs/bluge/analysis/tokenizer"
	"github.com/blugelabs/bluge/search/highlight"
)

// holeTokenFilter removes the token "zap" while preserving the
// removed position, so the following token is advanced by two
// positions (creating an analyzer level position hole).
type holeTokenFilter struct{}

func (holeTokenFilter) Filter(input analysis.TokenStream) analysis.TokenStream {
	rv := make(analysis.TokenStream, 0, len(input))
	for i, token := range input {
		if string(token.Term) == "zap" {
			if i+1 < len(input) {
				input[i+1].PositionIncr++
			}
			continue
		}
		rv = append(rv, token)
	}
	return rv
}

var holeAnalyzer = &analysis.Analyzer{
	Tokenizer:    tokenizer.NewWhitespaceTokenizer(),
	TokenFilters: []analysis.TokenFilter{holeTokenFilter{}},
}

func newSpanTerm(term string) *SpanTermQuery {
	return NewSpanTermQuery(term)
}

func openSpanTestWriter(t *testing.T) *Writer {
	t.Helper()

	indexWriter, err := OpenWriter(InMemoryOnlyConfig())
	if err != nil {
		t.Fatalf("error opening writer: %v", err)
	}

	// each insert is its own batch/segment so results merge across
	// segment boundaries
	docs := []*Document{
		// mv: same field with two values separated by the default
		// position increment gap (alpha@1 bravo@2, bravo@103 charlie@104)
		NewDocument("mv").
			AddField(NewTextField("body", "alpha bravo").
				SearchTermPositions().StoreValue()).
			AddField(NewTextField("body", "bravo charlie").
				SearchTermPositions().StoreValue()),
		// hole: alpha@1 zap removed, bravo@3
		NewDocument("hole").
			AddField(NewTextField("body", "alpha zap bravo").
				SearchTermPositions().StoreValue().
				WithAnalyzer(holeAnalyzer)),
		// repeat: two windows with repeated words
		NewDocument("repeat").
			AddField(NewTextField("body", "alpha bravo alpha bravo").
				SearchTermPositions().StoreValue()),
		NewDocument("plain").
			AddField(NewTextField("body", "alpha bravo").
				SearchTermPositions().StoreValue()),
		NewDocument("other").
			AddField(NewTextField("body", "nothing here").
				SearchTermPositions().StoreValue()),
	}

	for _, doc := range docs {
		if err = indexWriter.Insert(doc); err != nil {
			t.Fatalf("error inserting doc: %v", err)
		}
	}

	return indexWriter
}

// spanHitLocs runs a query and records the participating positions
// per term keyed by document id.
func spanHitLocs(t *testing.T, indexWriter *Writer, q Query) map[string]map[string][]int {
	t.Helper()

	indexReader, err := indexWriter.Reader()
	if err != nil {
		t.Fatalf("error getting reader: %v", err)
	}
	defer func() {
		_ = indexReader.Close()
	}()

	req := NewTopNSearch(50, q).IncludeLocations().WithStandardAggregations()
	dmi, err := indexReader.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("error searching: %v", err)
	}

	rv := make(map[string]map[string][]int)
	next, err := dmi.Next()
	for err == nil && next != nil {
		var id string
		err = next.VisitStoredFields(func(field string, value []byte) bool {
			if field == "_id" {
				id = string(value)
			}
			return true
		})
		if err != nil {
			t.Fatalf("error visiting stored fields: %v", err)
		}
		locs := make(map[string][]int)
		for term, termLocs := range next.Locations["body"] {
			for _, l := range termLocs {
				locs[term] = append(locs[term], l.Pos)
			}
			sort.Ints(locs[term])
		}
		rv[id] = locs
		next, err = dmi.Next()
	}
	if err != nil {
		t.Fatalf("error iterating results: %v", err)
	}
	return rv
}

func TestSpanQueryOrderedAcrossValuesAndSegments(t *testing.T) {
	indexWriter := openSpanTestWriter(t)
	defer func() {
		_ = indexWriter.Close()
	}()

	ab := NewSpanNearQuery([]Query{newSpanTerm("alpha"), newSpanTerm("bravo")}, 0, true).
		SetField("body")
	got := spanHitLocs(t, indexWriter, ab)
	expect := map[string]map[string][]int{
		// bravo@103 from the second value must not be reported
		"mv":     {"alpha": {1}, "bravo": {2}},
		"repeat": {"alpha": {1, 3}, "bravo": {2, 4}},
		"plain":  {"alpha": {1}, "bravo": {2}},
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("ordered near expected %v got %v", expect, got)
	}

	// running again must produce identical results across segments
	gotAgain := spanHitLocs(t, indexWriter, ab)
	if !reflect.DeepEqual(gotAgain, expect) {
		t.Fatalf("non-deterministic results: %v vs %v", expect, gotAgain)
	}

	// second value matched on its own, without the first value's bravo
	bc := NewSpanNearQuery([]Query{newSpanTerm("bravo"), newSpanTerm("charlie")}, 0, true).
		SetField("body")
	got = spanHitLocs(t, indexWriter, bc)
	expect = map[string]map[string][]int{
		"mv": {"bravo": {103}, "charlie": {104}},
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("second value near expected %v got %v", expect, got)
	}
}

func TestSpanQueryCrossValueBoundarySlop(t *testing.T) {
	indexWriter := openSpanTestWriter(t)
	defer func() {
		_ = indexWriter.Close()
	}()

	// alpha@1 to charlie@104 has 102 unoccupied positions
	base := func(slop int) Query {
		return NewSpanNearQuery([]Query{newSpanTerm("alpha"), newSpanTerm("charlie")}, slop, true).
			SetField("body")
	}

	if got := spanHitLocs(t, indexWriter, base(101)); len(got) != 0 {
		t.Fatalf("expected no cross value match with slop 101, got %v", got)
	}
	got := spanHitLocs(t, indexWriter, base(102))
	expect := map[string]map[string][]int{
		"mv": {"alpha": {1}, "charlie": {104}},
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("expected cross value match with slop 102, got %v", got)
	}
}

func TestSpanQueryAnalyzerPositionHole(t *testing.T) {
	indexWriter := openSpanTestWriter(t)
	defer func() {
		_ = indexWriter.Close()
	}()

	q := NewSpanNearQuery([]Query{newSpanTerm("alpha"), newSpanTerm("bravo")}, 0, true).
		SetField("body")
	if got := spanHitLocs(t, indexWriter, q); len(got["hole"]) != 0 {
		t.Fatalf("hole document must not match with slop 0, got %v", got["hole"])
	}

	q.SetSlop(1)
	got := spanHitLocs(t, indexWriter, q)
	expect := map[string][]int{"alpha": {1}, "bravo": {3}}
	if !reflect.DeepEqual(got["hole"], expect) {
		t.Fatalf("hole document with slop 1 expected %v got %v", expect, got["hole"])
	}
}

func TestSpanQueryNestedAndUnordered(t *testing.T) {
	indexWriter := openSpanTestWriter(t)
	defer func() {
		_ = indexWriter.Close()
	}()

	// (alpha bravo) then (bravo charlie), the two subsequences sit in
	// different values with a 100 position gap between them
	first := NewSpanNearQuery([]Query{newSpanTerm("alpha"), newSpanTerm("bravo")}, 0, true)
	second := NewSpanNearQuery([]Query{newSpanTerm("bravo"), newSpanTerm("charlie")}, 0, true)
	nested := NewSpanNearQuery([]Query{first, second}, 100, true).SetField("body")

	got := spanHitLocs(t, indexWriter, nested)
	expect := map[string]map[string][]int{
		"mv": {"alpha": {1}, "bravo": {2, 103}, "charlie": {104}},
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("nested cross value match expected %v got %v", expect, got)
	}

	nested.SetSlop(99)
	if got = spanHitLocs(t, indexWriter, nested); len(got) != 0 {
		t.Fatalf("nested match must respect outer slop, got %v", got)
	}

	// unordered: bravo charlie before alpha bravo matches the same doc
	unordered := NewSpanNearQuery([]Query{second, first}, 0, false).SetField("body")
	// within a single value there is no such arrangement, but give the
	// outer clause enough slop to bridge the two values
	unordered.SetSlop(100)
	got = spanHitLocs(t, indexWriter, unordered)
	if !reflect.DeepEqual(got["mv"], expect["mv"]) {
		t.Fatalf("unordered nested expected %v got %v", expect["mv"], got["mv"])
	}
}

func TestSpanQueryHighlightOnlyParticipants(t *testing.T) {
	// dedicated writer: trailing tokens never participate in the
	// (alpha bravo) interval and must stay unmarked
	indexWriter, err := OpenWriter(InMemoryOnlyConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = indexWriter.Close()
	}()

	err = indexWriter.Insert(NewDocument("tail").
		AddField(NewTextField("body", "alpha bravo charlie delta").
			SearchTermPositions().StoreValue()))
	if err != nil {
		t.Fatal(err)
	}

	indexReader, err := indexWriter.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = indexReader.Close()
	}()

	q := NewSpanNearQuery([]Query{newSpanTerm("alpha"), newSpanTerm("bravo")}, 0, true).
		SetField("body")
	req := NewTopNSearch(10, q).IncludeLocations()
	dmi, err := indexReader.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	next, err := dmi.Next()
	if err != nil {
		t.Fatal(err)
	}
	if next == nil {
		t.Fatal("expected one match")
	}

	var stored string
	err = next.VisitStoredFields(func(field string, value []byte) bool {
		if field == "body" {
			stored = string(value)
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	fragment := highlight.NewHTMLHighlighter().BestFragment(next.Locations["body"], []byte(stored))
	if fragment != `<mark>alpha</mark> <mark>bravo</mark> charlie delta` {
		t.Fatalf("only matching tokens should be highlighted, got %q", fragment)
	}

	// and the reported locations contain exactly the two participating positions
	locs := next.Locations["body"]
	if len(locs["alpha"]) != 1 || locs["alpha"][0].Pos != 1 ||
		len(locs["bravo"]) != 1 || locs["bravo"][0].Pos != 2 {
		t.Fatalf("unexpected participating locations: %v", locs)
	}
}

func TestSpanQueryIllegalIntervals(t *testing.T) {
	indexWriter := openSpanTestWriter(t)
	defer func() {
		_ = indexWriter.Close()
	}()
	indexReader, err := indexWriter.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = indexReader.Close()
	}()

	expectSearchError := func(t *testing.T, q Query) {
		t.Helper()
		req := NewTopNSearch(10, q)
		_, err := indexReader.Search(context.Background(), req)
		if err == nil {
			t.Fatalf("expected search error")
		}
	}

	tests := []struct {
		name           string
		query          Query
		validateErrors bool
	}{
		{"empty span term", NewSpanTermQuery(""), true},
		{"near without clauses", NewSpanNearQuery(nil, 0, true), true},
		{"near single clause", NewSpanNearQuery([]Query{newSpanTerm("a")}, 0, true), true},
		{"near negative slop", NewSpanNearQuery([]Query{newSpanTerm("a"), newSpanTerm("b")}, -1, true), true},
		{"non-span clause", NewSpanNearQuery([]Query{NewTermQuery("a"), NewTermQuery("b")}, 0, true), true},
		{"field conflict", NewSpanNearQuery([]Query{
			newSpanTerm("a").SetField("body"),
			newSpanTerm("b").SetField("title"),
		}, 0, true), true},
		{"nested non-span clause", NewSpanNearQuery([]Query{
			NewSpanNearQuery([]Query{newSpanTerm("a"), NewTermQuery("b")}, 0, true),
			newSpanTerm("c"),
		}, 0, true).SetField("body"), true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.validateErrors {
				if vq, ok := test.query.(validatableQuery); ok {
					if err := vq.Validate(); err == nil {
						t.Fatalf("expected Validate error for %s", test.name)
					}
				}
			}
			expectSearchError(t, test.query)
		})
	}

	// boolean query construction must surface the interval error
	bad := NewBooleanQuery().AddMust(NewSpanNearQuery(nil, 0, true))
	expectSearchError(t, bad)
}

func TestSpanQueryFieldPropagation(t *testing.T) {
	indexWriter := openSpanTestWriter(t)
	defer func() {
		_ = indexWriter.Close()
	}()

	// field configured only on the leaf clauses
	leaves := NewSpanNearQuery([]Query{
		newSpanTerm("alpha").SetField("body"),
		newSpanTerm("bravo").SetField("body"),
	}, 0, true)
	got := spanHitLocs(t, indexWriter, leaves)
	if len(got) != 3 {
		t.Fatalf("expected 3 hits from leaf field clauses, got %d (%v)", len(got), got)
	}

	// field propagated through a nested near with no field of its own
	inner := NewSpanNearQuery([]Query{newSpanTerm("alpha"), newSpanTerm("bravo")}, 0, true)
	outer := NewSpanNearQuery([]Query{inner, newSpanTerm("charlie")}, 102, true).SetField("body")
	got = spanHitLocs(t, indexWriter, outer)
	if _, ok := got["mv"]; !ok {
		t.Fatalf("expected field propagation through nested clauses to match mv, got %v", got)
	}
}
