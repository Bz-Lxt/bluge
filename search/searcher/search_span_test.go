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
	"reflect"
	"sort"
	"testing"

	"github.com/blugelabs/bluge/search"
	"github.com/blugelabs/bluge/search/similarity"

	segment "github.com/blugelabs/bluge_segment_api"
)

func newSpanTestReader() *stubIndexReader {
	r := newStubIndexReader()
	for _, doc := range spanTestDocs {
		r.add(doc)
	}
	return r
}

var spanTestDocs = []segment.Document{
	// 0: plain ordered
	&FakeDocument{
		NewFakeField("_id", "0", true, false, false, nil),
		NewFakeField("desc", "a b c d", false, true, true, nil),
	},
	// 1: repeated words
	&FakeDocument{
		NewFakeField("_id", "1", true, false, false, nil),
		NewFakeField("desc", "a b a b", false, true, true, nil),
	},
	// 2: position hole between a and b
	&FakeDocument{
		NewFakeField("_id", "2", true, false, false, nil),
		NewFakeField("desc", "a x b", false, true, true, nil),
	},
	// 3: reversed order
	&FakeDocument{
		NewFakeField("_id", "3", true, false, false, nil),
		NewFakeField("desc", "b a", false, true, true, nil),
	},
	// 4: two repeated windows
	&FakeDocument{
		NewFakeField("_id", "4", true, false, false, nil),
		NewFakeField("desc", "a b c a b c", false, true, true, nil),
	},
	// 5: repeated leading word
	&FakeDocument{
		NewFakeField("_id", "5", true, false, false, nil),
		NewFakeField("desc", "a a b", false, true, true, nil),
	},
}

func spanTestOptions() search.SearcherOptions {
	return search.SearcherOptions{
		SimilarityForField: func(field string) search.Similarity {
			return similarity.NewBM25Similarity()
		},
		Explain:            true,
		IncludeTermVectors: true,
	}
}

func termClause(term string) *SpanClause {
	return &SpanClause{Field: "desc", Term: term}
}

func nearClause(clauses []*SpanClause, slop int, inOrder bool) *SpanClause {
	return &SpanClause{Field: "desc", Clauses: clauses, Slop: slop, InOrder: inOrder}
}

// collectSpanPositions executes a span searcher and records the
// participating positions per term per document.
func collectSpanPositions(t *testing.T, reader search.Reader, root *SpanClause) map[uint64]map[string][]int {
	t.Helper()

	s, err := NewSpanSearcher(reader, root, 1.0, nil, spanTestOptions())
	if err != nil {
		t.Fatalf("error building span searcher: %v", err)
	}
	defer func() {
		_ = s.Close()
	}()

	ctx := &search.Context{
		DocumentMatchPool: search.NewDocumentMatchPool(s.DocumentMatchPoolSize(), 0),
	}

	rv := make(map[uint64]map[string][]int)
	next, err := s.Next(ctx)
	for err == nil && next != nil {
		next.Complete(nil)
		docLocs := make(map[string][]int)
		for term, locs := range next.Locations["desc"] {
			for _, loc := range locs {
				docLocs[term] = append(docLocs[term], loc.Pos)
			}
			sort.Ints(docLocs[term])
		}
		rv[next.Number] = docLocs
		next, err = s.Next(ctx)
	}
	if err != nil {
		t.Fatalf("error iterating span searcher: %v", err)
	}
	return rv
}

func TestSpanSearcherLeaf(t *testing.T) {
	got := collectSpanPositions(t, newSpanTestReader(), termClause("a"))
	expect := map[uint64]map[string][]int{
		0: {"a": {1}},
		1: {"a": {1, 3}},
		2: {"a": {1}},
		3: {"a": {2}},
		4: {"a": {1, 4}},
		5: {"a": {1, 2}},
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("leaf expected %v got %v", expect, got)
	}
}

