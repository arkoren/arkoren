package query

import "strings"

// Order is one ordering of a [Spec].
// Inspect returns orders in declaration order, with owned slice storage.
// Editing an Order changes only the inspection result, not its query.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrderByDesc("users.id")
//	})
//	ordering := users.Inspect().Orders[0]
//	_ = ordering.Column // "users.id"
//	_ = ordering.Desc   // true
type Order struct {
	// Column is the ordered column, qualified with dots when it was.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.OrderBy("users.id")
	//	})
	//	_ = users.Inspect().Orders[0].Column // "users.id"
	Column string

	// Desc reports whether the ordering is descending.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.OrderByDesc("id")
	//	})
	//	_ = users.Inspect().Orders[0].Desc // true
	Desc bool
}

// Spec is a read-only snapshot of a [Query], for callers such as
// pagination helpers that need to know what a query selects without
// compiling it. Changing a Spec never affects the query it came from.
// Inspection reports declarations even if later compilation rejects them;
// it does not validate the query or resolve a database schema.
//
// Example:
//
//	spec := query.Table("users", func(statement *query.Builder) {
//		statement.OrderBy("id")
//		statement.Limit(20)
//	}).Inspect()
//	_ = spec.Table
//	_ = spec.Orders
type Spec struct {
	// Kind is the statement the query compiles to.
	//
	// Example:
	//
	//	_ = query.Table("users").Inspect().Kind == query.KindSelect
	Kind Kind

	// Table is the source table as written, such as "public.users". It is
	// empty when the source is raw SQL.
	//
	// Example:
	//
	//	_ = query.Table("public.users").Inspect().Table // "public.users"
	Table string

	// Orders lists the orderings in the order they apply; nil when none.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.OrderBy("id")
	//	})
	//	_ = users.Inspect().Orders[0].Column
	Orders []Order

	// Limit is a copy of the row limit; nil when unset.
	// Explicit zero requests no rows and differs from an unset limit.
	//
	// Example:
	//
	//	spec := query.Table("users", func(statement *query.Builder) {
	//		statement.Limit(20)
	//	}).Inspect()
	//	if spec.Limit != nil {
	//		_ = *spec.Limit // 20
	//	}
	Limit *int

	// Offset is a copy of the row offset; nil when unset.
	// Explicit zero remains present even though it skips no rows.
	//
	// Example:
	//
	//	spec := query.Table("users", func(statement *query.Builder) {
	//		statement.Offset(0)
	//	}).Inspect()
	//	if spec.Offset != nil {
	//		_ = *spec.Offset // 0
	//	}
	Offset *int

	// Distinct reports whether the select is DISTINCT.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.Distinct()
	//	})
	//	_ = users.Inspect().Distinct // true
	Distinct bool

	// Grouped reports whether the query has GROUP BY columns.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.Select("role")
	//		statement.GroupBy("role")
	//	})
	//	_ = users.Inspect().Grouped // true
	Grouped bool

	// Having reports whether the query has HAVING filters.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.SelectRaw("COUNT(*)")
	//		statement.HavingRaw("COUNT(*) > ?", 5)
	//	})
	//	_ = users.Inspect().Having // true
	Having bool

	// Raw reports whether the source is a [FromRaw] subquery.
	// Query-sourced wrappers created by Count do not set this flag.
	//
	// Example:
	//
	//	users := query.FromRaw(query.Raw("SELECT id FROM users"))
	//	_ = users.Inspect().Raw // true
	Raw bool

	// Returning reports whether an INSERT, UPDATE, or DELETE has a
	// RETURNING clause and so yields rows.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.Insert(map[string]any{"name": "Ada"})
	//		statement.Returning("id")
	//	})
	//	_ = users.Inspect().Returning // true; compile for PostgreSQL or SQLite.
	Returning bool
}

// Inspect returns a snapshot of the query's structure. Slices and pointers
// in the result are copies, so the query stays immutable.
//
// Example:
//
//	users := query.Table("users")
//	_ = users.Inspect().Table
func (query Query) Inspect() Spec {
	spec := Spec{
		Kind:     query.kind,
		Table:    strings.Join(query.table.parts, "."),
		Limit:    copyInt(query.limit),
		Offset:   copyInt(query.offset),
		Distinct: query.distinct,
		Grouped:  len(query.groups) > 0,
		Having:   len(query.havings) > 0,
		Raw:      query.source != nil && query.source.inner == nil,

		Returning: len(query.returning) > 0,
	}

	for _, ordering := range query.orders {
		spec.Orders = append(spec.Orders, Order{
			Column: strings.Join(ordering.column.parts, "."),
			Desc:   ordering.direction == "DESC",
		})
	}

	return spec
}

// copyInt returns a pointer to a copy of value, or nil.
func copyInt(value *int) *int {
	if value == nil {
		return nil
	}

	copied := *value

	return &copied
}
