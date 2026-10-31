package search

import (
	"errors"
	"fmt"
	"slices"

	"arkoren.dev/core/service/search/internal/scalar"
)

// Operator is a comparison in a [Condition]. The empty value is invalid for a
// comparison and appears only on group nodes. Where accepts the first six
// constants or their string spellings; membership uses WhereIn or WhereNotIn.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.Where("price", search.OpLessOrEqual, 100)
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
type Operator string

// The operators of [Condition]. The first six are the ones [QueryBuilder.Where]
// accepts as its optional second argument.
const (
	// OpEqual is "=": the field equals the value.
	//
	// Example:
	//
	//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
	//		query.Where("category", search.OpEqual, "shoes")
	//	})
	//	if err := query.Err(); err != nil {
	//		return err
	//	}
	OpEqual Operator = "="

	// OpNotEqual is "!=": the field has a value other than the given one.
	// Missing, null, or differently typed fields do not match.
	//
	// Example:
	//
	//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
	//		query.Where("category", search.OpNotEqual, "shoes")
	//	})
	//	if err := query.Err(); err != nil {
	//		return err
	//	}
	OpNotEqual Operator = "!="

	// OpLess is "<": the field precedes the operand in its scalar ordering.
	// Booleans cannot use ordered comparisons.
	//
	// Example:
	//
	//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
	//		query.Where("price", search.OpLess, 50)
	//	})
	//	if err := query.Err(); err != nil {
	//		return err
	//	}
	OpLess Operator = "<"

	// OpLessOrEqual is "<=": equality is included in the upper bound.
	// It follows the same type restrictions as OpLess.
	//
	// Example:
	//
	//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
	//		query.Where("price", search.OpLessOrEqual, 50)
	//	})
	//	if err := query.Err(); err != nil {
	//		return err
	//	}
	OpLessOrEqual Operator = "<="

	// OpGreater is ">": the field follows the operand in its scalar ordering.
	// It follows the same type restrictions as OpLess.
	//
	// Example:
	//
	//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
	//		query.Where("price", search.OpGreater, 10)
	//	})
	//	if err := query.Err(); err != nil {
	//		return err
	//	}
	OpGreater Operator = ">"

	// OpGreaterOrEqual is ">=": equality is included in the lower bound.
	// It follows the same type restrictions as OpLess.
	//
	// Example:
	//
	//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
	//		query.Where("price", search.OpGreaterOrEqual, 10)
	//	})
	//	if err := query.Err(); err != nil {
	//		return err
	//	}
	OpGreaterOrEqual Operator = ">="

	// OpIn means the field equals one of [Condition.Values].
	// Use WhereIn to normalize and capture a nonempty operand list.
	//
	// Example:
	//
	//	filter := search.Condition{Field: "category", Operator: search.OpIn, Values: []any{"shoes"}}
	//	fmt.Println(filter.Operator)
	OpIn Operator = "in"

	// OpNotIn means the field has a value that equals none of
	// [Condition.Values].
	// Use WhereNotIn rather than supplying this constant to Where.
	//
	// Example:
	//
	//	filter := search.Condition{Field: "category", Operator: search.OpNotIn, Values: []any{"shoes"}}
	//	fmt.Println(filter.Operator)
	OpNotIn Operator = "not in"
)

// whereOperators maps the operator text accepted by [QueryBuilder.Where] to its
// operator.
var whereOperators = map[string]Operator{
	"=": OpEqual, "!=": OpNotEqual, "<": OpLess, "<=": OpLessOrEqual, ">": OpGreater, ">=": OpGreaterOrEqual,
}