func TestSpanSearcherOrderedNear(t *testing.T) {
	tests := []struct {
		slop   int
		expect map[uint64]map[string][]int
	}{
		{
			slop: 0,
			expect: map[uint64]map[string][]int{
				0: {"a": {1}, "b": {2}},
				1: {"a": {1, 3}, "b": {2, 4}},
				// doc 2 (a x b) has a one position hole, no match
				// doc 3 (b a) is reversed, no match
				4: {"a": {1, 4}, "b": {2, 5}},
				// only the second a@2 is adjacent to b@3; a@1 must not be reported
				5: {"a": {2}, "b": {3}},
			},
		},
		{
			slop: 1,
			expect: map[uint64]map[string][]int{
				0: {"a": {1}, "b": {2}},
				1: {"a": {1, 3}, "b": {2, 4}},
				2: {"a": {1}, "b": {3}},
				4: {"a": {1, 4}, "b": {2, 5}},
				5: {"a": {1, 2}, "b": {3}},
			},
		},
	}

	for _, test := range tests {
		root := nearClause([]*SpanClause{termClause("a"), termClause("b")}, test.slop, true)
		got := collectSpanPositions(t, newSpanTestReader(), root)
		if !reflect.DeepEqual(got, test.expect) {
			t.Fatalf("ordered near slop %d expected %v got %v", test.slop, test.expect, got)
		}
	}
}

func TestSpanSearcherUnorderedNear(t *testing.T) {
	root := nearClause([]*SpanClause{termClause("a"), termClause("b")}, 0, false)
	got := collectSpanPositions(t, newSpanTestReader(), root)
	expect := map[uint64]map[string][]int{
		0: {"a": {1}, "b": {2}},
		// unordered also matches b@2 next to a@3
		1: {"a": {1, 3}, "b": {2, 4}},
		// reversed document matches in either order
		3: {"a": {2}, "b": {1}},
		4: {"a": {1, 4}, "b": {2, 5}},
		5: {"a": {2}, "b": {3}},
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("unordered near expected %v got %v", expect, got)
	}
}

func TestSpanSearcherRepeatedTerm(t *testing.T) {
	tests := []struct {
		slop   int
		expect map[uint64]map[string][]int
	}{
		{
			slop: 0,
			expect: map[uint64]map[string][]int{
				5: {"a": {1, 2}},
			},
		},
		{
			slop: 2,
			expect: map[uint64]map[string][]int{
				1: {"a": {1, 3}},
				4: {"a": {1, 4}},
				5: {"a": {1, 2}},
			},
		},
	}

	for _, test := range tests {
		root := nearClause([]*SpanClause{termClause("a"), termClause("a")}, test.slop, true)
		got := collectSpanPositions(t, newSpanTestReader(), root)
		if !reflect.DeepEqual(got, test.expect) {
			t.Fatalf("repeated term slop %d expected %v got %v", test.slop, test.expect, got)
		}
	}
}

func TestSpanSearcherNested(t *testing.T) {
	// (a b)~1 ordered, followed directly by c in order
	inner := nearClause([]*SpanClause{termClause("a"), termClause("b")}, 1, true)
	rootOrdered := nearClause([]*SpanClause{inner, termClause("c")}, 0, true)
	got := collectSpanPositions(t, newSpanTestReader(), rootOrdered)
	expect := map[uint64]map[string][]int{
		0: {"a": {1}, "b": {2}, "c": {3}},
		4: {"a": {1, 4}, "b": {2, 5}, "c": {3, 6}},
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("nested ordered expected %v got %v", expect, got)
	}

	// (a b)~0 in either order relative to c
	inner0 := nearClause([]*SpanClause{termClause("a"), termClause("b")}, 0, true)
	rootUnordered := nearClause([]*SpanClause{inner0, termClause("c")}, 0, false)
	got = collectSpanPositions(t, newSpanTestReader(), rootUnordered)
	// in doc 4 c@3 may sit before the a@4 b@5 subsequence
	expect = map[uint64]map[string][]int{
		0: {"a": {1}, "b": {2}, "c": {3}},
		4: {"a": {1, 4}, "b": {2, 5}, "c": {3, 6}},
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("nested unordered expected %v got %v", expect, got)
	}
}

