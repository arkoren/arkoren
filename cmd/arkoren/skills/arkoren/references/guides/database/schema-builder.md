---
title: Schema Builder
description: Create, alter, and drop tables, columns, indexes, and foreign keys with operations that compile to PostgreSQL, MySQL, or SQLite SQL.
order: 40
---

# Schema Builder

## Introduction

To create the tables your application queries, describe their structure with
the schema builder. You declare the columns,
keys, and indexes of a table inside a function, and the result is an
**operation**, an immutable value that compiles to the SQL statements that make
the change. The builder covers `CREATE TABLE`, `ALTER TABLE`, `DROP TABLE`,
`CREATE INDEX`, and `DROP INDEX`. It lives in the `schema` package of the
`query` module (`arkoren.dev/foundation/query/schema`), so there is nothing
extra to install.

An operation compiles for a **dialect**, the SQL flavor of the database. You
write the table once, and the builder renders the right column types,
quoting, and constraint syntax for whatever database the application runs on.
That is what lets the same migration create a table on SQLite in your tests
and on PostgreSQL in production. Where the databases genuinely cannot do the
same thing, compiling fails with `query.ErrUnsupported` instead of guessing.

Operations only describe statements. You run them with
[`migrate.Schema`](migrations.md) inside a migration, and you can compile one
without a database to look at the SQL.

## Creating a table

To create a table, import `arkoren.dev/foundation/query/schema` and call
`schema.Create` with the table name and a function that defines it. The execution
line uses `migrate` from `arkoren.dev/core/service/database/migrate`, your configured
`db` from the [database overview](overview.md), and an operation context `ctx`:

```go
users := schema.Create("users", func(table *schema.Table) {
	table.ID()
	table.String("email", 255).Unique()
	table.String("name", 100)
	table.Timestamps()
})

err := migrate.Schema(users)(ctx, db)
```

`ID` adds an auto-incrementing `bigint` primary key named `id`. The email's
`String` length is 255 characters on PostgreSQL and MySQL; SQLite stores text
without enforcing that length. `Unique` makes the email unique.
`Timestamps` adds `created_at` and `updated_at`, which default to the current
time. On SQLite the operation runs two statements:

```sql
CREATE TABLE "users" ("id" INTEGER PRIMARY KEY AUTOINCREMENT, "email" TEXT NOT NULL, "name" TEXT NOT NULL, "created_at" TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, "updated_at" TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)
CREATE UNIQUE INDEX "users_email_key" ON "users" ("email")
```

