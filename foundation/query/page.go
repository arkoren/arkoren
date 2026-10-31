package query

import (
	"errors"
	"slices"
)

// countAlias names the derived table of a wrapped [Query.Count].
const countAlias = "count"

// countExpression is the select list of a count query.
const countExpression = "COUNT(*)"

// Count returns a SELECT COUNT(*) query for the rows the query selects,
// ignoring ordering, limit, offset, and row locks, so it is the companion of
// a paged query. A plain query keeps its table, joins, and filters and swaps
// the select list for COUNT(*). A grouped, DISTINCT, HAVING, or raw-sourced
// query is wrapped, as in SELECT COUNT(*) FROM (SELECT ...) AS "count", so the
// count matches the rows the original returns; its select list must then have
// unique column names. Only SELECT queries can be counted.
//
// Example:
//
//	users := query.Table("users")
//	total := users.Count()
//	_ = total
func (query Query) Count() Query {
	if query.err != nil {
		return query
	}

	if query.kind != KindSelect {
		return query.withError(errors.New("count requires a select"))
	}

	query.orders = nil
	query.limit = nil
	query.offset = nil
	query.lock = lockNone
	query.skipLocked = false
	query.noWait = false

	total, err := rawIdentifier(countExpression)

	if err != nil {
		return query.withError(err)
	}

	if len(query.groups) > 0 || len(query.havings) > 0 || query.distinct || query.source != nil {
		// countAlias is a fixed, valid identifier, not caller-provided input.
		alias, _ := parseIdentifier(countAlias, false)

		return Query{
			queryState: queryState{
				columns: []identifier{total},
				source: &source{
					inner: &query,
				},
				alias: alias,
			},
		}
	}

	query.columns = []identifier{total}

	return query
}

// Seek adds a keyset pagination filter for the rows after (or, with
// backward, before) the row whose ordering values are values. It reads the
// query's orders, so call it after OrderBy and friends, and pass one value
// per ordering in the same sequence. For orders (a ASC, b DESC) it appends
//
//	WHERE (a > ? OR (a = ? AND b < ?))
//
// to the existing filters, which are parenthesized when they contain OR.
// Backward reverses every ordering direction and comparison, returning rows
// in reverse order for paging toward the start; reverse the fetched rows to
// restore display order. Call Seek once per query. Like a scope, the filter
// is ANDed after the existing filters even when they contain OR. Values must
// not be nil, because NULL never compares; order by a non-null unique column
// last so rows have a total order. Only SELECT accepts it.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrderBy("id")
//	})
//	next := users.Seek([]any{7}, false)
//	_ = next
func (query Query) Seek(values []any, backward bool) Query {
	if query.err != nil {
		return query
	}

	if query.kind != KindSelect {
		return query.withError(errors.New("seek requires a select"))
	}

	if len(query.orders) == 0 {
		return query.withError(errors.New("seek requires at least one order"))
	}

	if len(values) != len(query.orders) {
		return query.withError(errors.New("seek requires one value per order"))
	}

	if slices.ContainsFunc(values, isNil) {
		return query.withError(errors.New("seek values cannot be nil"))
	}

	if backward {
		query.orders = reverseOrders(query.orders)
	}

	query.scopes = append(slices.Clone(query.scopes), seekPredicate(query.orders, values))

	return query
}

// reverseOrders returns a copy of orders with every direction flipped.
func reverseOrders(orders []order) []order {
	reversed := slices.Clone(orders)

	for index := range reversed {
		reversed[index].direction = oppositeDirection(reversed[index].direction)
	}

	return reversed
}

// oppositeDirection flips ASC and DESC.
func oppositeDirection(direction string) string {
	if direction == "DESC" {
		return "ASC"
	}

	return "DESC"
}

// seekPredicate builds the expanded keyset comparison for orders, already
// in their final direction: one OR branch per ordering, each holding equality
// on the earlier columns and a strict comparison on its own.
func seekPredicate(orders []order, values []any) predicate {
	branches := make([]predicate, 0, len(orders))

	for index, ordering := range orders {
		operator := ">"

		if ordering.direction == "DESC" {
			operator = "<"
		}

		strict := predicate{
			kind:     predicateComparison,
			boolean:  booleanAnd,
			column:   ordering.column,
			operator: operator,
			value:    values[index],
		}

		if index == 0 {
			branches = append(branches, strict)

			continue
		}

		members := make([]predicate, 0, index+1)

		for prior := range index {
			members = append(members, predicate{
				kind:     predicateComparison,
				boolean:  booleanAnd,
				column:   orders[prior].column,
				operator: "=",
				value:    values[prior],
			})
		}

		members = append(members, strict)

		branches = append(branches, predicate{
			kind:       predicateGroup,
			boolean:    booleanOr,
			predicates: members,
		})
	}

	return predicate{
		kind:       predicateGroup,
		boolean:    booleanAnd,
		predicates: branches,
	}
}