func TestSpanSearcherLocationsOnlyContainParticipants(t *testing.T) {
	// doc 0 contains c@3 and d@4, neither participates in a b
	root := nearClause([]*SpanClause{termClause("a"), termClause("b")}, 0, true)
	got := collectSpanPositions(t, newSpanTestReader(), root)
	if locs, ok := got[0]; !ok {
		t.Fatalf("expected doc 0 to match")
	} else if _, present := locs["c"]; present {
		t.Fatalf("term c must not be reported as a match participant: %v", locs)
	} else if _, present := locs["d"]; present {
		t.Fatalf("term d must not be reported as a match participant: %v", locs)
	}
}

func loc(term string, pos int) spanLeaf {
	return spanLeaf{term: term, loc: &search.Location{Pos: pos, Start: pos, End: pos + 1}}
}

func tlmFor(leaves ...spanLeaf) search.TermLocationMap {
	tlm := make(search.TermLocationMap)
	for _, lf := range leaves {
		tlm[lf.term] = append(tlm[lf.term], lf.loc)
	}
	return tlm
}

// matchPositionSets enumerates a clause and returns each match as the
// sorted set of its participating positions.
func matchPositionSets(c *SpanClause, tlm search.TermLocationMap) [][]int {
	matches := enumerateSpanMatches(c, tlm)
	rv := make([][]int, 0, len(matches))
	for _, m := range matches {
		var positions []int
		for _, lf := range m.leaves {
			positions = append(positions, lf.loc.Pos)
		}
		sort.Ints(positions)
		rv = append(rv, positions)
	}
	sort.Slice(rv, func(i, j int) bool {
		for k := 0; k < len(rv[i]) && k < len(rv[j]); k++ {
			if rv[i][k] != rv[j][k] {
				return rv[i][k] < rv[j][k]
			}
		}
		return len(rv[i]) < len(rv[j])
	})
	return rv
}

func TestEnumerateSpanMatchesNoPositionReuse(t *testing.T) {
	// two terms at the very same position must never match
	tlm := tlmFor(loc("a", 1), loc("b", 1))
	clause := nearClause([]*SpanClause{termClause("a"), termClause("b")}, 100, false)
	if got := matchPositionSets(clause, tlm); len(got) != 0 {
		t.Fatalf("expected no matches for co-located terms, got %v", got)
	}
}

func TestEnumerateSpanMatchesHoleSlop(t *testing.T) {
	tlm := tlmFor(loc("a", 1), loc("b", 3))
	clause := nearClause([]*SpanClause{termClause("a"), termClause("b")}, 0, true)
	if got := matchPositionSets(clause, tlm); len(got) != 0 {
		t.Fatalf("expected no match with slop 0 across a hole, got %v", got)
	}
	clause.Slop = 1
	if got := matchPositionSets(clause, tlm); !reflect.DeepEqual(got, [][]int{{1, 3}}) {
		t.Fatalf("expected single match [1 3] with slop 1, got %v", got)
	}
}

func TestEnumerateSpanMatchesNestedSlopIsolation(t *testing.T) {
	// a@1 b@4 (two unoccupied positions) then c@5 directly after b
	tlm := tlmFor(loc("a", 1), loc("b", 4), loc("c", 5))
	inner := nearClause([]*SpanClause{termClause("a"), termClause("b")}, 2, true)
	outer := nearClause([]*SpanClause{inner, termClause("c")}, 0, true)
	if got := matchPositionSets(outer, tlm); !reflect.DeepEqual(got, [][]int{{1, 4, 5}}) {
		t.Fatalf("inner hole must not consume outer slop, got %v", got)
	}

	// without enough inner slop there is no match
	inner.Slop = 1
	if got := matchPositionSets(outer, tlm); len(got) != 0 {
		t.Fatalf("expected no match when inner slop is exhausted, got %v", got)
	}
}

