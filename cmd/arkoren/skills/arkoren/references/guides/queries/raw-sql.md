---
title: Raw SQL
description: Run handwritten SQL safely, bind parameters, and account for dialect differences.
summary:
  - Bind positional or named values in handwritten SQL.
  - Reuse raw result sets through structured query composition.
  - Recognize parser limits and keep dialect-specific SQL explicit.
order: 20
---

# Raw SQL

Use handwritten SQL when a database operation does not fit the
[Query Builder](query-builder.md), such as a CTE or a vendor-specific expression.
`query.Raw` and `query.RawNamed` still bind values and compile placeholders for
the driver. They do not translate SQL syntax or validate the statement's effect.

Import `arkoren.dev/foundation/query`. The examples assume a configured `db`,
`ctx`, and a `User` result with `id` and `email` fields from
[Database Overview](../database/overview.md#reading-data).

## Bound Values

For a user lookup, put the ID in the argument list rather than formatting it
into the SQL string:

```go
lookup := query.Raw("SELECT id, email FROM users WHERE id = ?", userID)
user, err := db.Find[User](ctx, lookup)
```

The facade rewrites `?` to the driver's placeholder syntax, such as `$1` on
PostgreSQL. `Find` preserves the usual missing-row behavior; `Select` scans
several rows, and `Exec` returns the affected-row count without scanning rows.

Parameters bind values, not table names, column names, operators, or sort
directions. Choose those through a fixed application allowlist. Never interpolate
untrusted text into SQL; raw statements do not have the builder's unfiltered-write
guard, so retain explicit authorization and WHERE conditions on writes.

## Named Parameters

For a targeted update with several values, name the bindings. First define the
already decoded and validated input:

```go
type UpdateEmail struct {
	Email string `json:"email" db:"email"`
}
```

Keep the authorized target outside that input, then add it to the binding map:

```go
values := query.Values(input)
values["id"] = userID
update := query.RawNamed("UPDATE users SET email = :email WHERE id = :id", values)
affected, err := db.Exec(ctx, update)
```

`RawNamed` replaces each `:name` outside quotes and comments with a positional
binding. Repeating a name binds its value again. A missing name fails compilation;
extra map entries are unused. PostgreSQL's `::` cast syntax stays intact.
`Values` uses database tags, not JSON decoding tags.

## Raw Sources

When handwritten SQL produces a reusable result set, wrap it with `FromRaw`
and apply ordinary builder filtering outside it:

```go
users := query.FromRaw(query.Raw(
	"SELECT id, email FROM users WHERE active = ?", true,
), func(statement *query.Builder) {
	statement.Select("id", "email")
	statement.OrderBy("id")
})
```

This renders the raw SELECT as a subquery with alias `t` by default; `As`
changes that alias. Inner arguments bind before outer ones. The resulting
`query.Query` supports `Count` and [pagination](pagination.md). Its raw source
must be a valid subquery for the database and return uniquely named columns
when wrapped for counting. A whole `RawStatement` is opaque and cannot be
passed directly to the facade's pagination methods.

For smaller expressions, use `WhereRaw`, `HavingRaw`, or `SetRaw` with trusted
SQL and `?` bindings. `SelectRaw` is different: it accepts no arguments and
copies its expression verbatim without scanning placeholders.

## Dialect Boundaries

Raw SQL preserves your grammar. PostgreSQL-only operators, MySQL functions, and
SQLite PRAGMAs remain provider-specific even though placeholders are rewritten.
Use `BuildFor(db.Dialect())` when compiling for a native client; `Build()` uses
ANSI syntax. Native execution bypasses facade telemetry and context-based
session/transaction routing.

The positional scanner counts `?` outside standard single, double, and backtick
quotes, line comments, and block comments. The count must match the arguments.
Write `??` for a literal question mark, such as a PostgreSQL JSON operator, in
scanned raw SQL. In `SelectRaw`, write the operator as a single `?` instead.

The scanner does not understand PostgreSQL dollar quoting or MySQL backslash
escapes inside strings. Pass those contents as bound values instead of embedding
them. Block comments nest only on ANSI and PostgreSQL. PostgreSQL line comments
end at CR or LF; other dialects use LF. MySQL recognizes `--` as a comment only
when followed by whitespace or a control character. SQL syntax validation still
belongs to the database; placeholder processing is not a general SQL parser.

For raw statements and compiler rules, see the
[`query` API reference](https://pkg.go.dev/arkoren.dev/foundation/query).
