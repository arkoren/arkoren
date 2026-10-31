# Query

[![Go Reference](https://pkg.go.dev/badge/arkoren.dev/foundation/query.svg)](https://pkg.go.dev/arkoren.dev/foundation/query)

When filters and query variants grow, keep SQL conditions and their bound values
together instead of assembling strings. `query` publishes immutable,
parameterized SQL definitions from mutable builder callbacks. It does not
execute queries or scan results. Keep the normal package name and use
`statement` for the mutable draft, so package types stay available:

```go
import "arkoren.dev/foundation/query"
```

`Table(name, configure...)` and `FromRaw(source, configure...)` return `Query`.
Their callbacks use `BuilderFunc = func(*Builder)`; predicate groups use
`PredicateBuilderFunc = func(*PredicateBuilder)`. These aliases also work in
callback slices and return types without conversion.
Callbacks run directly once, synchronously in order on one `*Builder`; each
setting returns that draft. Supplied nil callbacks panic, while expanded nil
or empty slices omit edits. Use `base.Derive(configure...)` for variants; no
callbacks preserves the base and its first error. Publication detaches owned
containers and retained root, predicate, and conflict drafts, not arbitrary SQL
argument objects. Only published queries and raw statements compile or execute.

```go
statement, err := query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "name")
	statement.Where("active", true)
}).Build()
```

`statement.SQL()` returns ANSI SQL with `?` placeholders, and
`statement.Args()` returns the separately bound values. This query selects the
active users' IDs and names. Identifiers use ANSI
double-quote syntax, and paging uses `OFFSET n ROWS` and
`FETCH FIRST n ROWS ONLY`. Use `BuildFor` with a concrete dialect for
`LIMIT`/`OFFSET` databases.

## Dialects

`BuildFor` compiles for a specific database:

```go
users := query.Table("users", func(statement *query.Builder) {
	statement.Where("id", 7)
})

statement, err := users.BuildFor(query.Postgres) // SELECT * FROM "users" WHERE "id" = $1
statement, err = users.BuildFor(query.MySQL)     // SELECT * FROM `users` WHERE `id` = ?
statement, err = users.Build()                 // ANSI, same as BuildFor(query.ANSI)
```

The package provides `ANSI`, `SQLite`, `Postgres`, and `MySQL`. Dialects are
opaque, comparable values, and the zero `Dialect` is ANSI. New dialects are
added to this package, so the compiler can grow dialect-specific syntax
without breaking callers.

| Dialect | Placeholders | Identifiers | Paging |
| --- | --- | --- | --- |
| `ANSI` | `?` | `"x"` | `OFFSET n ROWS`, `FETCH FIRST n ROWS ONLY` |
| `SQLite` | `?` | `"x"` | `LIMIT n OFFSET n`; offset alone adds `LIMIT -1` |
| `Postgres` | `$n` | `"x"` | `LIMIT n OFFSET n`; offset alone is `OFFSET n` |
| `MySQL` | `?` | `` `x` `` | `LIMIT n OFFSET n`; offset alone adds `LIMIT 18446744073709551615` |

`ILIKE` is valid on Postgres only; other dialects return `ErrUnsupported`.
`IS DISTINCT FROM` and `IS NOT DISTINCT FROM` compile on ANSI, Postgres, and
SQLite. MySQL has neither operator, so compilation returns `ErrUnsupported`
instead of emitting invalid SQL. The same checks apply to column comparisons
and join conditions; raw SQL remains your responsibility.

### Raw expressions

Raw expressions that accept arguments, such as `HavingRaw`, use `?` placeholders.
`BuildFor` rewrites each `?` outside quoted text (`'...'`, `"..."`, `` `...` ``)
and outside `--` and `/* */` comments to the dialect placeholder. Block comments
nest on ANSI and Postgres; MySQL and SQLite end them at the first `*/`. The
Postgres scanner ends line comments at CR or LF; other dialects use LF. The
number of `?` placeholders must equal the number of arguments, or the build
fails with `ErrInvalidQuery`. Write `??` for a literal `?`, such as the Postgres jsonb
operator. An empty expression is an error. On MySQL, `--` begins a comment only
if whitespace or a control character follows; otherwise placeholders after
the two minus signs still bind. Raw text is never escaped or
validated: do not build it from user input. Backslash escapes in MySQL strings
and Postgres dollar quoting are not understood.

`SelectRaw` takes no arguments and copies its selection expression verbatim,
without scanning placeholders. Write a single `?` for a Postgres jsonb operator
there, not `??`. Do not put bind parameters in a `SelectRaw` expression.

`WhereRaw` and `OrWhereRaw` use the argument-binding scanner described above.
`query.Raw(sql, args...)` uses that scanner too and
wraps a whole statement for Exec, Select, and Find calls the builder cannot
express; it implements `Statementer` with `Build`/`BuildFor` but is opaque.

### Subquery source

To filter the result of SQL the builder cannot express, wrap it with `FromRaw`:

```go
query.FromRaw(query.Raw("SELECT id, n FROM events WHERE kind = ?", "click"), func(statement *query.Builder) {
	statement.As("e")
	statement.Where("n", ">", 3)
})
// SELECT * FROM (
// SELECT id, n FROM events WHERE kind = ?
// ) AS "e" WHERE "n" > ?
```

The alias defaults to `t`. Subquery arguments bind before the outer ones.
INSERT, UPDATE, and DELETE reject a raw source.

### Mutations

`UPDATE` and `DELETE` without a `WHERE` fail unless `Unfiltered()` is called,
which allows them to affect every row. `INSERT`, `UPDATE`, and `DELETE` also
fail when the builder carries joins, grouping, having, ordering, limit, offset,
or `DISTINCT` (and `INSERT` rejects `WHERE`).

SQLite also rejects qualified column names in INSERT maps, UPDATE assignments,
and conflict targets or assignments with `ErrUnsupported`. Use `name`, not
`users.name`, for those written columns. Qualified table names and read-side
column references remain available.

Empty `WhereIn` lists compile as false; empty `WhereNotIn` lists compile as
true, with no arguments in either case. An empty negative membership does not
limit which rows a mutation affects, even though it supplies a WHERE clause.

`Limit` and `Offset` reject negative inputs. Omitting a limit is unbounded;
`Limit(0)` explicitly requests no rows. `Offset(0)` explicitly skips no rows
and remains present in `Inspect`, unlike an omitted offset.

### Scopes

Tenant or access filters must apply even when a caller adds an OR condition.
Use a scope to keep that mandatory filter outside the caller's disjunction.

`Scope(fn...)` groups the entire nonempty user filter chain, then ANDs each
scope as its own group when the statement compiles. This keeps both structured
ORs and disjunctions in `WhereRaw` inside every scope, including predicates
added after `Scope`. Raw fragments still require trusted, constant SQL text
and separately bound values; grouping does not validate arbitrary SQL.
Without scopes, raw fragments retain their verbatim operator precedence.
They apply to `SELECT`, `UPDATE`, `DELETE`, `Count()`, and `Seek`, and count
as a filter for the `Unfiltered()` guard. An empty scope is a no-op; a supplied nil callback panics. Each scope has
its own `*PredicateBuilder`, which exposes only predicates and nested groups;
`INSERT` rejects scopes. Ordinary groups accept variadic callbacks on one draft
and validate their combined result: an empty ordinary group is invalid.

```go
active := func(predicate *query.PredicateBuilder) {
	predicate.Where("active", true)
}
query.Table("users", func(statement *query.Builder) {
	statement.Where("role", "admin")
	statement.OrWhere("role", "owner")
	statement.Scope(active)
})
// WHERE ("role" = ? OR "role" = ?) AND ("active" = ?)
```

### Exists

`WhereExists`, `WhereNotExists`, `OrWhereExists`, and `OrWhereNotExists` take a
published `SELECT` query, correlated with `WhereColumn`. Placeholders continue across the
subquery.

```go
members := query.Table("members", func(statement *query.Builder) {
	statement.WhereColumn("members.group_id", "posts.group_id")
	statement.Where("members.user_id", 7)
})
query.Table("posts", func(statement *query.Builder) {
	statement.WhereExists(members)
})
```

MySQL fails with error 1093 when an `UPDATE` or `DELETE` subquery reads the
target table.

### Returning, upserts, computed updates

Given your `row` column map, request the inserted ID on Postgres or SQLite:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Insert(row)
	statement.Returning("id")
})
```

On Postgres or SQLite, update the name when a unique `email` constraint conflicts:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Insert(row)
	statement.OnConflict("email").DoUpdate("name")
})
```