func TestEnumerateSpanMatchesNestedIntervalsDoNotInterleave(t *testing.T) {
	// (a c)~1 spans positions 1..3, b@2 sits in its hole: even though
	// no concrete position is shared, the intervals overlap and the
	// outer near clause must reject the combination
	tlm := tlmFor(loc("a", 1), loc("b", 2), loc("c", 3))
	inner := nearClause([]*SpanClause{termClause("a"), termClause("c")}, 1, true)
	outer := nearClause([]*SpanClause{inner, termClause("b")}, 100, false)
	if got := matchPositionSets(outer, tlm); len(got) != 0 {
		t.Fatalf("nested spans must not interleave through a hole, got %v", got)
	}
}

func TestEnumerateSpanMatchesDuplicatesAndOrdering(t *testing.T) {
	// reversed, unsorted input must be handled deterministically
	tlm := search.TermLocationMap{
		"a": {&search.Location{Pos: 4, Start: 0, End: 1}, &search.Location{Pos: 1, Start: 0, End: 1}},
		"b": {&search.Location{Pos: 2, Start: 0, End: 1}},
	}
	clause := nearClause([]*SpanClause{termClause("a"), termClause("b")}, 1, true)
	got := matchPositionSets(clause, tlm)
	// a@1 b@2 is one match; a@4 b@2 reversed and too far
	if !reflect.DeepEqual(got, [][]int{{1, 2}}) {
		t.Fatalf("expected single deterministic match, got %v", got)
	}

	// unordered near of two identical clauses enumerates one match,
	// not both permutations
	tlm2 := tlmFor(loc("a", 1), loc("a", 2))
	same := nearClause([]*SpanClause{termClause("a"), termClause("a")}, 0, false)
	if got := matchPositionSets(same, tlm2); !reflect.DeepEqual(got, [][]int{{1, 2}}) {
		t.Fatalf("expected permutations to de-duplicate, got %v", got)
	}

	// exact duplicate locations are ignored
	tlm3 := search.TermLocationMap{
		"a": {
			&search.Location{Pos: 1, Start: 0, End: 1},
			&search.Location{Pos: 1, Start: 0, End: 1},
		},
		"b": {&search.Location{Pos: 2, Start: 0, End: 1}},
	}
	if got := matchPositionSets(clause, tlm3); !reflect.DeepEqual(got, [][]int{{1, 2}}) {
		t.Fatalf("expected duplicate locations to be ignored, got %v", got)
	}
}

func TestSpanClauseValidation(t *testing.T) {
	reader := newSpanTestReader()

	tests := []struct {
		name string
		root *SpanClause
	}{
		{"nil clause", nil},
		{"missing field", &SpanClause{Term: "a"}},
		{"empty term", &SpanClause{Field: "desc"}},
		{"single near clause", &SpanClause{Field: "desc", Clauses: []*SpanClause{termClause("a")}}},
		{"negative slop", &SpanClause{Field: "desc", Slop: -1, Clauses: []*SpanClause{termClause("a"), termClause("b")}}},
		{"nil child", &SpanClause{Field: "desc", Clauses: []*SpanClause{termClause("a"), nil}}},
		{"field mismatch", &SpanClause{Field: "desc", Clauses: []*SpanClause{
			termClause("a"),
			&SpanClause{Field: "title", Term: "b"},
		}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, err := NewSpanSearcher(reader, test.root, 1.0, nil, spanTestOptions())
			if err == nil {
				_ = s.Close()
				t.Fatalf("expected error for %s", test.name)
			}
		})
	}
}

func TestSpanSearcherAdvance(t *testing.T) {
	root := nearClause([]*SpanClause{termClause("a"), termClause("b")}, 0, true)
	s, err := NewSpanSearcher(newSpanTestReader(), root, 1.0, nil, spanTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.Close()
	}()

	ctx := &search.Context{
		DocumentMatchPool: search.NewDocumentMatchPool(s.DocumentMatchPoolSize(), 0),
	}

	match, err := s.Advance(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if match == nil || match.Number != 4 {
		t.Fatalf("expected to advance to doc 4, got %v", match)
	}
}
