---
title: Schema Builder
description: Define and change tables, columns, indexes, and constraints across database dialects.
summary:
  - Describe tables, columns, indexes, and foreign keys as schema operations.
  - Add or change columns with explicit dialect and data-safety constraints.
  - Inspect generated statements without confusing compilation with execution.
order: 20
---

# Schema Builder

The schema builder describes DDL: statements that create or change database
objects. An operation is an immutable definition that compiles into one or more
statements. It does not inspect your existing schema or execute SQL. Use
[migration steps](overview.md#migration-steps) to apply operations with history
and coordination.

Import `arkoren.dev/foundation/query/schema` and `arkoren.dev/foundation/query`.

## Creating Tables

For a user table with optional biography text:

```go
users := schema.Create("users", func(table *schema.Table) {
	table.ID()
	table.String("email", 255).Unique()
	table.Text("bio").Nullable()
})
```

`ID` adds an auto-incrementing primary key named `id`. Columns are NOT NULL
unless `Nullable` is called. `String` requires a positive length; SQLite renders
it as TEXT rather than enforcing that length. Validate portable application
length and numeric range rules yourself.

Common column choices include integers, booleans, decimals, UUIDs, JSON, binary,
dates, and timestamps. `Timestamps()` adds `created_at` and `updated_at` with
current-time defaults; it does not install an automatic update trigger.
`TimestampTZ` retains provider differences: MySQL uses datetime(6), not a
timezone-aware type. Store UTC consistently, especially on SQLite where
timestamp comparisons use text.

Use `Default(value)` for scalar literal defaults and `DefaultRaw(expression)`
only for trusted database expressions. DDL defaults are rendered into SQL, not
bound parameters. Never pass request text to raw types or expressions.

Database default collations often compare text case-insensitively (MySQL) or
by locale (PostgreSQL), so `"abc"` and `"ABC"` may collide under a unique
index. Call `Bytewise()` on a `String` or `Text` column to compare byte for
byte, case, accents and trailing spaces included. Use it for identifiers that
must match exactly, such as the subject an auth module stores for a user:

```go
identities := schema.Create("identities", func(table *schema.Table) {
	table.ID()
	table.String("subject", 255).Bytewise().Unique()
})
```

It renders `COLLATE "C"` on PostgreSQL, `COLLATE BINARY` on SQLite and
`COLLATE utf8mb4_0900_bin` on MySQL 8, a no-pad collation so `"abc "` differs
from `"abc"`. MariaDB lacks that collation, and ANSI has no portable name, so
compiling for it returns `query.ErrUnsupported`. Other column types are
construction errors. MySQL cannot index `Text` without a prefix length, so
index bytewise columns declared with `String` and a length of at most 768.

## Keys and Indexes

If email must be unique per tenant rather than globally, use this alternative
table definition. Assume `tenants` already exists with a compatible `id`:

```go
users := schema.Create("users", func(table *schema.Table) {
	table.ID()
	table.ForeignID("tenant_id", "tenants").OnDelete(schema.Cascade)
	table.String("email", 255)
	table.Unique("tenant_id", "email")
})
```

`ForeignID` adds a bigint column referencing the parent's `id`; cascade deletes
remove dependent users when their tenant is deleted. Choose that destructive
behavior deliberately. Without a cascade policy, do not assume parent deletion
will clean up child rows. SQLite foreign-key enforcement is a connection policy
you must enable in the database configuration.

`Index` adds a nonunique index. `Unique` and a column's `Unique()` create unique
indexes rather than inline constraints. Generated names use the table and
columns: `users_tenant_id_email_key` for this unique index and
`users_tenant_id_fkey` for the foreign key. Schema-qualified names contribute
their schema. Names, including generated ones, must fit 63 bytes.

To remove generated objects during an `Alter`, supply the original columns to
`DropIndex`, `DropUnique`, or `DropForeign`, not the generated index name. `Primary`
supports a table-level primary key; choose key design before data depends on it.

## Adding Columns

For a rolling deployment, begin with an additive change old code can tolerate:

```go
addPhone := schema.Alter("users", func(table *schema.Table) {
	table.String("phone", 32).Nullable()
})
```

The operation adds a nullable column and leaves existing rows readable. Backfill
and application changes are separate steps; do not add a required column to
populated data without deciding how existing rows receive valid values. DDL can
lock or rebuild a table depending on the engine and operation, so assess it on
production-sized data before deployment.

`RenameColumn` and `DropColumn` belong inside `Alter`; `schema.Rename` renames
a table and `schema.Drop` removes one. Renames and drops can break old instances
still serving traffic. Follow [expand and contract](deployment.md#rolling-deploys)
and back up data before destructive changes.

## Changing Columns

PostgreSQL can change only the aspects you specify. If an existing phone column
should become nullable:

```go
changePhone := schema.Alter("users", func(table *schema.Table) {
	table.Change("phone").Nullable()
})
```

This PostgreSQL-oriented operation does not restate its type or default. SQLite
cannot compile column changes and returns `query.ErrUnsupported`. Plan a tested
table-rebuild migration when SQLite needs that transformation.

MySQL type or nullability changes use MODIFY COLUMN, which restates the whole
column. Supply a type and either `Nullable` or `NotNull` together, and restate
any default you need to retain. A default-only change uses ALTER COLUMN instead.
There is no universally safe ALTER recipe: inspect the existing attributes and
dependent objects before deciding what to restate or transform. `Change.Type`
and `Change.Using` contain trusted verbatim SQL, not validated user input.

## Inspecting Statements

Check an operation's construction error, then inspect each statement for your
target dialect. Start with the `users` operation above:

```go
err := users.Err()
```

After handling that error, compile its statements individually for PostgreSQL:

```go
for _, statement := range users.Statements() {
	compiled, err := statement.BuildFor(query.Postgres)
	// Inspect compiled.SQL() after handling err.
}
```

Create emits the table first, then one statement per index. Alter emits statements in written
order. Operation construction errors make every statement fail, while dialect
limits appear at compilation under `query.ErrUnsupported`; schema errors also
match `query.ErrInvalidQuery`. Compilation does not verify existing columns,
permissions, data conversion, or acceptable lock time.

`CreateIfNotExists` and `DropIfExists` help make some steps repeatable, but they
do not prove an existing object has the intended definition. Table IF NOT EXISTS
is unavailable on ANSI; index IF NOT EXISTS is unavailable on ANSI and MySQL.
A multi-statement operation can partially apply without transactional DDL.

Literal rendering assumes PostgreSQL `standard_conforming_strings=on` and MySQL
without `NO_BACKSLASH_ESCAPES`. MySQL text/blob/JSON defaults require MySQL 8.0.13
or newer. SQLite REFERENCES targets must be unqualified and resolve in the
referencing table's schema. Test generated SQL on each supported deployment
profile, not only against ANSI output.

For all operations and column modifiers, see the
[`schema` API reference](https://pkg.go.dev/arkoren.dev/foundation/query/schema).