To leave an existing row unchanged instead, choose `DoNothing`:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Insert(row)
	statement.OnConflict("email").DoNothing()
})
```

For a view counter, compute the new count and timestamp in the database:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.Where("id", 7)
	statement.Increment("views", 1)
	statement.SetRaw("seen_at", "CURRENT_TIMESTAMP")
})
```

| Feature | Postgres, SQLite | MySQL | ANSI |
| --- | --- | --- | --- |
| `Returning` | `RETURNING cols` | `ErrUnsupported` | `ErrUnsupported` |
| `DoUpdate` | `ON CONFLICT (cols) DO UPDATE SET c = EXCLUDED.c` | `ON DUPLICATE KEY UPDATE c = VALUES(c)` | `ErrUnsupported` |
| `DoNothing` | `ON CONFLICT (cols) DO NOTHING` | `INSERT IGNORE` | `ErrUnsupported` |

Design: `OnConflict(cols...)` returns a parent-attached `*Conflict` draft so the target
and the action read in order. MySQL has no conflict target and ignores the
columns, including an omitted target; `DoUpdate` needs them on Postgres and
SQLite. Its update list must be nonempty and contain no duplicate assignments.
`INSERT IGNORE` also downgrades unrelated errors to warnings, and `VALUES()` is
deprecated since MySQL 8.0.20 but works everywhere. `Increment`, `Decrement`, and
`SetRaw` turn the builder into an UPDATE and compose with `Update(map)`; map
columns render first, sorted, then expressions in call order. Assigning a column
twice fails.

