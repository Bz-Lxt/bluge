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
	"reflect"
	"strings"
	"testing"

	"github.com/blugelabs/bluge/analysis"
)

func TestSynonymFilter(t *testing.T) {
	tests := []struct {
		expand bool
		rules  string
		input  analysis.TokenStream
		output analysis.TokenStream
	}{
		{
			expand: true,
			rules:  "happy, glad, joyful",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("happy"), Start: 0, End: 5, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("happy"), Start: 0, End: 5, PositionIncr: 1},
				&analysis.Token{Term: []byte("glad"), Start: 0, End: 5, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("joyful"), Start: 0, End: 5, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			expand: true,
			rules:  "happy, glad, joyful",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("glad"), Start: 0, End: 4, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("glad"), Start: 0, End: 4, PositionIncr: 1},
				&analysis.Token{Term: []byte("happy"), Start: 0, End: 4, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("joyful"), Start: 0, End: 4, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			expand: true,
			rules:  "united states, usa",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("united"), Start: 0, End: 6, PositionIncr: 1},
				&analysis.Token{Term: []byte("states"), Start: 7, End: 13, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("united"), Start: 0, End: 6, PositionIncr: 1},
				&analysis.Token{Term: []byte("usa"), Start: 0, End: 6, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("states"), Start: 7, End: 13, PositionIncr: 1},
			},
		},
		{
			expand: true,
			rules:  "united states, usa",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("usa"), Start: 0, End: 3, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("usa"), Start: 0, End: 3, PositionIncr: 1},
				&analysis.Token{Term: []byte("united"), Start: 0, End: 3, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("states"), Start: 3, End: 3, PositionIncr: 1, Type: analysis.Synonym},
			},
		},
		{
			expand: true,
			rules:  "united states, usa",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("x"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("united"), Start: 2, End: 8, PositionIncr: 1},
				&analysis.Token{Term: []byte("states"), Start: 9, End: 15, PositionIncr: 1},
				&analysis.Token{Term: []byte("y"), Start: 16, End: 17, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("x"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("united"), Start: 2, End: 8, PositionIncr: 1},
				&analysis.Token{Term: []byte("usa"), Start: 2, End: 8, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("states"), Start: 9, End: 15, PositionIncr: 1},
				&analysis.Token{Term: []byte("y"), Start: 16, End: 17, PositionIncr: 1},
			},
		},
		{
			expand: false,
			rules:  "happy, glad, joyful",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("glad"), Start: 0, End: 4, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("glad"), Start: 0, End: 4, PositionIncr: 1},
				&analysis.Token{Term: []byte("happy"), Start: 0, End: 4, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			// canonical first side is left untouched in normalization mode
			expand: false,
			rules:  "happy, glad, joyful",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("happy"), Start: 0, End: 5, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("happy"), Start: 0, End: 5, PositionIncr: 1},
			},
		},
		{
			// explicit mapping replaces nothing, originals are retained
			expand: true,
			rules:  "a b => x",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
				&analysis.Token{Term: []byte("c"), Start: 4, End: 5, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("x"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
				&analysis.Token{Term: []byte("c"), Start: 4, End: 5, PositionIncr: 1},
			},
		},
		{
			// explicit mappings ignore the expand setting
			expand: false,
			rules:  "a b => x",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("x"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
			},
		},
		{
			// single word expands to a multi-word sequence
			expand: true,
			rules:  "ipod => i pod",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("ipod"), Start: 0, End: 4, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("ipod"), Start: 0, End: 4, PositionIncr: 1},
				&analysis.Token{Term: []byte("i"), Start: 0, End: 4, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("pod"), Start: 4, End: 4, PositionIncr: 1, Type: analysis.Synonym},
			},
		},
		{
			// multiple right-hand alternatives, one of them multi-word
			expand: true,
			rules:  "a => b, c d",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("c"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("d"), Start: 1, End: 1, PositionIncr: 1, Type: analysis.Synonym},
			},
		},
		{
			// multiple left-hand alternatives
			expand: true,
			rules:  "a, b => c",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("c"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			// multi-word to multi-word explicit mapping
			expand: true,
			rules:  "a b => c d",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("c"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
				&analysis.Token{Term: []byte("d"), Start: 2, End: 3, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			// longest overlapping source sequence wins
			expand: true,
			rules:  "a => x\na b => y",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("y"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
			},
		},
		{
			// no overlap, the shorter word rule still fires
			expand: true,
			rules:  "a => x\na b => y",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("c"), Start: 2, End: 3, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 4, End: 5, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("x"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
				&analysis.Token{Term: []byte("c"), Start: 2, End: 3, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 4, End: 5, PositionIncr: 1},
			},
		},
		{
			// a multi-word rule must not span a position hole
			expand: true,
			rules:  "a b => x",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 3, End: 4, PositionIncr: 2},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 3, End: 4, PositionIncr: 2},
			},
		},
		{
			// a hole before the matched word is preserved
			expand: true,
			rules:  "a => x",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("q"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("a"), Start: 3, End: 4, PositionIncr: 2},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("q"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("a"), Start: 3, End: 4, PositionIncr: 2},
				&analysis.Token{Term: []byte("x"), Start: 3, End: 4, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			// synonym word identical to the original is not emitted twice
			expand: true,
			rules:  "a b, a c",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
				&analysis.Token{Term: []byte("c"), Start: 2, End: 3, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			// duplicated right-hand words are emitted once
			expand: true,
			rules:  "a => b, b",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			// repeated equivalent groups do not produce repeated words
			expand: true,
			rules:  "a, b, a",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			// explicit rule cycles terminate after one expansion
			expand: true,
			rules:  "a => b\nb => a",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			// equivalent rule cycles terminate after one expansion
			expand: true,
			rules:  "a, b",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 1},
				&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
			},
		},
		{
			// unmatched tokens pass through unchanged
			expand: true,
			rules:  "a => x",
			input: analysis.TokenStream{
				&analysis.Token{Term: []byte("q"), Start: 0, End: 1, PositionIncr: 1},
			},
			output: analysis.TokenStream{
				&analysis.Token{Term: []byte("q"), Start: 0, End: 1, PositionIncr: 1},
			},
		},
	}

	for _, test := range tests {
		synMap := NewSynonymMap(test.expand)
		if err := synMap.LoadBytes([]byte(test.rules)); err != nil {
			t.Fatalf("error loading rules %q: %v", test.rules, err)
		}
		filter := NewSynonymFilter(synMap)
		actual := filter.Filter(test.input)
		if !reflect.DeepEqual(actual, test.output) {
			t.Errorf("rules %q expected %s, got %s", test.rules, test.output, actual)
		}
	}
}

