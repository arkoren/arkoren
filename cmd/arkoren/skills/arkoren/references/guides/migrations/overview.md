---
title: Overview
description: Define, register, apply, inspect, roll back, and test schema or data changes.
summary:
  - Define ordered schema and data steps with stable migration identities.
  - Register changes, apply pending work, and inspect recorded history.
  - Plan reversible changes and package independent migration sequences.
order: 10
---

# Overview

A migration is a named, ordered change to your database schema or data. The
migrator executes its steps and records completion in a bookkeeping table, so
later runs can identify pending work. Your application owns registration and
execution; constructing a database driver does not run migrations.

Assume an already configured `db` from [Database Drivers](../database/drivers.md).
Import `arkoren.dev/core/service/database/migrate`,
`arkoren.dev/foundation/query/schema`, and `arkoren.dev/foundation/query` where used.

## Defining Changes

To introduce a `users` table, first describe the table:

```go
users := schema.Create("users", func(table *schema.Table) {
	table.ID()
	table.String("email", 255).Unique()
})
```

This prepares a [schema operation](schema-builder.md), not a database call.
Attach it to a stable migration identity:

```go
createUsers := migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Schema(users))
	migration.Down(migrate.Schema(schema.Drop("users")))
})
```

`Up` is required and contains one or more steps. `Down` is optional; omitting
it marks the migration irreversible. A drop reverses this schema addition but
destroys its data, so do not equate reversible schema with recoverable data.
Definition construction is synchronous and performs no SQL.

An ID is 1–191 printable UTF-8 bytes with no whitespace. Prefer a short change
name such as `add-users-email-index`; numeric or date prefixes do not control
execution order. Never rename a recorded ID: the new name becomes pending and
the old name becomes missing. Keep definitions append-only and preserve their
relative order after application. Do not edit an applied Up definition to change
existing databases: add a new migration instead.

## Migration Steps

Use `migrate.Schema` for schema operations and `migrate.Exec` for SQL statements.
Both accept several arguments and run them in written order. For example, a
data change can normalize existing values:

```go
normalizeEmails := migrate.NewMigration("normalize-user-emails", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Exec(query.Raw("UPDATE users SET email = LOWER(email)")))
})
```

This irreversible example assumes case normalization is an approved application
rule and existing uniqueness constraints permit the result. Test on representative
data before deployment; a SQL expression is not a substitute for deciding data policy.

For logic that needs the current dialect or several facade calls, use a function
step with imports `context` and `arkoren.dev/core/service/database`:

```go
step := func(ctx context.Context, db *database.Database) error {
	_, err := db.Exec(ctx, query.Raw("UPDATE users SET email = LOWER(email)"))
	return err
}
```

Pass `step` to `migration.Up(step)` instead of `migrate.Exec`. Always forward the
supplied context: it carries the migration's retained session and any active
transaction. A fresh context or direct `db.Driver()` call bypasses the lock-owning
connection and can deadlock a one-connection pool. Do not retain the context or
use it concurrently. Close streaming cursors before later statements or boundaries.

`Schema` and `Exec` report operation or statement positions on failure. Empty
step helpers and nil statements are detected when the step runs, not by `Add`.
Schema operation construction errors are checked before that helper executes any
of its operations; dialect-specific failures occur during compilation.

Use `migration.Only(query.Postgres)` only when the entire migration is intended
for PostgreSQL. `Add` rejects it on another dialect rather than silently skipping
it. For a change with different implementations per dialect, leave it unrestricted
and switch on `db.Dialect()` inside a function step.

## Applying Changes

Create a migrator and register definitions in dependency order:

```go
migrator := migrate.New(db)
err := migrator.Add(createUsers, normalizeEmails)
```

`New` performs no I/O. `Add` registers all supplied definitions or none; invalid
definitions and duplicate IDs return `migrate.ErrInvalidMigration` and
`migrate.ErrDuplicateID`. After handling registration errors, apply pending work:

```go
err := migrator.Up(ctx)
```

`Up` takes a named lock, prepares the bookkeeping table, reads history, and runs
pending definitions in `Add` order. The default table and lock name are both
`arkoren_migrations`. A successful run records completed migrations in one new
batch; another run skips recorded IDs. Each migration commits independently
when transactional DDL is supported. There is no operation-wide rollback:
earlier commits survive a later failure. Read [Deployment](deployment.md) before
using this in production or running nontransactional changes.

## Inspecting History

To compare registered definitions with recorded completion:

```go
statuses, err := migrator.Status(ctx)
```

The result lists registered definitions in `Add` order, then recorded but
unregistered IDs with `Missing`. Each entry includes `ID`, `Group`, `Ran`,
`Batch`, `RanAt`, and `OutOfOrder`. Status takes the same lock and creates the
ledger on a fresh database: it is not a read-only connection probe or a dry run.
An acquisition, session, inspection, or cleanup failure returns nil rather than
an unprotected partial snapshot.

## Reverting Changes

Before rollback, review the selected `Down` steps, take an appropriate backup,
and ensure running code tolerates the previous schema. To revert only the latest
applied migration:

```go
err := migrator.Rollback(ctx, migrate.Steps(1))
```

Without `Steps`, rollback selects the last batch. It processes newest batches
first and reverse `Add` order within a batch, not timestamp order. It validates
all targets before executing any: missing definitions and irreversible targets
fail with `ErrUnknownMigration` and `ErrIrreversible`. An unknown definition takes
precedence over known targets in its batch, including with `Steps(1)`; restore
the definitions rather than guessing which known member to revert. Preserve the original
relative `Add` order so rollback can reconstruct application order. Earlier
successful Down commits remain final if a later Down fails.

## Package Migrations

A reusable package can return definitions in its own group:

```go
definitions := migrate.NewGroup("acme.users").Migrations(createUsers)
err := migrator.Add(definitions...)
```

The group prefixes short IDs, producing `acme.users/create-users`. Grouped short
IDs cannot contain `/`, and the complete ID must still fit 191 bytes. Definitions
already assigned to a group cannot be grouped again. Each group is an independent
append-only sequence for out-of-order checks, while overall `Add` order remains
run order. Add package migrations before application changes that use their tables.

Test definitions against each supported real database engine: apply to an empty
database, rerun to check recorded work is skipped, and verify schema and data.
If Down exists, test its effects separately. Also exercise failure halfway through
nontransactional work; a successful clean install does not establish safe recovery.

For definitions, steps, and execution, see the
[`migrate` API reference](https://pkg.go.dev/arkoren.dev/core/service/database/migrate).