### Row locking

`ForUpdate()`, `ForShare()`, `SkipLocked()`, and `NoWait()` append
`FOR UPDATE|SHARE [SKIP LOCKED|NOWAIT]` after paging on Postgres and MySQL
(`FOR SHARE` needs MySQL 8). SQLite has no row locks and ANSI has no portable
form, so both return `ErrUnsupported`. `SkipLocked` and `NoWait` need a lock
and exclude each other.

Postgres also returns `ErrUnsupported` when a locking query declares DISTINCT,
GROUP BY, or HAVING. Raw selection expressions remain database-validated;
the compiler does not inspect them for aggregates or window functions.

### Inspect, Count, and Seek

`Inspect()` returns a `Spec` copy (`Kind` such as `KindSelect`, table, `[]Order` with `Column` and `Desc`, limit,
offset, distinct, grouped, having, raw source, returning). `Count()` returns the matching
immutable `COUNT(*)` query without orders, limit, offset, or locks; grouped, distinct,
having, and raw-sourced queries are wrapped as
`SELECT COUNT(*) FROM (...) AS "count"`. `Seek(values, backward)` adds a keyset
filter from the published query's orders, leaving the base unchanged:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.OrderByDesc("created_at")
	statement.OrderBy("id")
}).Seek([]any{ts, 42}, false)
// WHERE ("created_at" < ? OR ("created_at" = ? AND "id" > ?))
// ORDER BY "created_at" DESC, "id" ASC
```

`backward` reverses every comparison and direction. Values cannot be nil, and
the orders must end in a unique non-null column. `database.CursorPaginate`
and `database.Each` use `Seek` for you.

A raw source makes `Count` and deep offset pages wrap the whole subquery, so
`GROUP BY`, `DISTINCT`, or `LIMIT` inside `FromRaw` SQL act as optimizer
barriers and slow deep pages.

### Structs and named parameters

`Values` turns a struct into the column map that `Insert`, `InsertRows`, and
`Update` take, using `db` tags; `Only` and `Except` pick columns. `Targets`
returns the scan destinations of a result row for a struct with the same
rules. `RawNamed` is `Raw` with `:name` placeholders, bound from a map such as
the one `Values` returns:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.Insert(query.Values(post, query.Except("id")))
	statement.Returning("id")
})
query.RawNamed("UPDATE posts SET title = :title WHERE id = :id", query.Values(post))
```

