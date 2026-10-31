package query

import (
	"maps"
	"slices"
)

// Query is an immutable SQL definition, compiled lazily for a chosen dialect.
// Construct it with Table or FromRaw and use Derive for independent variants.
// Its owned containers are detached from construction drafts. Arbitrary argument
// objects remain caller-owned: do not mutate them while compiling or executing.
//
// Example:
//
//	users := query.Table("users")
//	statement, err := users.BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
type Query struct {
	// queryState is the detached representation; no published method mutates it.
	queryState
}

// Derive publishes an independent variant after applying configure to one draft.
// Callbacks run directly once, synchronously and in order; nil callbacks panic.
// No callbacks preserves the base, including its first error. Settings cannot
// repair inherited errors. Retained drafts cannot change either published value.
//
// Example:
//
//	base := query.Table("users")
//	active := base.Derive(func(statement *query.Builder) {
//		statement.Where("active", true)
//	})
//	_ = active
func (query Query) Derive(configure ...BuilderFunc) Query {
	draft := &Builder{
		queryState: query.snapshot(),
	}

	for _, configure := range configure {
		configure(draft)
	}

	if query.err != nil {
		draft.err = query.err
	}

	return Query{
		queryState: draft.snapshot(),
	}
}

// withError preserves the first error in a transformed published value.
// Count and Seek change only their local copy; the original query stays valid.
func (query Query) withError(err error) Query {
	if query.err == nil {
		query.err = err
	}

	return query
}

// snapshot detaches all mutable SQL containers at the publication boundary.
// Identifier parts and published subqueries are private immutable values; SQL
// arguments are intentionally not recursively copied, matching Raw and Values.
func (query queryState) snapshot() queryState {
	query.columns = slices.Clone(query.columns)
	query.joins = slices.Clone(query.joins)
	query.predicates = snapshotPredicates(query.predicates)
	query.groups = slices.Clone(query.groups)
	query.havings = snapshotPredicates(query.havings)
	query.orders = slices.Clone(query.orders)
	query.limit = copyInt(query.limit)
	query.offset = copyInt(query.offset)
	query.rows = slices.Clone(query.rows)

	for index := range query.rows {
		query.rows[index] = slices.Clone(query.rows[index])
	}

	query.updates = slices.Clone(query.updates)
	query.expressions = slices.Clone(query.expressions)

	for index := range query.expressions {
		query.expressions[index].args = slices.Clone(query.expressions[index].args)
	}

	query.returning = slices.Clone(query.returning)
	query.scopes = snapshotPredicates(query.scopes)

	if query.conflict != nil {
		conflict := *query.conflict
		conflict.target = slices.Clone(conflict.target)
		conflict.update = slices.Clone(conflict.update)

		query.conflict = &conflict
	}

	if query.source != nil {
		source := *query.source
		source.raw.args = slices.Clone(source.raw.args)
		source.raw.named = maps.Clone(source.raw.named)

		query.source = &source
	}

	return query
}

// snapshotPredicates recursively detaches predicate groups and argument lists.
// EXISTS references published immutable queries and therefore needs no mutable
// draft clone. Values inside SQL argument slots retain their original ownership.
func snapshotPredicates(predicates []predicate) []predicate {
	predicates = slices.Clone(predicates)

	for index := range predicates {
		predicates[index].values = slices.Clone(predicates[index].values)
		predicates[index].predicates = snapshotPredicates(predicates[index].predicates)
	}

	return predicates
}