// Condition is one node of a query's filter tree, as drivers see it. A
// condition is either a comparison of Field or, when Group is not empty, a
// parenthesized group of conditions.
//
// Conditions form a sequence. Each one after the first is joined to the one
// before it with AND, or with OR when Or is set, and AND binds tighter than
// OR, as in SQL: a AND b OR c means (a AND b) OR c. A document without the
// field, or with a null or differently typed value, matches no comparison on
// that field, "!=" and "not in" included.
// Inspect returns independent group and membership containers. Raw driver
// records are mutable and must obey these shapes when constructed directly.
//
// Example:
//
//	filter := search.Condition{Field: "category", Operator: search.OpEqual, Value: "shoes"}
//	spec := search.Spec{Where: []search.Condition{filter}}
//	_ = spec.Where
type Condition struct {
	// Or joins the condition to the previous one with OR instead of AND. It
	// has no meaning on the first condition of a sequence.
	//
	// Example: filter := search.Condition{Or: true, Field: "price", Operator: search.OpLess, Value: float64(10)}
	Or bool

	// Field is the filtered field. Empty for a group.
	// The field must appear in the corresponding Schema.Filterable list.
	//
	// Example: filter := search.Condition{Field: "category", Operator: search.OpEqual, Value: "shoes"}
	Field string

	// Operator is the comparison. Empty for a group.
	// Membership reads Values; ordinary comparisons read Value instead.
	//
	// Example: filter := search.Condition{Field: "price", Operator: search.OpGreater, Value: float64(10)}
	Operator Operator

	// Value is the operand of every operator except [OpIn] and [OpNotIn]:
	// a string, a float64, or a bool. Time values arrive as RFC 3339 UTC
	// strings. String comparison is case-insensitive.
	//
	// Example: filter := search.Condition{Field: "price", Operator: search.OpLess, Value: float64(50)}
	Value any

	// Values holds the operands of [OpIn] and [OpNotIn], each a string, a
	// float64, or a bool.
	// Published queries normalize operands and copy this outer list.
	//
	// Example: filter := search.Condition{Field: "category", Operator: search.OpIn, Values: []any{"shoes", "boots"}}
	Values []any

	// Group holds the nested sequence of a group. A group is never empty.
	// Group nodes leave Field, Operator, Value, and Values unused.
	//
	// Example:
	//
	//	filter := search.Condition{Group: []search.Condition{
	//		{Field: "category", Operator: search.OpEqual, Value: "shoes"},
	//	}}
	//	_ = filter.Group
	Group []Condition
}

// Order is one sort key of a query. Keys are applied in slice order before
// relevance and the final ID tiebreaker described by Driver.Search.
//
// Example:
//
//	spec := search.Spec{Orders: []search.Order{{Field: "price", Desc: true}}}
//	_ = spec.Orders
type Order struct {
	// Field is the sortable field.
	// It must appear in the corresponding Schema.Sortable list.
	//
	// Example: order := search.Order{Field: "price"}
	Field string

	// Desc sorts from the highest value to the lowest. Documents without the
	// field sort last in both directions.
	//
	// Example: order := search.Order{Field: "price", Desc: true}
	Desc bool
}

// Spec is the driver's view of a [Query]: what to match and in which order.
// [Query.Inspect] builds it; the index arrives separately as a [Schema].
// The zero record matches every document with the default relevance/ID order.
// Driver callers must supply validated, normalized field operands themselves.
//
// Example:
//
//	spec := search.NewQuery(Products, "red shoes").Inspect()
//	fmt.Println(len(spec.Text)) // 2
type Spec struct {
	// Text holds the parsed query text. No terms matches every document.
	// Driver implementations consume these terms, not the original query syntax.
	//
	// Example: terms := search.NewQuery(Products, "red shoes").Inspect().Text
	Text []Term

	// Where holds the filter sequence. Empty matches every document.
	// AND has higher precedence than OR; Group establishes explicit boundaries.
	//
	// Example:
	//
	//	spec := search.Spec{Where: []search.Condition{
	//		{Field: "category", Operator: search.OpEqual, Value: "shoes"},
	//	}}
	//	_ = spec.Where
	Where []Condition

	// Orders lists the sort keys in priority order. See [Driver.Search] for
	// how results with equal keys are ordered.
	//
	// Example: spec := search.Spec{Orders: []search.Order{{Field: "price"}}}
	Orders []Order
}