`Only` and `Except` copy their column-name slices when you create the options,
so later changes to an expanded input slice do not affect a saved option.
The copy covers the column names, not the struct's field values; `Values`
returns those values as is.

Several `Only` options combine their names, as do several `Except` options;
exclusions apply after selection. `Only()` adds no restriction: by itself it
keeps every column, and it does not clear an earlier selection. `Except()`
drops nothing. Expanded nil and empty column slices have the same behavior
as supplying no columns.

`Values` panics on anything but a struct or a non-nil pointer to one.
It returns field values as declared. If only `*T` implements `driver.Valuer`,
an untagged embedded value `T` follows ordinary flattened field mapping;
embed `*T` to retain and bind that Valuer pointer as one column. Types recognized
through `sql.Scanner` and `time.Time` remain scalar rather than flattened.

## Schema

DDL (`CREATE TABLE`, `ALTER TABLE`, indexes, foreign keys) lives in the
`arkoren.dev/foundation/query/schema` package of this module. It uses a
callback API and compiles to argument-free statements for every dialect:

```go
operation := schema.Create("posts", func(table *schema.Table) {
	table.ID()
	table.ForeignID("user_id", "users").OnDelete(schema.Cascade)
	table.Text("body")
	table.Timestamps()
	table.Index("user_id")
})
err := operation.Err()
for _, statement := range operation.Statements() {
	compiled, err := statement.BuildFor(query.Postgres)
}
```

`Create`, `CreateIfNotExists`, and `Alter` accept zero or more declaration
callbacks. After validating the table name, they invoke each supplied function
directly, once, synchronously in argument order on the same table. An invalid
name skips the callbacks. A supplied nil function or application panic propagates
and stops the sequence; an expanded nil or empty callback slice supplies none.

The combined declaration is validated after all callbacks. Omitting callbacks
or supplying an empty declaration does not infer a schema: `Create` still needs
columns and `Alter` needs actions, reported through `Operation.Err()`. Published
operations retain independent table, action, and child-declaration snapshots;
later changes to retained builders cannot change their SQL or statement count.
Builders are not safe for concurrent mutation. Arbitrary values passed to
`Default` are not recursively copied.

`Dialect.TransactionalDDL` reports whether schema changes roll back: true for
Postgres and SQLite, false for MySQL (which commits implicitly) and for ANSI,
because the standard does not require transactional DDL and the compiler
promises nothing for the generic dialect. `Dialect.Quote` quotes one
identifier part. `ErrUnsupported` also covers dialect limits in DDL.

On SQLite, adding a `NOT NULL` column requires a non-NULL literal default;
omitting the default or specifying `Default(nil)` returns `ErrUnsupported`.
Use `Nullable()` when the added column must allow NULL.

SQLite foreign-key targets must be unqualified and resolve in the referencing
table's schema. A qualified `References` target returns `ErrUnsupported` rather
than silently discarding its schema.

Several column `Primary()` modifiers form one composite key in `Create`.
`Alter` rejects multiple added primary-key columns because it emits a separate
statement per added column; use raw DDL for an existing table's composite key.

## Installation

```sh
go get arkoren.dev/foundation/query
```

## Requirements

Go 1.27 or later. Start with the
[query guide](../../cmd/arkoren/skills/arkoren/references/guides/database/query-builder.md)
or use the [API reference](https://pkg.go.dev/arkoren.dev/foundation/query)
for exact declarations.

## Development

```sh
go test -short -race ./...
golangci-lint run
```

## License

MIT. See [LICENSE](LICENSE).
