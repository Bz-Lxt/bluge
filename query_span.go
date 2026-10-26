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
	"fmt"

	"github.com/blugelabs/bluge/search"
	"github.com/blugelabs/bluge/search/searcher"
)

// SpanQuery is implemented by queries able to participate in
// positional span matching.  Span queries always match inside one
// field and keep track of the token positions they use.
type SpanQuery interface {
	Query

	// spanClause builds the searcher level clause tree, resolving an
	// unset field against the provided default field.
	spanClause(defaultField string) (*searcher.SpanClause, error)
}

// explicitSpanField returns the field explicitly configured on a span
// query, or the empty string when it is unset.
func explicitSpanField(q Query) string {
	if fq, ok := q.(interface{ Field() string }); ok {
		return fq.Field()
	}
	return ""
}

// SpanTermQuery matches occurrences of a single term as positional
// spans.  It is used both on its own and as a leaf clause of a
// SpanNearQuery.
type SpanTermQuery struct {
	term   string
	field  string
	boost  *boost
	scorer search.Scorer
}

// NewSpanTermQuery creates a span query matching the specified term.
func NewSpanTermQuery(term string) *SpanTermQuery {
	return &SpanTermQuery{
		term: term,
	}
}

// Term returns the exact term being queried
func (q *SpanTermQuery) Term() string {
	return q.term
}

func (q *SpanTermQuery) SetField(f string) *SpanTermQuery {
	q.field = f
	return q
}

// Field returns the field being queried
func (q *SpanTermQuery) Field() string {
	return q.field
}

func (q *SpanTermQuery) SetBoost(b float64) *SpanTermQuery {
	boostVal := boost(b)
	q.boost = &boostVal
	return q
}

// Boost returns the query boost
func (q *SpanTermQuery) Boost() float64 {
	return q.boost.Value()
}

func (q *SpanTermQuery) spanClause(defaultField string) (*searcher.SpanClause, error) {
	if q == nil {
		return nil, fmt.Errorf("span term query must not be nil")
	}
	if q.term == "" {
		return nil, fmt.Errorf("span term query requires a non-empty term")
	}
	field := q.field
	if field == "" {
		field = defaultField
	}
	return &searcher.SpanClause{
		Field: field,
		Term:  q.term,
	}, nil
}

// Validate returns an error when the query does not describe a legal
// span interval.
func (q *SpanTermQuery) Validate() error {
	if q == nil {
		return fmt.Errorf("span term query must not be nil")
	}
	if q.term == "" {
		return fmt.Errorf("span term query requires a non-empty term")
	}
	return nil
}

// Searcher builds the searcher for this query.
func (q *SpanTermQuery) Searcher(i search.Reader, options search.SearcherOptions) (search.Searcher, error) {
	root, err := q.spanClause(options.DefaultSearchField)
	if err != nil {
		return nil, err
	}
	return searcher.NewSpanSearcher(i, root, q.boost.Value(), q.scorer, options)
}

// SpanNearQuery matches the spans of its nested clauses when they
// occur within one field inside a bounded positional interval.  The
// clauses may themselves be SpanTermQuery or SpanNearQuery instances,
// allowing arbitrary nesting of ordered and unordered subsequences.
//
// When InOrder is true the clauses must occur in declaration order,
// otherwise they may occur in any order.  Slop bounds the total
// number of unmatched token positions between the first and last
// clause; a Slop of zero requires direct adjacency.  Token positions
// may not be reused within a match.
type SpanNearQuery struct {
	clauses []Query
	field   string
	slop    int
	inOrder bool
	boost   *boost
	scorer  search.Scorer
}

// NewSpanNearQuery creates a span near query from the provided nested
// clauses.  At least two span clauses are required and slop must not be
// negative.
func NewSpanNearQuery(clauses []Query, slop int, inOrder bool) *SpanNearQuery {
	return &SpanNearQuery{
		clauses: clauses,
		slop:    slop,
		inOrder: inOrder,
	}
}

// Clauses returns the nested span clauses
func (q *SpanNearQuery) Clauses() []Query {
	return q.clauses
}

// Slop returns the maximum total number of unmatched positions
// allowed between the clauses
func (q *SpanNearQuery) Slop() int {
	return q.slop
}

// SetSlop updates the maximum total number of unmatched positions
// allowed between the clauses
func (q *SpanNearQuery) SetSlop(slop int) *SpanNearQuery {
	q.slop = slop
	return q
}

// InOrder reports whether the clauses must occur in declaration order
func (q *SpanNearQuery) InOrder() bool {
	return q.inOrder
}

