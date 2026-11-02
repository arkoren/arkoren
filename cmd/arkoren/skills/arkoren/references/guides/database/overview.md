---
title: Overview
description: Use the database facade to read typed results, execute writes, and map SQL columns to Go values.
summary:
  - Understand how the facade executes statements through a database driver.
  - Read single values or collections into Go types and handle missing rows.
  - Execute targeted writes using request structs and interpret the affected-row count.
  - Map SQL columns and nullable values to struct fields.
order: 10
---

# Overview

The database facade runs SQL statements and reads their results into Go values.
Your code works with a `*database.Database`; its driver handles the connection
and database-specific execution. The facade compiles each statement for that
driver's SQL dialect. It is not an ORM: you choose the columns and write the
queries rather than loading models with implicit relationships.

The examples assume an already configured `db` and an operation context `ctx`.
See [Drivers](drivers.md) for construction, connection settings, and lifecycle.
Use the request or job's context for each call so cancellation reaches the
database operation.

Import `database` from `arkoren.dev/core/service/database` and `query` from
`arkoren.dev/foundation/query`. The snippets keep error results visible but omit
routine propagation; handle errors before using results in your application.

## Reading Data

Suppose your application has a `users` table with an integer primary key `id`
and a non-null text column `email`. Tables must already exist; the facade does
not create them from Go types. Use [migrations](../migrations/overview.md) to
manage the schema.

Define the fields you want to read. Each `db` tag names its SQL column:

```go
type User struct {
	ID    int64  `db:"id"`
	Email string `db:"email"`
}
```

To look up a user, describe the selected columns and filter by the primary key:

```go
lookup := query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "email")
	statement.Where("id", 7)
})
```

This builds a statement without executing it. `Find` runs it and scans one row
into the type you specify:

```go
user, err := db.Find[User](ctx, lookup)
```

On success, `user` contains the matching ID and email. If several rows match,
`Find` reads the first; filtering by a unique key avoids an ambiguous result.

If nothing matches, the error matches `database.ErrNoRows`. Import the
standard-library `errors` package to distinguish absence from another failure.
For example, if your application defines `ErrUserNotFound`, translate it at the
domain boundary:

```go
if errors.Is(err, database.ErrNoRows) {
	return ErrUserNotFound
}
```

Other query and driver errors remain errors; do not treat them as a missing user.
`Find` can also read a single selected column into a scalar type, such as
`string` or `int64`, instead of a struct.

For several users, use `Select`. This query reads the first twenty users in ID
order:

```go
users, err := db.Select[User](ctx, query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "email")
	statement.OrderBy("id")
	statement.Limit(20)
}))
```

`Select` returns a `collection.Slice[User]`; `users.Items()` exposes its items
as a Go slice. No matching rows means an empty collection, not `ErrNoRows`.
It loads the complete result into memory, so bound large results with
[pagination](../queries/pagination.md) or use [batching and streaming](batching-and-streaming.md).

## Executing Statements

Suppose an HTTP endpoint accepts a new email address. Keep its input small and
specific to the operation:

```go
type UpdateEmail struct {
	Email string `json:"email" db:"email"`
}
```

Assume `input` is an `UpdateEmail` already decoded and validated by your handler.
`query.Values(input)` maps its fields to database columns using `db` tags, not
`json` tags. This input exposes only the email to update; the target ID stays
outside the request body.

Build an update with a filter that identifies the intended row. Without a
filter, an update can affect every row in the table:

```go
update := query.Table("users", func(statement *query.Builder) {
	statement.Where("id", 7)
	statement.Update(query.Values(input))
})
```

`Exec` executes a statement without scanning result rows:

```go
affected, err := db.Exec(ctx, update)
```

On success, `affected` is the count reported by the driver. An update with no
matching row is not a `Find` operation and does not return `ErrNoRows`. Do not
use a zero count alone as portable proof that a user is missing: databases can
differ in whether an unchanged value counts as an affected row.

The facade also executes insert and delete statements. See
[Query Builder](../queries/query-builder.md) for constructing them, or
[Raw SQL](../queries/raw-sql.md) when you need handwritten SQL. Values passed to
the builder are bound parameters; do not interpolate untrusted values into SQL.

## Data Mapping

Result columns map to exported fields by their `db` tags. Without a tag, the
field name is lowercased: `Email` maps to `email`, but `CreatedAt` maps to
`createdat`, not `created_at`. Use an explicit tag when those names differ.

Every returned column needs a destination field. An unmatched column fails with
an error matching `query.ErrUnknownColumn`; fields not selected remain at their
zero values in the newly returned struct. Selecting named columns, as above,
keeps your result shape explicit. Use `db:"-"` for a field that must not receive
a column value.

If `email` can be SQL `NULL`, replace the plain string with a nullable field.
For example, this alternative result type preserves the distinction between
a missing email and an empty one:

```go
type Contact struct {
	ID    int64   `db:"id"`
	Email *string `db:"email"`
}
```

Read it with `Find[Contact]` or `Select[Contact]`. A SQL `NULL` becomes a nil
pointer; an empty string becomes a non-nil pointer to `""`. The standard
library's `sql.NullString` is another option. Scanning SQL `NULL` into a plain
`string` fails rather than silently replacing it with an empty string.

For the complete facade contract, see the
[`database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