// TestSynonymFilterStateless verifies that reusing one filter instance on
// independent token streams never leaks state across invocations. This is
// what keeps multi-word rules from crossing the position gap between two
// values of the same field.
func TestSynonymFilterStateless(t *testing.T) {
	synMap := NewSynonymMap(true)
	if err := synMap.LoadBytes([]byte("a b => x\na => y")); err != nil {
		t.Fatal(err)
	}
	filter := NewSynonymFilter(synMap)

	first := analysis.TokenStream{
		&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
		&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
	}
	firstExpected := analysis.TokenStream{
		&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
		&analysis.Token{Term: []byte("x"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
		&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
	}
	actual := filter.Filter(first)
	if !reflect.DeepEqual(actual, firstExpected) {
		t.Fatalf("first invocation expected %s, got %s", firstExpected, actual)
	}

	// a stream split across two invocations must not match the phrase rule
	second := analysis.TokenStream{
		&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
	}
	secondExpected := analysis.TokenStream{
		&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
		&analysis.Token{Term: []byte("y"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
	}
	actual = filter.Filter(second)
	if !reflect.DeepEqual(actual, secondExpected) {
		t.Fatalf("second invocation expected %s, got %s", secondExpected, actual)
	}

	third := analysis.TokenStream{
		&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 1},
	}
	actual = filter.Filter(third)
	if !reflect.DeepEqual(actual, third) {
		t.Fatalf("third invocation expected %s, got %s", third, actual)
	}

	// running the first stream again must give the same result
	actual = filter.Filter(analysis.TokenStream{
		&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
		&analysis.Token{Term: []byte("b"), Start: 2, End: 3, PositionIncr: 1},
	})
	if !reflect.DeepEqual(actual, firstExpected) {
		t.Fatalf("fourth invocation expected %s, got %s", firstExpected, actual)
	}
}

// TestSynonymFilterEmptyMap verifies an empty map leaves the stream untouched.
func TestSynonymFilterEmptyMap(t *testing.T) {
	input := analysis.TokenStream{
		&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
	}
	actual := NewSynonymFilter(NewSynonymMap(true)).Filter(input)
	if !reflect.DeepEqual(actual, input) {
		t.Fatalf("expected %s, got %s", input, actual)
	}
}

func TestSynonymIllegalRules(t *testing.T) {
	illegal := []string{
		"a,,b",
		"a,",
		",a",
		"a =>",
		"=> a",
		"a => b => c",
		"a => , b",
		"a, => b",
		"a , , b => c",
	}
	for _, rule := range illegal {
		synMap := NewSynonymMap(true)
		if err := synMap.LoadLine(rule); err == nil {
			t.Errorf("expected error for illegal rule %q, got none", rule)
		}
	}
}

func TestSynonymLegalRules(t *testing.T) {
	legal := []string{
		"",
		"   ",
		"# full line comment",
		"a , b",
		"united   states , usa",
		"a, b # trailing comment",
		"c#sharp, c-sharp", // '#' inside a word is not a comment
		"lonely",           // a single entry is a valid no-op
	}
	for _, rule := range legal {
		synMap := NewSynonymMap(true)
		if err := synMap.LoadLine(rule); err != nil {
			t.Errorf("unexpected error for rule %q: %v", rule, err)
		}
	}
}

func TestSynonymLoadBytesLineNumbers(t *testing.T) {
	data := []byte("# header comment\n\na,,b\n")
	synMap := NewSynonymMap(true)
	err := synMap.LoadBytes(data)
	if err == nil {
		t.Fatal("expected error, got none")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("expected error to reference line 3, got: %v", err)
	}
}

func TestSynonymMergedRules(t *testing.T) {
	// identical rules loaded on separate lines merge without duplicates
	synMap := NewSynonymMap(true)
	if err := synMap.LoadBytes([]byte("a => b\na => b\n")); err != nil {
		t.Fatal(err)
	}
	actual := NewSynonymFilter(synMap).Filter(analysis.TokenStream{
		&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
	})
	expected := analysis.TokenStream{
		&analysis.Token{Term: []byte("a"), Start: 0, End: 1, PositionIncr: 1},
		&analysis.Token{Term: []byte("b"), Start: 0, End: 1, PositionIncr: 0, Type: analysis.Synonym},
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("expected %s, got %s", expected, actual)
	}
}