// SetInOrder updates whether the clauses must occur in declaration
// order
func (q *SpanNearQuery) SetInOrder(inOrder bool) *SpanNearQuery {
	q.inOrder = inOrder
	return q
}

func (q *SpanNearQuery) SetField(f string) *SpanNearQuery {
	q.field = f
	return q
}

// Field returns the field being queried
func (q *SpanNearQuery) Field() string {
	return q.field
}

func (q *SpanNearQuery) SetBoost(b float64) *SpanNearQuery {
	boostVal := boost(b)
	q.boost = &boostVal
	return q
}

// Boost returns the query boost
func (q *SpanNearQuery) Boost() float64 {
	return q.boost.Value()
}

// resolveField determines the single field shared by every clause,
// preferring an explicit field on the query or any clause, and only
// falling back to the default field when none is configured.
// Conflicting explicit fields are an error.
func (q *SpanNearQuery) resolveField(defaultField string) (string, error) {
	field := q.field
	for i, clause := range q.clauses {
		if clause == nil {
			return "", fmt.Errorf("span near query clause at position %d must not be nil", i)
		}
		if _, ok := clause.(SpanQuery); !ok {
			return "", fmt.Errorf("span near query clause at position %d is %T, not a span query",
				i, clause)
		}
		clauseField := explicitSpanField(clause)
		if clauseField != "" {
			if field == "" {
				field = clauseField
			} else if clauseField != field {
				return "", fmt.Errorf("span near query clauses must all target the same field, "+
					"found %q and %q", field, clauseField)
			}
		}
	}
	if field == "" {
		field = defaultField
	}
	if field == "" {
		return "", fmt.Errorf("span near query requires a field")
	}
	return field, nil
}

func (q *SpanNearQuery) spanClause(defaultField string) (*searcher.SpanClause, error) {
	if q == nil {
		return nil, fmt.Errorf("span near query must not be nil")
	}
	if q.slop < 0 {
		return nil, fmt.Errorf("span near query slop must be greater than or equal to 0")
	}
	if len(q.clauses) < 2 {
		return nil, fmt.Errorf("span near query requires at least two clauses")
	}

	field, err := q.resolveField(defaultField)
	if err != nil {
		return nil, err
	}

	root := &searcher.SpanClause{
		Field:   field,
		Slop:    q.slop,
		InOrder: q.inOrder,
	}
	for _, clause := range q.clauses {
		sq := clause.(SpanQuery) // verified by resolveField
		child, err := sq.spanClause(field)
		if err != nil {
			return nil, err
		}
		if child.Field != field {
			return nil, fmt.Errorf("span near query clauses must all target the same field, "+
				"found %q and %q", field, child.Field)
		}
		root.Clauses = append(root.Clauses, child)
	}

	if err := root.Validate(); err != nil {
		return nil, err
	}
	return root, nil
}

// Validate returns an error when the query does not describe a legal
// span interval.
func (q *SpanNearQuery) Validate() error {
	if q == nil {
		return fmt.Errorf("span near query must not be nil")
	}
	if q.slop < 0 {
		return fmt.Errorf("span near query slop must be greater than or equal to 0")
	}
	if len(q.clauses) < 2 {
		return fmt.Errorf("span near query requires at least two clauses")
	}
	// explicitly configured fields must not conflict, either with
	// each other or with the near query field
	explicitField := q.field
	for i, clause := range q.clauses {
		if clause == nil {
			return fmt.Errorf("span near query clause at position %d must not be nil", i)
		}
		sq, ok := clause.(SpanQuery)
		if !ok {
			return fmt.Errorf("span near query clause at position %d is %T, not a span query",
				i, clause)
		}
		if vq, ok := sq.(validatableQuery); ok {
			if err := vq.Validate(); err != nil {
				return err
			}
		}
		clauseField := explicitSpanField(clause)
		if clauseField != "" {
			if explicitField == "" {
				explicitField = clauseField
			} else if clauseField != explicitField {
				return fmt.Errorf("span near query clauses must all target the same field, "+
					"found %q and %q at clause %d", explicitField, clauseField, i)
			}
		}
	}
	return nil
}

// Searcher builds the searcher for this query.
func (q *SpanNearQuery) Searcher(i search.Reader, options search.SearcherOptions) (search.Searcher, error) {
	root, err := q.spanClause(options.DefaultSearchField)
	if err != nil {
		return nil, err
	}
	return searcher.NewSpanSearcher(i, root, q.boost.Value(), q.scorer, options)
}