// Query is an immutable search over one [Index]: text, filters, and orders.
// Create it with [NewQuery], derive local variants with [Query.Derive], and run
// it with [Engine.Search]. Conditions require filterable fields and orders
// require sortable fields. The first domain error is kept by [Query.Err], and
// Search returns it before calling the driver. Owned containers are detached
// at publication; arbitrary caller values and closure state are not deep-cloned.
// Its zero value is invalid; Err and Derive preserve the missing-index cause.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.Where("category", "shoes")
//		query.OrderBy("price")
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
type Query[T any] struct {
	// index is the index the query runs against.
	index Index[T]

	// spec is the accumulated query.
	spec Spec

	// err is the first error found while building the query.
	err error
}

// NewQuery publishes a query against index for text. Words are all required;
// "double quotes" make a phrase; a leading minus excludes a word or phrase;
// the last plain word matches as a prefix. Other text is an ordinary word,
// so parsing never fails and empty text matches every document. An invalid
// index stores [ErrInvalidQuery] wrapping the index error.
//
// Callbacks run directly once, synchronously in order on one fresh draft,
// including when index is invalid. Nil entries panic; expanded nil/empty
// slices omit edits. Publication detaches owned conditions, groups, value
// lists, text, and orders. Arbitrary caller objects are not recursively cloned.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.Where("category", "shoes")
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func NewQuery[T any](index Index[T], text string, configure ...QueryBuilderFunc) Query[T] {
	query := Query[T]{
		index: index,
		spec:  Spec{Text: parseText(text)},
	}

	if err := index.Err(); err != nil {
		query.err = fmt.Errorf("search query: %w: %w", ErrInvalidQuery, err)
	}

	return query.Derive(configure...)
}

// Derive publishes an independent query after callbacks edit a copy of the
// base. Text, conditions, orders, and the first error are preserved without
// reapplying defaults. Callbacks run directly once, synchronously in order on
// one detached draft, including for invalid bases. Nil entries panic; expanded
// nil/empty slices omit edits. With no callbacks the result has the same meaning
// as the base. Retained drafts cannot change either published value.
//
// Example:
//
//	cheap := search.NewQuery(Products, "red").Derive(func(query *search.QueryBuilder) {
//		query.Where("price", "<", 50)
//	})
//	if err := cheap.Err(); err != nil {
//		return err
//	}
func (query Query[T]) Derive(configure ...QueryBuilderFunc) Query[T] {
	draft := &QueryBuilder{
		index: query.index.Schema(),
		spec:  query.Inspect(),
		err:   query.Err(),
	}

	for _, configure := range configure {
		configure(draft)
	}

	query.spec = cloneSpec(draft.spec)

	query.err = draft.err

	return query
}

// QueryBuilder is the mutable draft used by [NewQuery] and [Query.Derive].
// Conditions and orders append; settings return this same draft. The first
// domain error is preserved. Only published queries can be inspected or run.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.Where("category", "shoes")
//		query.OrderBy("price")
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
type QueryBuilder struct {
	// index is the detached schema used to validate fields.
	index Schema

	// spec holds normalized text, conditions, and orders until publication.
	spec Spec

	// err is the first domain error, including inherited query errors.
	err error
}

// PredicateBuilder restricts groups to predicates, without ordering or
// execution methods. Group callbacks share one draft; only the final combined
// expression must be nonempty. Retained drafts cannot change published groups.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereGroup(func(predicate *search.PredicateBuilder) {
//			predicate.Where("category", "shoes")
//		})
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
type PredicateBuilder struct {
	// query owns conditions and validation without exposing root-only methods.
	query QueryBuilder
}

// Where appends an AND comparison and returns this same draft. Use
// Where(field, value) for equality or Where(field, operator, value) with
// "=", "!=", "<", "<=", ">", or ">=", supplied as a string or [Operator].
// Values accept named or unnamed strings,
// booleans, integers, floats, and exact [time.Time]. Booleans support only
// "=" and "!=". Undeclared fields, unsupported or nonfinite values, and unknown
// operators store [ErrInvalidQuery], without replacing an earlier error.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.Where("category", "shoes")
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (query *QueryBuilder) Where(field string, args ...any) *QueryBuilder {
	return query.compare(false, field, args)
}

