// Package query builds parameterized SQL statements from structured,
// composable query state.
//
// [Query.Build] emits ANSI SQL: "?" placeholders, double-quoted
// identifiers, and OFFSET n ROWS / FETCH FIRST n ROWS ONLY for paging.
// [Query.BuildFor] compiles for a specific [Dialect]: [Postgres] numbers
// placeholders, [MySQL] quotes identifiers with backticks, and [MySQL],
// [SQLite], and [Postgres] page with LIMIT and OFFSET. An OFFSET without a
// LIMIT renders as LIMIT 18446744073709551615 OFFSET n on MySQL, LIMIT -1
// OFFSET n on SQLite, and a bare OFFSET n on Postgres. [SQLite] otherwise
// shares ANSI placeholders and identifier quoting. Use SelectRaw and
// HavingRaw for aggregates and other expressions the structured API cannot
// express.
//
// Table and FromRaw publish immutable [Query] values from ordered callbacks on
// one mutable [Builder]. Settings return the same draft. [Query.Derive] starts
// an independent draft from a base; no callbacks preserves its sticky errors.
// Supplied nil callbacks panic, while expanded nil or empty slices omit edits.
// Publication detaches owned containers and retained root and predicate drafts,
// not arbitrary SQL argument objects. Only published values compile or execute.
// Values become positional bindings; identifiers are validated and quoted.
//
// Example:
//
//	import "arkoren.dev/foundation/query"
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Where("active", true)
//	})
//	statement, err := users.BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
//
// # Raw sources, statements, and conditions
//
// [FromRaw] starts a SELECT from a subquery, rendered as FROM (sql) AS alias
// with the alias set by [Builder.As] ("t" by default). Its arguments bind
// before the outer query's. WhereRaw and OrWhereRaw add verbatim conditions
// like HavingRaw. [Raw] wraps a whole statement for Exec, Select, and Find
// calls the builder cannot express; it satisfies [Statementer] but is opaque.
//
// # Mutation features
//
// [Builder.Returning] appends RETURNING on [Postgres] and [SQLite] and fails
// with [ErrUnsupported] elsewhere. [Builder.OnConflict] followed by DoUpdate
// or DoNothing builds an upsert: ON CONFLICT on Postgres and SQLite, ON
// DUPLICATE KEY UPDATE with VALUES() or INSERT IGNORE on MySQL, and
// [ErrUnsupported] on [ANSI]. [Builder.Increment], [Builder.Decrement], and
// [Builder.SetRaw] add computed assignments that compose with
// [Builder.Update]. [Builder.ForUpdate], [Builder.ForShare],
// [Builder.SkipLocked], and [Builder.NoWait] lock selected rows on [Postgres]
// and [MySQL] 8; SQLite and ANSI fail with [ErrUnsupported].
// PostgreSQL row locks also reject DISTINCT, GROUP BY, and HAVING. SQLite
// rejects qualified written column names in INSERT, UPDATE, and conflict
// clauses with ErrUnsupported; qualified tables and read references remain
// available. Duplicate DoUpdate assignments are invalid on every dialect.
//
// # Introspection and pagination
//
// [Query.Inspect] returns a read-only [Spec] copy of the query's table,
// orders, paging, and shape: its [Kind] (SELECT, INSERT, UPDATE, or DELETE) and
// its [Order] list. [Query.Count] derives the matching COUNT(*)
// query, wrapping grouped, DISTINCT, HAVING, and raw-sourced queries in a
// subquery. [Query.Seek] adds keyset (cursor) pagination filters from the
// query's orders. Both transformations leave the base unchanged.
// Limit and Offset reject negative inputs. Limit(0) requests no rows rather
// than an unbounded result; Offset(0) remains an explicit zero in Inspect.
//
// # Scopes and exists
//
// [Builder.Scope] groups the entire nonempty user filter chain, then ANDs
// each scope as its own group. Neither a later OrWhere nor a disjunction in
// [Builder.WhereRaw] can bypass a scope. Scopes also apply to UPDATE, DELETE,
// and [Query.Count], and satisfy the [Builder.Unfiltered] guard. Each scope
// receives its own [PredicateBuilder]; an empty scope is a no-op, nil panics.
// Ordinary predicate groups share one restricted draft across their callbacks;
// validate the combined result, which must not be empty. Raw text
// must remain trusted, constant SQL with bound values: grouping does not
// validate arbitrary SQL. Without scopes, raw precedence is unchanged.
// [Builder.WhereExists] and its NOT and OR variants take a published SELECT query,
// correlated with [Builder.WhereColumn]. MySQL fails with error 1093 when an
// UPDATE or DELETE subquery reads the target table.
//
// UPDATE and DELETE statements without a WHERE clause are rejected unless
// [Builder.Unfiltered] is called, which explicitly allows them to affect every
// row. INSERT, UPDATE, and DELETE builders that carry clauses they cannot
// render, such as joins, grouping, ordering, limits, or DISTINCT, fail at
// Build instead of silently dropping them.
// An empty WhereIn list is false; an empty WhereNotIn list is true. Neither
// binds arguments. A negative membership with no values therefore does not
// restrict the affected rows, even though it satisfies the WHERE guard.
//
// # Raw expressions and literal safety
//
// SelectRaw and HavingRaw render their expression verbatim and cannot be
// validated: never build one from user input; pass values as arguments. A raw
// expression that accepts arguments, such as HavingRaw, binds them through
// "?" placeholders, which BuildFor rewrites to the dialect placeholder.
// SelectRaw takes no arguments and copies its expression without scanning:
// write a single "?" for a PostgreSQL JSON operator there, not "??".
// Placeholders are counted outside '...', "...",
// and `...` quotes, -- line comments, and /* */ block comments. The number of
// placeholders must equal the number of arguments, and "??" writes a literal
// "?" (for example the PostgreSQL jsonb operator). An empty expression is an
// error. Block comments nest on ANSI and PostgreSQL; MySQL and SQLite end a
// block comment at the first */. PostgreSQL line comments end at CR or LF;
// other dialects use LF. Backslash escapes inside MySQL strings and
// PostgreSQL dollar quoting are not understood, so keep such text out of raw
// expressions or pass it as an argument.
// MySQL recognizes -- as a comment only before whitespace or a control
// character; otherwise the scanner retains the two subtraction/minus tokens.
// Structured IS DISTINCT FROM and IS NOT DISTINCT FROM comparisons fail with
// [ErrUnsupported] on MySQL; ILIKE and NOT ILIKE require PostgreSQL. These
// dialect checks also apply to column comparisons and join conditions.
//
// DDL lives in the subpackage [arkoren.dev/foundation/query/schema], which
// compiles to argument-free statements through [Raw] and reports dialect limits
// with [ErrUnsupported]. [Dialect.TransactionalDDL] tells whether DDL rolls
// back, and [Dialect.Quote] quotes one identifier part for a dialect.
//
// # Structs and named parameters
//
// [Values] turns a struct into a column-to-value map for Insert and Update, and
// [Targets] returns the scan destinations of a result row for a struct, with
// one set of field rules (see [Values]). [RawNamed] writes caller SQL with
// :name placeholders instead of ?, so a map from [Values] can bind it.
// Only and Except retain copied column names. Only() adds no restriction;
// Except() drops nothing. Values retains field objects rather than converting
// them: a nonpointer embedded T with only a pointer Valuer maps its fields,
// whereas an embedded *T implementing Valuer stays one bound pointer value.
//
// Package query only builds statements. Database execution, result scanning,
// and transactions belong to the database contract and its drivers.
package query
