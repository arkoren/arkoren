---
title: Query Builder
description: Build reads and writes, then introduce joins, reusable conditions, and advanced queries.
summary:
  - Build immutable SQL definitions and derive independent query variants.
  - Construct targeted writes from typed inputs and bind values safely.
  - Compose joins, aggregates, and reusable access filters without executing SQL.
order: 10
---

# Query Builder

The query builder describes a SQL statement without opening a connection or
executing it. Pass the published definition to the
[database facade](../database/overview.md) for execution, or compile it for a
native client. Import `arkoren.dev/foundation/query`.

## Selecting Rows

Suppose `users` contains `id`, `email`, and `active`. Select only the fields your
result needs and give bounded reads an explicit order:

```go
users := query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "email")
	statement.Where("active", true)
	statement.OrderBy("id")
	statement.Limit(20)
})
```

`Table` publishes an immutable `query.Query`. Its callback edits a temporary
draft; retaining that draft cannot change the published query. Values such as
`true` become bound arguments. Valid identifiers are quoted for the chosen
dialect, not treated as raw SQL. `Select` replaces the selected columns;
`AddSelect` appends to them. Without a selection, the default is `*`.

`Where("id", 7)` means equality; `Where("id", ">", 7)` supplies an operator.
Use `WhereNull` for SQL NULL, `WhereIn` for membership, and `WhereBetween` for
inclusive bounds. Empty `WhereIn` is false; empty `WhereNotIn` is true. In
particular, an empty exclusion list does not restrict a write.

For an independent variation, derive from the published definition:

```go
recent := users.Derive(func(statement *query.Builder) {
	statement.Where("id", ">", 100)
})
```

`users` remains unchanged. Arbitrary argument objects are still caller-owned;
do not mutate them concurrently with compilation or execution. Limits reject
negatives, and `Limit(0)` requests no rows, not an unbounded result. Let the
[pagination helpers](pagination.md) set limits when they own the result window.

## Targeted Writes

For a new user, expose only the fields this operation accepts:

```go
type NewUser struct {
	Email  string `db:"email"`
	Active bool   `db:"active"`
}
```

With a validated `input` of that type, convert the fields to column values:

```go
insert := query.Table("users", func(statement *query.Builder) {
	statement.Insert(query.Values(input))
})
```

`Values` uses `db` tags, not JSON tags. It includes zero values, so `false` is
an explicit value rather than an omitted patch field. `InsertRows` accepts
several value maps when you need a multirow insert; split very large inputs to
respect database parameter and statement-size limits.

For an email-only update, use a separate writable shape:

```go
type UpdateEmail struct {
	Email string `json:"email" db:"email"`
}
```

After decoding and validating that input, select the target using an authorized
ID outside the body. `Update` needs `query.Values(input)`, not the struct itself:

```go
update := query.Table("users", func(statement *query.Builder) {
	statement.Where("id", userID)
	statement.Update(query.Values(input))
})
```

Use the same explicit filtering approach with `Delete()`. UPDATE and DELETE
without a WHERE fail at compilation unless you deliberately call `Unfiltered()`.
That guard is not authorization: a permissive predicate, including an empty
`WhereNotIn`, can still affect every row. Ordering or limits on writes are
rejected rather than silently discarded.

For an atomic counter change, `Increment("visits", 1)` performs the arithmetic in
SQL instead of reading and writing a stale application value. `SetRaw` permits
trusted computed assignments with bound arguments; never build its expression
from request text.

## Returned Values

PostgreSQL and SQLite can return a newly inserted row without a separate lookup.
For this independent insert, `input` is the validated `NewUser` shape above:

```go
insert := query.Table("users", func(statement *query.Builder) {
	statement.Insert(query.Values(input))
	statement.Returning("id", "email")
})
```

With a configured `db`, operation context `ctx`, and the `User` result from
[Database Overview](../database/overview.md#reading-data), use
`db.Find[User](ctx, insert)` or `db.Select[User](ctx, insert)` to scan returned
rows. `Exec` only returns the affected-row count. `Returning` fails with
`query.ErrUnsupported` on MySQL and ANSI.

For duplicate-safe inserts, `OnConflict("email").DoUpdate("active")` updates
the named columns from the proposed row; `DoNothing()` chooses the no-update
path. PostgreSQL and SQLite use ON CONFLICT. MySQL uses ON DUPLICATE KEY UPDATE
or INSERT IGNORE, whose matching and error behavior are native MySQL semantics,
not an exact conflict-target equivalent. ANSI rejects upserts.

## Joins and Totals

To display a user's team name, assume `users.team_id` references `teams.id`:

```go
members := query.Table("users", func(statement *query.Builder) {
	statement.Select("users.id", "users.email", "teams.name AS team")
	statement.Join("teams", "users.team_id", "=", "teams.id")
})
```

The result type needs fields for the returned column names, including `db:"team"`.
`LeftJoin` keeps unmatched users; represent nullable joined values with nullable
Go fields. Use `WhereColumn` to compare columns rather than binding the second
column name as a value.

For counts per team, define the aggregate expression as trusted SQL:

```go
totals := query.Table("users", func(statement *query.Builder) {
	statement.Select("team_id")
	statement.SelectRaw("COUNT(*) AS total")
	statement.GroupBy("team_id")
	statement.HavingRaw("COUNT(*) > ?", 5)
})
```

`SelectRaw` appends a verbatim expression and accepts no arguments; `HavingRaw`
binds its argument. `totals.Count()` counts the result groups, not their members.
Grouped, DISTINCT, HAVING, and raw-source count queries wrap the original SELECT,
so their selected columns need unique names.

## Reusable Filters

If a tenant condition must constrain every branch of a query, use a scope rather
than relying on the placement of an ordinary `Where` beside OR conditions:

```go
users := query.Table("users", func(statement *query.Builder) {
	statement.Scope(func(predicate *query.PredicateBuilder) {
		predicate.Where("tenant_id", tenantID)
	})
	statement.Where("active", true)
	statement.OrWhere("invited", true)
})
```

The complete user filter is grouped, then ANDed with the scope. A later
`OrWhere` cannot bypass it; scopes also apply to writes and `Count`. Obtain
`tenantID` from your authorization boundary, not an untrusted body field. Empty
scopes do nothing, and raw SQL remains trusted SQL even inside a scope. See
[Row Access](../authorization/row-access.md) for policy integration.

For ordinary parentheses, use `WhereGroup` with a `PredicateBuilder`. For
existence checks, pass a published SELECT to `WhereExists` or `WhereNotExists`,
using `WhereColumn` for correlation. MySQL can reject an UPDATE or DELETE whose
subquery reads the target table with native error 1093.

## Compiling SQL

The facade chooses its driver's dialect automatically. To inspect the SQL or
use a native client, compile deliberately:

```go
compiled, err := users.BuildFor(query.Postgres)
```

`compiled.SQL()` contains placeholders and `compiled.Args()` contains values in
binding order. `Build()` uses ANSI instead. Construction problems are retained
and returned at build time under `query.ErrInvalidQuery`; a later edit cannot
repair an inherited error. Unsupported dialect features also match
`query.ErrUnsupported`. Compilation is not database execution or proof that
tables exist. Use [Raw SQL](raw-sql.md) for statements outside this structured API.

For the complete builder, see the
[`query` API reference](https://pkg.go.dev/arkoren.dev/foundation/query).