// OrWhere appends a comparison joined by OR and returns this same draft.
// It follows [QueryBuilder.Where] validation. AND binds tighter than OR:
// Where(a).Where(b).OrWhere(c) means (a AND b) OR c.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.Where("category", "shoes").OrWhere("price", "<", 10)
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (query *QueryBuilder) OrWhere(field string, args ...any) *QueryBuilder {
	return query.compare(true, field, args)
}

// WhereIn appends an AND condition requiring the field to equal one of
// values and returns this same draft. Typed values follow [QueryBuilder.Where]
// normalization and are copied into an owned list. Empty input, including an
// expanded nil/empty slice, stores [ErrInvalidQuery]; the first error is kept.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereIn("category", "boots", "socks")
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (query *QueryBuilder) WhereIn[V any](field string, values ...V) *QueryBuilder {
	return query.membership(field, OpIn, anyValues(values))
}

// WhereNotIn appends an AND condition requiring the field to equal none
// of values and returns this same draft. Input copying, normalization, empty
// list validation, and sticky errors follow [QueryBuilder.WhereIn].
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereNotIn("category", "boots")
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (query *QueryBuilder) WhereNotIn[V any](field string, values ...V) *QueryBuilder {
	return query.membership(field, OpNotIn, anyValues(values))
}

// WhereGroup appends a parenthesized AND expression and returns this draft.
// Callbacks run directly once, synchronously in order on one restricted draft,
// even after a parent error. Nil entries panic; zero callbacks or a final
// empty expression store [ErrInvalidQuery]. Expanded nil/empty slices omit
// callbacks. Nested groups and value lists are copied before retaining them;
// a retained predicate cannot change this group. The first query error is kept.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereGroup(func(predicate *search.PredicateBuilder) {
//			predicate.Where("category", "shoes")
//			predicate.OrWhere("price", "<", 10)
//		})
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (query *QueryBuilder) WhereGroup(configure ...PredicateBuilderFunc) *QueryBuilder {
	predicate := &PredicateBuilder{query: QueryBuilder{index: query.index}}

	for _, configure := range configure {
		configure(predicate)
	}

	if query.err != nil {
		return query
	}

	if predicate.query.err != nil {
		return query.fail(predicate.query.err)
	}

	if len(predicate.query.spec.Where) == 0 {
		return query.fail(invalidQuery(query.index.Name, "empty group"))
	}

	query.spec.Where = append(query.spec.Where, Condition{Group: cloneConditions(predicate.query.spec.Where)})

	return query
}

// OrderBy appends an ascending sort key after existing orders and returns
// this same draft. The field must be declared sortable on the index; otherwise
// it stores [ErrInvalidQuery] without replacing an earlier error.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.OrderBy("price")
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (query *QueryBuilder) OrderBy(field string) *QueryBuilder {
	return query.order(field, false)
}

// OrderByDesc appends a descending sort key after existing orders and
// returns this same draft. Validation and sticky errors follow
// [QueryBuilder.OrderBy]; missing values sort last in either direction.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.OrderByDesc("price")
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (query *QueryBuilder) OrderByDesc(field string) *QueryBuilder {
	return query.order(field, true)
}

// Where appends a predicate and returns this same restricted draft.
// It follows [QueryBuilder.Where] validation and preserves the first error.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereGroup(func(predicate *search.PredicateBuilder) {
//			predicate.Where("category", "shoes")
//		})
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (predicate *PredicateBuilder) Where(field string, args ...any) *PredicateBuilder {
	predicate.query.Where(field, args...)

	return predicate
}

// OrWhere appends a predicate and returns this same restricted draft.
// It follows [QueryBuilder.OrWhere] validation and preserves the first error.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereGroup(func(predicate *search.PredicateBuilder) {
//			predicate.Where("category", "shoes").OrWhere("price", "<", 10)
//		})
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (predicate *PredicateBuilder) OrWhere(field string, args ...any) *PredicateBuilder {
	predicate.query.OrWhere(field, args...)

	return predicate
}