`updated_at` is an insertion default, not an automatic update hook. Set it in
your update statements when the application needs modification timestamps.
SQLite foreign keys are disabled unless enabled on each connection; configure
the [SQLite driver](drivers.md#configuration) before relying on them.

Each type method adds a column and returns it, and the modifiers you chain on
it (`Unique`, `Nullable`, `Default`, and the others in this guide) apply to
that column. A table needs at least one column, and a column name can appear
only once.

`Create`, `CreateIfNotExists`, and `Alter` accept zero or more callbacks. Each
supplied callback runs directly, once, synchronously in argument order on the
same `*schema.Table`. The completed declaration is validated after all callbacks,
so reusable declarations can run before a local addition. Normally one callback
keeps the declaration together. An invalid table name is rejected before any
callback runs.

Omitting callbacks, passing an empty callback, or expanding a nil or empty
callback slice leaves an empty declaration. It fails normal validation: CREATE
needs columns and ALTER needs actions. A supplied nil function is different:
invoking it panics. Application panics also propagate and stop later callbacks.

The operation keeps independent snapshots of the root table, actions, and child
declarations. Changes to a retained `Table`, `Column`, `Change`, or `Foreign`
after construction cannot change its SQL or statement count. Do not mutate
builders concurrently. Arbitrary values passed to `Default` are not recursively
copied.

Domain mistakes are kept and returned by `Err()`, which
wraps `query.ErrInvalidQuery`: invalid or too long names, an index over a
column the table does not have, a modifier that does not apply, or an empty
declaration. The first error is retained. `migrate.Schema` checks construction
errors across its operations before running any statements.

The builder accepts a schema-qualified name such as `"billing.invoices"` for
tables, and it quotes each part for the dialect. Each identifier part must be
an unquoted SQL name of at most 63 bytes. Column names must be unqualified.

To create a table only when it is missing, use `schema.CreateIfNotExists`,
which also adds `IF NOT EXISTS` to the table's indexes:

```go
schema.CreateIfNotExists("audit_log", func(table *schema.Table) {
	table.ID()
	table.Text("action")
})
```

ANSI rejects the table form. MySQL accepts the table form but rejects
`CREATE INDEX IF NOT EXISTS`, so an operation with indexes can fail after its
table statement. See [dialect differences](#dialect-differences) before using
this form in a migration.

## Column types

Each column method names a portable type, and the dialect decides how it is
stored. The most common ones are:

| Method | Holds | PostgreSQL | MySQL | SQLite |
| --- | --- | --- | --- | --- |
| `String(name, n)` | text up to `n` characters | `varchar(n)` | `varchar(n)` | `TEXT` |
| `Text` | text of any length | `text` | `text` | `TEXT` |
| `Integer` | 32-bit integer | `integer` | `int` | `INTEGER` |
| `BigInteger` | 64-bit integer | `bigint` | `bigint` | `INTEGER` |
| `Boolean` | true or false | `boolean` | `boolean` | `INTEGER` |
| `Float` | double-precision number | `double precision` | `double` | `REAL` |
| `Decimal(name, p, s)` | exact number | `numeric(p,s)` | `decimal(p,s)` | `NUMERIC` |
| `UUID` | identifier | `uuid` | `char(36)` | `TEXT` |
| `JSON` | JSON document | `jsonb` | `json` | `TEXT` |
| `Binary` | bytes | `bytea` | `blob` | `BLOB` |
| `Date` | calendar date | `date` | `date` | `DATE` |
| `Timestamp` | date and time | `timestamp(6)` | `datetime(6)` | `DATETIME` |
| `TimestampTZ` | moment in time | `timestamptz` | `datetime(6)` | `TIMESTAMP` |

`SmallInteger` and `Char(name, n)` complete the set. Use `Decimal` for money,
since `Float` rounds.

`String` and `Char` lengths must be positive and count characters, not bytes.
For `Decimal(name, p, s)`, precision `p` is the total number of digits and scale
`s` is the number after the decimal point: `p > 0` and `0 <= s <= p`. The
compiler checks these ranges, not every database's maximum type size.

Two of these deserve a closer look. MySQL has no time zone aware timestamp, so
`TimestampTZ` becomes `datetime(6)` there and stores whatever value you give
it: write times in UTC on every database. And SQLite has no real types for
booleans, UUIDs, JSON, or timestamps; it stores them as integers and text (see
the [SQLite driver](drivers.md)). Keep timestamps in UTC on SQLite as well,
because it compares them as text.

If you need a type the builder does not know, such as PostgreSQL's `citext`,
use `Column` with the type name:

```go
schema.Create("accounts", func(table *schema.Table) {
	table.ID()
	table.Column("handle", "citext")
})
```

> [!WARNING]
> `Column`, `DefaultRaw`, `Change.Type`, and `Change.Using` write their argument
> into the SQL exactly as given. Pass literals from your own source code, never
> user input.

## Nullability and defaults

Columns are `NOT NULL` unless you say otherwise. This is the opposite of SQL's
own default: inserting NULL fails instead of storing it. To allow `NULL`, call
`Nullable`:

```go
schema.Create("posts", func(table *schema.Table) {
	table.ID()
	table.String("title", 200)
	table.Text("summary").Nullable()
})
```

`Default` sets a literal default for strings, numbers, booleans, and `nil`
(which means `NULL`). The builder renders the literal for the dialect, so a
boolean default becomes `TRUE` on PostgreSQL and `1` on MySQL and SQLite.
Inside a table callback:

```go
table.Boolean("published").Default(false)
table.String("status", 20).Default("draft")
```

Floats must be finite. Unsupported types, NaN, infinities, and strings containing
NUL record construction errors reported by `Operation.Err()`, including for
`Change.Default`. A later valid default does not clear an earlier error.

`DefaultNow` defaults a `Timestamp` or `TimestampTZ` column to the current time,
which is `CURRENT_TIMESTAMP` on PostgreSQL and SQLite and
`CURRENT_TIMESTAMP(6)` on MySQL. For anything else, `DefaultRaw` takes an SQL
expression, such as `DefaultRaw("gen_random_uuid()")` on PostgreSQL.

Literal defaults escape single quotes, reject NUL, and double MySQL
backslashes. This assumes standard server modes: PostgreSQL
`standard_conforming_strings=on` and MySQL without `NO_BACKSLASH_ESCAPES`.
Raw expressions remain trusted SQL. Statements pass through `query.Raw` with
no arguments, so a `?` recognized as a placeholder fails compilation. A `?`
inside supported quotes or comments is preserved; `??` outside them writes a
literal question mark. These expressions use the
[raw SQL scanner's supported subset](raw-sql-and-locks.md#placeholder-scanning),
not PostgreSQL dollar quoting or backslash-escaped string literals. Defaults
cannot bind arguments; do not put user input into `DefaultRaw` or `Using`.

## Primary keys

`ID` is the right primary key for most tables. When creating a table whose key
is not a single auto-incrementing number, mark one or more columns with
`Primary`. Inside `Create` or `CreateIfNotExists`, several marked columns form
one composite key, in the order you declare them:

```go
schema.Create("post_tag", func(table *schema.Table) {
	table.BigInteger("post_id").Primary()
	table.BigInteger("tag_id").Primary()
})
```

If you prefer to state the key in one place, `table.Primary("post_id", "tag_id")`
does the same at table level. A table can have only one primary key, so mixing
`Primary` on a column with `table.Primary(...)`, or either with `ID`, is an error.

This composite-key behavior is for `Create`, not `Alter`. In `Alter`, each added
column is a separate statement. Marking multiple added columns as `Primary`
would add multiple primary keys, so construction fails with
`query.ErrInvalidQuery`. `table.Primary(...)` is also create-only. Use
dialect-specific raw DDL in a migration to add an existing table's composite
key; SQLite requires a table rebuild rather than adding that constraint.

## Indexes and unique keys

An index makes lookups on its columns faster, at the cost of slower writes
and more storage. `Index` adds one over one or more columns, and `Unique` adds
an index that also enforces uniqueness. Both accept several columns for a rule
that spans them, and a column's own `Unique()` is the same as `table.Unique` on
that column:

```go
schema.Create("memberships", func(table *schema.Table) {
	table.BigInteger("user_id")
	table.BigInteger("team_id")
	table.String("role", 20).Default("member")
	table.Primary("user_id", "team_id")
	table.Unique("team_id", "role")
	table.Index("role")
})
```

A `Create` emits the `CREATE TABLE` first and then one `CREATE INDEX` per
index, in the order you wrote them. Unique keys are always unique indexes,
never inline constraints, so every dialect treats them the same way and a
unique column can also be added to an existing table, even on SQLite.

The builder names every key for you, deterministically, from the table and
column names: `memberships_pkey`, `memberships_team_id_role_key` for a unique
key, `memberships_role_index` for a plain index, and `posts_user_id_fkey` for
a foreign key. A schema-qualified table adds its schema (`billing_invoices_number_key`).
You rarely need the names, because the drop methods take the same columns as
the create and derive them. A generated name longer than 63 bytes, the
PostgreSQL limit, is an error, so long table and column names can be a reason
to shorten them. Two keys that would get the same name are an error too.

Drop operations validate their generated names as well. In particular,
`DropForeign(column)` checks `<table>_<column>_fkey` during construction, including
the schema contribution to the name. If it exceeds 63 bytes, `Operation.Err()`
already matches `query.ErrInvalidQuery`; the error is not deferred until SQL
compilation or execution.

## Foreign keys

A foreign key makes the database reject rows that point at something that
does not exist. `ForeignID` adds a `bigint` column that references the `id`
column of another table; follow it with what should happen when the
referenced row is deleted:

```go
schema.Create("posts", func(table *schema.Table) {
	table.ID()
	table.ForeignID("user_id", "users").OnDelete(schema.Cascade)
	table.String("title", 200)
})
```

For another type or parent column, use `References` on any column:
`table.UUID("owner_id").References("owners", "uuid")`.

PostgreSQL and MySQL accept schema-qualified reference targets. SQLite requires
an unqualified target such as `"users"`, which resolves in the referencing
table's schema. A target such as `"main.users"` fails SQLite compilation with
`query.ErrUnsupported` and `query.ErrInvalidQuery`, even when it names the same
schema; the compiler does not silently discard the qualifier. This applies to
both `References` and `ForeignID` when creating or adding a column.

The actions are `schema.Cascade` (delete or update the referencing rows too),
`schema.SetNull`, `schema.Restrict`, and `schema.NoAction`. `OnDelete` and
`OnUpdate` need a `References` (or `ForeignID`) on the same column. A foreign
key needs a parent column of the same type, so create `users` first and drop it
last. A `SetNull` action also needs a `Nullable` column.

To add a foreign key to a table that already exists, use `Foreign` inside
`schema.Alter`, and `DropForeign` with the same column to remove it:

```go
schema.Alter("posts", func(table *schema.Table) {
	table.Foreign("user_id", "users", "id").OnDelete(schema.Cascade)
})
```

The constraint is named `posts_user_id_fkey`. SQLite cannot add or drop a
constraint on an existing column through `Foreign` or `DropForeign`. It can add
a new referencing column when that column is nullable and its default is
omitted or NULL; see [Dialect differences](#dialect-differences).

## Altering tables

`schema.Alter` changes an existing table. Actions render in the order you
declare them, so a single `Alter` can make several changes:

```go
schema.Alter("users", func(table *schema.Table) {
	table.String("nickname", 50).Nullable()
	table.RenameColumn("nickname", "handle")
	table.Index("handle")
	table.DropColumn("legacy_a", "legacy_b")
})
```

A column type method adds a column, with the same modifiers as in `Create`.
`DropColumn` with several names drops each in its own statement. `DropIndex`
and `DropUnique` take the columns of the `Index` or `Unique` that created the
index. Methods that only make sense on an existing table (`DropColumn`,
`RenameColumn`, `Change`, `DropIndex`, `DropUnique`, `Foreign`, `DropForeign`)
are errors inside `Create`.

Adding a `NOT NULL` column to a table that already has rows needs a default,
or the database rejects the change because the existing rows would have no
value. Give the column a `Default`, or add it as `Nullable`, fill it in, and
tighten it in a later migration.

For SQLite, compilation requires a non-NULL literal default for any added
`NOT NULL` column, without inspecting whether the table has rows. Omitting the
default or passing `Default(nil)` fails with `query.ErrUnsupported`. For example,
this added column compiles with a zero default:

```go
schema.Alter("users", func(table *schema.Table) {
	table.Integer("visits").Default(0)
})
```

SQLite also rejects `DefaultNow`, `DefaultRaw`, and added primary keys. A new
referencing column cannot have a non-NULL default, so make it `Nullable()` and
omit its default or use `Default(nil)`. Tightening nullability later requires a
SQLite table rebuild, not `Change`.

A unique column becomes two statements, `ADD COLUMN` followed by
`CREATE UNIQUE INDEX`, on every dialect.

To rename a table, use `schema.Rename`; the new name has no schema, because a
table keeps its schema when renamed:

```go
schema.Rename("users", "members")
```

### Changing a column

`table.Change(name)` records changes to a column's type, nullability, and default.
On PostgreSQL and ANSI, only the aspects you set change. MySQL type/nullability
changes restate the whole column and can drop an unstated default, as explained
below.

For PostgreSQL, convert an existing reference column to an integer, remove a
note's default, and change the default status with separate changes:

```go
schema.Alter("events", func(table *schema.Table) {
	table.Change("ref").Integer().NotNull().Using("ref::integer")
	table.Change("note").DropDefault()
	table.Change("status").Default("new")
})
```

The type methods are those of the column types above (`String(length)`,
`Integer()`, and so on) plus `Type(sqlType)` for a raw type. The defaults are
`Default(v)`, `DefaultNow()`, `DefaultRaw(expr)`, and `DropDefault()`. Calling
nothing is an error. `Using(expr)` is PostgreSQL's expression for converting
existing values; it needs a type, and other dialects reject it.

On PostgreSQL each aspect you set becomes its own `ALTER COLUMN` clause in one
statement, so the first line above runs
`ALTER COLUMN "ref" TYPE integer USING ref::integer, ALTER COLUMN "ref" SET NOT NULL`.
MySQL works differently, because it can only restate a whole column to change
its type or nullability:

- A change of the default alone runs `ALTER COLUMN ... SET DEFAULT` or
  `DROP DEFAULT`.
- A change of the type or nullability runs `MODIFY COLUMN` with the full
  definition. It therefore needs **both** a type and `Nullable()` or
  `NotNull()`; with only one of them, compiling fails with
  `query.ErrUnsupported` and a message saying what is missing.
- `MODIFY COLUMN` drops a default you do not restate. If the column has a
  default you want to keep, call `Default` in the same `Change`.

SQLite cannot change a column.

## Dropping tables

> [!WARNING]
> Executing a drop operation deletes the table's data. Back up data you need,
> remove dependencies deliberately, and prefer additive migrations during a
> rolling deployment.

`schema.Drop` removes a table with its rows and indexes. `schema.DropIfExists`
makes it succeed when the table is already gone:

```go
schema.DropIfExists("audit_log")
```

A table that other tables reference by foreign key cannot be dropped until
those tables, or their constraints, are gone.

## Dialect differences

The builder renders each statement for the dialect the database reports, so
the same code produces different SQL. Here is one table on all three:

```go
schema.Create("posts", func(table *schema.Table) {
	table.ID()
	table.ForeignID("user_id", "users").OnDelete(schema.Cascade)
	table.Boolean("published").Default(false)
	table.JSON("meta").Nullable()
})
```

On PostgreSQL:

```sql
CREATE TABLE "posts" ("id" bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, "user_id" bigint NOT NULL, "published" boolean NOT NULL DEFAULT FALSE, "meta" jsonb, CONSTRAINT "posts_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON DELETE CASCADE)
```

On MySQL, which quotes with backticks and numbers its auto-increment differently:

```sql
CREATE TABLE `posts` (`id` bigint NOT NULL AUTO_INCREMENT PRIMARY KEY, `user_id` bigint NOT NULL, `published` boolean NOT NULL DEFAULT 0, `meta` json, CONSTRAINT `posts_user_id_fkey` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE)
```

On SQLite:

```sql
CREATE TABLE "posts" ("id" INTEGER PRIMARY KEY AUTOINCREMENT, "user_id" INTEGER NOT NULL, "published" INTEGER NOT NULL DEFAULT 0, "meta" TEXT, CONSTRAINT "posts_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON DELETE CASCADE)
```

Beyond types and quoting, some operations exist on only some databases. When
you ask for one that is missing, compiling the statement fails with an error
that wraps both `query.ErrUnsupported` and `query.ErrInvalidQuery`, before that
statement runs. `Err()` does not report these, because they depend on the dialect.
Compilation happens one statement at a time during execution, so earlier
statements may already have run when a later one fails. Transactional DDL and
[migration rollback behavior](migrations.md#failures-and-transactions) determine
whether those earlier changes remain applied.

| Operation | PostgreSQL | MySQL | SQLite |
| --- | --- | --- | --- |
| `CreateIfNotExists` on indexes | yes | no | yes |
| `Change` | yes | yes; type/nullability changes need both | no |
| `Change` with `Using` | yes | no | no |
| `Foreign`, `DropForeign` | yes | yes | no |
| Added column that is `Primary` or auto-increment | yes | yes | no |
| Added column that is `NOT NULL` with no default or `Default(nil)` | yes, subject to existing data | yes, subject to existing data | no |
| Schema-qualified `References` or `ForeignID` target | yes | yes | no |

These entries describe what the compiler can render, not proof that the
database accepts the statement or existing data. SQLite's `ALTER TABLE` support
is deliberately small: an added `NOT NULL` column needs a non-NULL literal
default, while a new referencing column must allow NULL with no non-NULL default.
Neither may carry an added primary key or a current-time or raw default.
The builder cannot change a SQLite column or add or drop a constraint on an
existing column. To make such a change on SQLite, create a new table, copy the
rows with raw SQL, drop the old
table, and rename the new one. If your production database is PostgreSQL or
MySQL, write these migrations for it and keep the SQLite tests to the
statements that SQLite supports, or run the tests against the real database.

> [!NOTE]
> The statements the builder cannot express are rare, but they exist, for
> example PostgreSQL partial indexes. For those, run `query.Raw` in the
> migration and restrict it to a dialect, as [Migrations](migrations.md#dialect-specific-migrations)
> shows.

One more difference matters when you write migrations: PostgreSQL and SQLite
roll back schema changes inside a transaction, and MySQL does not.
[Migrations](migrations.md#failures-and-transactions) explains what that means
for a migration that fails half way.

## Building without executing

Every operation has `Statements()`, which returns its statements in execution
order, and each one implements `BuildFor(dialect)`. The number of statements
does not depend on the dialect. This is useful to review the SQL a migration
will run, to write tests that pin it, or to produce a script for a database
administrator:

```go
op := schema.Create("tags", func(table *schema.Table) {
	table.ID()
	table.String("name", 50).Unique()
})
for _, statement := range op.Statements() {
	compiled, err := statement.BuildFor(query.Postgres)
	fmt.Println(compiled.SQL())
}
```

```sql
CREATE TABLE "tags" ("id" bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, "name" varchar(50) NOT NULL)
CREATE UNIQUE INDEX "tags_name_key" ON "tags" ("name")
```

If the operation has a construction error, `Statements()` returns one statement
whose `BuildFor` returns that error, so that operation cannot run partially.
The four dialects are `query.Postgres`, `query.MySQL`, `query.SQLite`, and
`query.ANSI`, the portable baseline, which does not support the
`IF NOT EXISTS` and `IF EXISTS` forms.

`migrate.Schema` does this for you with the dialect the database reports, so
you rarely call `BuildFor` yourself outside of tests. `db.Dialect()` returns
the dialect when your own code needs to switch on it.

For every type and method, see [the `schema` API reference](https://pkg.go.dev/arkoren.dev/foundation/query/schema),
and for running schema changes in order, [Migrations](migrations.md).
