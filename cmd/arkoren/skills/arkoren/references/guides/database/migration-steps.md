---
title: Migration Steps
description: Build migration steps from schema operations with migrate.Schema, from SQL statements with migrate.Exec, or as functions, and combine them in one migration.
order: 55
---

# Migration Steps

A [migration](migrations.md) applies its changes through **steps**: `Up` and
`Down` each take one or more and run them in order.

## Introduction

A step is a `migrate.Func`, a function that receives the context and the
database and returns an error:

```go
type Func = func(ctx context.Context, db *database.Database) error
```

You rarely write one by hand. The package builds the two common kinds for you,
and you write a function only when a step needs logic:

| Step | Use it for | Takes |
| --- | --- | --- |
| `migrate.Schema` | Creating, changing, renaming, and dropping tables | [Schema operations](schema-builder.md) |
| `migrate.Exec` | SQL the schema builder does not cover, and set-based data changes | `query.Raw` and query builder statements |
| A function | Loops, reads before writes, and per-dialect branches | Your code |

Every kind runs with the same `ctx` and `db`. When the migration runs in a
transaction, `ctx` carries it, so all three kinds join it; see
[Failures and transactions](migrations.md#failures-and-transactions).

The supplied context also carries the session that holds the migration lock.
Use it for every facade call: a fresh context or direct `db.Driver()` call
bypasses that session and can deadlock a one-connection pool. Close streaming
cursors before later statements, and do not retain the context or use it
concurrently.

Import `arkoren.dev/core/service/database/migrate` for steps,
`arkoren.dev/foundation/query/schema` for schema operations, and
`arkoren.dev/foundation/query` for SQL statements. Function steps additionally use
`context` from the standard library and `arkoren.dev/core/service/database`.
Examples that alter or reference `users` assume your earlier migration created it.

## Schema changes with migrate.Schema

`migrate.Schema` takes one or more schema operations, such as `schema.Create`,
`schema.Alter`, `schema.Rename`, and `schema.Drop`, and runs them in the order
you pass them. Use it for every change to the structure of a table:

```go
migrate.NewMigration("create-posts", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Schema(
		schema.Create("posts", func(table *schema.Table) {
			table.ID()
			table.ForeignID("user_id", "users").OnDelete(schema.Cascade)
			table.String("title", 200)
			table.Timestamps()
			table.Index("user_id")
		}),
		schema.Alter("users", func(table *schema.Table) {
			table.Integer("posts_count").Default(0)
		}),
	))
	migration.Down(migrate.Schema(
		schema.Alter("users", func(table *schema.Table) {
			table.DropColumn("posts_count")
		}),
		schema.Drop("posts"),
	))
})
```

`Down` lists the reverse operations in reverse order: the column added last is
dropped first, and the table the others may reference is dropped last.

When the step runs, `Schema` works in two passes:

1. It checks every operation for mistakes, such as an index on a column the
   table does not have. If one is wrong, the step fails with `operation N: ...`
   before any SQL runs.
2. It compiles each operation for the database's dialect and runs its
   statements in order. One operation can be several statements: the `Create`
   above is a `CREATE TABLE` followed by a `CREATE INDEX`. A failure reads
   `operation N statement M: ...`, counted from 1.

Because the operations compile when the step runs, the same migration works on
PostgreSQL, MySQL, and SQLite. The first pass does not know the dialect, so an
operation that a dialect cannot run, such as `table.Change` on SQLite, fails
in the second pass with `query.ErrUnsupported`, after the statements before it
have run. Inside a transaction those statements are rolled back with the rest
of the migration.
`migrate.Schema()` with no operations fails with `migrate.ErrInvalidMigration`
when the step runs.

`Schema` copies the operation slice when the helper is created, without
compiling or executing it. Later replacement of a caller's slice entry cannot
change the step; each published schema operation already owns its declaration
snapshot.

## SQL statements with migrate.Exec

For SQL the schema builder cannot express, or for a set-based data change, use
`migrate.Exec`. It runs one or more ready-made `query.Statementer` values in
order. Write database-specific SQL with `query.Raw`:

```go
migrate.NewMigration("enable-citext", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Exec(query.Raw("CREATE EXTENSION IF NOT EXISTS citext")))
	migration.Down(migrate.Exec(query.Raw("DROP EXTENSION IF EXISTS citext")))
	migration.Only(query.Postgres)
})
```

Raw SQL is not translated between dialects, so restrict such a migration with
`Only`; see [Dialect-specific migrations](migrations.md#dialect-specific-migrations).

For a set-based data change such as seeding fixed rows, use the query builder
so the statement compiles for every dialect:

```go
migrate.NewMigration("seed-roles", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Exec(query.Table("roles", func(statement *query.Builder) {
		statement.InsertRows(
			map[string]any{"name": "admin"},
			map[string]any{"name": "member"},
		)
	})))
	migration.Down(migrate.Exec(query.Table("roles", func(statement *query.Builder) {
		statement.WhereIn("name", "admin", "member")
		statement.Delete()
	})))
})
```

`Exec` compiles and runs each statement in turn, so if the second statement
fails to build or to run, the first has already run. A failure reads
`statement N: ...`. Inside a transaction the earlier statements are rolled back
with the rest of the migration. `migrate.Exec()` with no statements, or with a
nil statement, fails with `migrate.ErrInvalidMigration` when the step runs,
before it executes anything.

`Exec` likewise copies its statement slice immediately. Published queries and
raw statements retain their own input-capture rules; arbitrary SQL argument
objects and function captures are not recursively copied. A raw source may use
`query.RawNamed` as well as positional `query.Raw`; both compile for the database
dialect and retain the [raw scanner's limits](raw-sql-and-locks.md#placeholder-scanning).

## Function steps

When a change needs a per-dialect decision or a read before a write, use a
function step. It receives the same `ctx` and
`db` as the built-in steps; use those, not another database you hold, so your
queries join the migration's transaction. Return an error to fail the
migration.

`migrate.Schema` and `migrate.Exec` return ordinary `Func` values, so a function
step can call them. Do this when one migration must exist on every dialect but
behave differently on one of them:

```go
migrate.NewMigration("widen-users-name", func(migration *migrate.MigrationBuilder) {
	migration.Up(func(ctx context.Context, db *database.Database) error {
		if db.Dialect() == query.SQLite {
			return nil // SQLite strings have no length limit to widen.
		}

		return migrate.Schema(schema.Alter("users", func(table *schema.Table) {
			table.Change("name").String(255).NotNull()
		}))(ctx, db)
	})
})
```

Keep long backfills out of schema migrations when the table is large, or run
them in batches, since the migration holds the migration lock while it works.

## Combining steps

A migration may have several steps of different kinds. `Up(first, second,
third)` runs them in order and stops at the first failure, which it reports as
`step N: ...` around the step's own error.

The common case is adding a required column to a table that already has rows.
Add the column as nullable, fill it, then make it `NOT NULL`:

```go
migrate.NewMigration("add-users-locale", func(migration *migrate.MigrationBuilder) {
	migration.Up(
		migrate.Schema(schema.Alter("users", func(table *schema.Table) {
			table.String("locale", 10).Nullable()
		})),
		migrate.Exec(query.Table("users", func(statement *query.Builder) {
			statement.WhereNull("locale")
			statement.Update(map[string]any{"locale": "en"})
		})),
		migrate.Schema(schema.Alter("users", func(table *schema.Table) {
			table.Change("locale").String(10).NotNull()
		})),
	)
	migration.Down(migrate.Schema(schema.Alter("users", func(table *schema.Table) {
		table.DropColumn("locale")
	})))
})
```

The last step states the type as well as `NotNull()` because MySQL can only
change nullability by restating the column; PostgreSQL accepts either form.
SQLite cannot change a column in place, so this migration fails there with
`query.ErrUnsupported`; give the column a default when it is added instead, or
see the [schema builder](schema-builder.md) for SQLite's limits.

On PostgreSQL all three steps and the bookkeeping write run in one
transaction, so a failed backfill leaves the column unadded. On MySQL each step
is applied as it runs, so a failure leaves the earlier steps in place; see
[Failures and transactions](migrations.md#failures-and-transactions).

## Testing migrations

In your application migration package's test file, import the standard-library
`context` and `testing` packages, `arkoren.dev/driver/sqlite/sqlitedatabase`,
`arkoren.dev/core/service/database`, and `arkoren.dev/core/service/database/migrate`.
Use the application's `Migrations` function from [the definitions example](migrations.md#ordering-and-ids).
Apply those migrations to a fresh SQLite database, revert them, and apply them
again; this catches a missing
`Down`, a `Down` that does not undo its `Up`, and a migration that cannot run twice:

```go
func TestMigrations(t *testing.T) {
	driver, err := sqlitedatabase.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	db := database.New(driver)
	migrator := migrate.New(db)
	if err := migrator.Add(Migrations()...); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Rollback(t.Context()); err != nil { // reverts the whole batch
		t.Fatal(err)
	}
	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
}
```

A migration without a `Down` makes the `Rollback` in this test fail with
`migrate.ErrIrreversible`; for such a migration, test only `Up`.

SQLite runs the builder's SQLite statements, so this test proves that the
definitions are consistent, not that they work on PostgreSQL. A migration that
needs a PostgreSQL-only feature, or a change SQLite cannot make, belongs in
an [integration test](../testing/integration-tests.md) against the real
database. For the SQL a migration will run on a given dialect without any
database, build the statement with `BuildFor`, as
[Building without executing](schema-builder.md#building-without-executing)
shows.

In application tests that only need the tables to exist, run the same `Up`
on a fresh in-memory database in the test setup, rather than keeping a second
copy of the schema.

For every step helper and error, see [the `migrate` API reference](https://pkg.go.dev/arkoren.dev/core/service/database/migrate).