// WhereIn appends a predicate and returns this same restricted draft.
// It follows [QueryBuilder.WhereIn] validation and preserves the first error.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereGroup(func(predicate *search.PredicateBuilder) {
//			predicate.WhereIn("category", "boots", "socks")
//		})
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (predicate *PredicateBuilder) WhereIn[V any](field string, values ...V) *PredicateBuilder {
	predicate.query.WhereIn(field, values...)

	return predicate
}

// WhereNotIn appends a predicate and returns this same restricted draft.
// It follows [QueryBuilder.WhereNotIn] validation and preserves the first error.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereGroup(func(predicate *search.PredicateBuilder) {
//			predicate.WhereNotIn("category", "boots")
//		})
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (predicate *PredicateBuilder) WhereNotIn[V any](field string, values ...V) *PredicateBuilder {
	predicate.query.WhereNotIn(field, values...)

	return predicate
}

// WhereGroup appends a predicate and returns this same restricted draft.
// It follows [QueryBuilder.WhereGroup] validation and preserves the first error.
//
// Example:
//
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereGroup(func(predicate *search.PredicateBuilder) {
//			predicate.WhereGroup(func(predicate *search.PredicateBuilder) {
//				predicate.Where("category", "shoes")
//				predicate.OrWhere("price", "<", 10)
//			})
//		})
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
func (predicate *PredicateBuilder) WhereGroup(configure ...PredicateBuilderFunc) *PredicateBuilder {
	predicate.query.WhereGroup(configure...)

	return predicate
}

// Err returns the first problem found while building the query, or nil. A
// non-nil error wraps [ErrInvalidQuery]. The zero value is invalid and also
// preserves the underlying [ErrInvalidName] from its missing index.
//
// Example:
//
//	query := search.NewQuery(Products, "red")
//	if err := query.Err(); err != nil {
//		return err
//	}
func (query Query[T]) Err() error {
	if query.err != nil {
		return query.err
	}

	if query.index.key == nil {
		return fmt.Errorf("search query: %w: %w", ErrInvalidQuery, query.index.Err())
	}

	return nil
}

// Inspect returns the query as a [Spec] for drivers. The result owns independent
// text, condition, nested group, membership-list, and order containers. Changing
// the returned spec cannot change the published query or any derived query.
//
// Example:
//
//	query := search.NewQuery(Products, "red", func(query *search.QueryBuilder) {
//		query.OrderBy("price")
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
//	spec := query.Inspect()
//	_ = spec.Orders
func (query Query[T]) Inspect() Spec {
	return cloneSpec(query.spec)
}

// cloneSpec detaches the known owned containers. Normalized scalar values need
// no recursive cloning; arbitrary caller objects are not retained here.
func cloneSpec(spec Spec) Spec {
	return Spec{
		Text:   slices.Clone(spec.Text),
		Where:  cloneConditions(spec.Where),
		Orders: slices.Clone(spec.Orders),
	}
}

// compare appends a comparison condition built from the arguments of Where.
func (query *QueryBuilder) compare(or bool, field string, args []any) *QueryBuilder {
	if query.err != nil {
		return query
	}

	operator, operand, err := comparisonArguments(args)

	if err != nil {
		return query.fail(invalidQuery(query.index.Name, "where %q: %v", field, err))
	}

	if !slices.Contains(query.index.Filterable, field) {
		return query.fail(invalidQuery(query.index.Name, "field %q is not filterable", field))
	}

	value, err := scalar.Value(operand)

	if err != nil {
		return query.fail(invalidQuery(query.index.Name, "field %q: %v", field, err))
	}

	_, boolean := value.(bool)
	ordered := operator != OpEqual && operator != OpNotEqual

	if boolean && ordered {
		return query.fail(invalidQuery(query.index.Name, "field %q: booleans support only = and !=", field))
	}

	query.spec.Where = append(query.spec.Where, Condition{
		Or:       or,
		Field:    field,
		Operator: operator,
		Value:    value,
	})

	return query
}

// comparisonArguments interprets the equality shorthand or explicit comparison
// form before scalar normalization. Only string and Operator spellings are
// accepted; membership operators still require WhereIn or WhereNotIn.
func comparisonArguments(args []any) (Operator, any, error) {
	if len(args) == 1 {
		return OpEqual, args[0], nil
	}

	if len(args) != 2 {
		return "", nil, errors.New("want a value or an operator and a value")
	}

	var operator Operator

	switch value := args[0].(type) {
	case string:
		operator = whereOperators[value]
	case Operator:
		operator = whereOperators[string(value)]
	}

	if operator == "" {
		return "", nil, fmt.Errorf("unknown operator %v", args[0])
	}

	return operator, args[1], nil
}

// membership appends an AND in or not-in condition over values.
func (query *QueryBuilder) membership(field string, operator Operator, values []any) *QueryBuilder {
	if query.err != nil {
		return query
	}

	if !slices.Contains(query.index.Filterable, field) {
		return query.fail(invalidQuery(query.index.Name, "field %q is not filterable", field))
	}

	if len(values) == 0 {
		return query.fail(invalidQuery(query.index.Name, "field %q: empty value list", field))
	}

	normalized := make([]any, len(values))

	for i, value := range values {
		var err error

		if normalized[i], err = scalar.Value(value); err != nil {
			return query.fail(invalidQuery(query.index.Name, "field %q: %v", field, err))
		}
	}

	query.spec.Where = append(query.spec.Where, Condition{
		Field:    field,
		Operator: operator,
		Values:   normalized,
	})

	return query
}

// order appends a sort key.
func (query *QueryBuilder) order(field string, desc bool) *QueryBuilder {
	if query.err != nil {
		return query
	}

	if !slices.Contains(query.index.Sortable, field) {
		return query.fail(invalidQuery(query.index.Name, "field %q is not sortable", field))
	}

	query.spec.Orders = append(query.spec.Orders, Order{Field: field, Desc: desc})

	return query
}

// fail records err as the first error of the query.
func (query *QueryBuilder) fail(err error) *QueryBuilder {
	query.err = err

	return query
}

// invalidQuery returns an error that wraps [ErrInvalidQuery] and names the
// index. It is a function rather than a method because go vet's printf
// analysis mixes up the facts of methods on generic types, and would then
// report calls of other methods as printf wrappers.
func invalidQuery(index, format string, args ...any) error {
	return fmt.Errorf("search query on %s: %w: %s", index, ErrInvalidQuery, fmt.Sprintf(format, args...))
}

// anyValues converts a typed slice to the []any that normalizeValue takes.
func anyValues[V any](values []V) []any {
	converted := make([]any, len(values))

	for i, value := range values {
		converted[i] = value
	}

	return converted
}

// cloneConditions copies a condition sequence, including nested groups and
// value lists.
func cloneConditions(conditions []Condition) []Condition {
	cloned := slices.Clone(conditions)

	for i := range cloned {
		cloned[i].Values = slices.Clone(cloned[i].Values)
		cloned[i].Group = cloneConditions(cloned[i].Group)
	}

	return cloned
}

// QueryBuilderFunc edits one detached query draft synchronously. NewQuery and
// Derive preserve the first error while still invoking each callback in order.
// Nil entries panic; expanding a nil or empty callback slice adds no edits.
//
// Example:
//
//	var cheap search.QueryBuilderFunc = func(query *search.QueryBuilder) {
//		query.Where("price", search.OpLess, 50)
//	}
//	query := search.NewQuery(Products, "red", cheap)
//	if err := query.Err(); err != nil {
//		return err
//	}
type QueryBuilderFunc = func(*QueryBuilder)

// PredicateBuilderFunc appends conditions to a restricted group draft. All
// callbacks in one WhereGroup share that draft; their final expression must be
// nonempty. Nil entries panic, and retained drafts cannot change publication.
//
// Example:
//
//	var shoes search.PredicateBuilderFunc = func(predicate *search.PredicateBuilder) {
//		predicate.Where("category", "shoes")
//	}
//	query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
//		query.WhereGroup(shoes)
//	})
//	if err := query.Err(); err != nil {
//		return err
//	}
type PredicateBuilderFunc = func(*PredicateBuilder)
