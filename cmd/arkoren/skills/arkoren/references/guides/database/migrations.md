---
title: Migrations
description: Define schema changes as ordered migrations, run them at startup or from a command, roll them back, and understand locking and failures.
order: 50
---

# Migrations

## Introduction

A **migration** is a named schema or data change, such as adding a table or
backfilling a column. Keep definitions with your application so each environment
applies the same changes in order. A migration can optionally define its reverse.

The `migrate` package applies them. You describe each migration with the
[schema builder](schema-builder.md), add the migrations to a `Migrator` in the
order they should run, and call `Up`. The migrator remembers which migrations
already ran in a **bookkeeping table**, so running `Up` again only applies what
is new. It takes a database lock first to serialize competing migrators within
the database's [lock scope](#concurrent-instances). This does not make partial
or nontransactional work exactly-once; [failure recovery](#failures-and-transactions)
still matters.

## Defining a migration

A migration has a stable id, an `Up` that applies it, and an optional `Down`
that reverts it. `Up` and `Down` each take one or more **steps**, which run in
order. The smallest useful migration has one step on each side:

Use `migrate` from `arkoren.dev/core/service/database/migrate` and `schema` from
`arkoren.dev/foundation/query/schema`. Later fragments use the configured `db`
and operation context `ctx` from the [database overview](overview.md);
`query` is `arkoren.dev/foundation/query`:

```go
migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Schema(
		schema.Create("users", func(table *schema.Table) {
			table.ID()
			table.String("email", 255).Unique()
			table.Timestamps()
		}),
	))
	migration.Down(migrate.Schema(schema.Drop("users")))
})
```

`NewMigration` runs construction callbacks once, synchronously in order, without
executing steps. It publishes an immutable `Definition`; retained drafts cannot
change it, but functions and captures remain shared. Nil callbacks panic. Invalid
ids, repeated `Up`/`Down`, and empty or nil steps are retained for `Err()` and
rejected by `Add`; `Up` is required after construction. See the
[declaration contract](https://pkg.go.dev/arkoren.dev/core/service/database/migrate) for ownership details.

`Up`, `Down`, and `Only` copy their input slices when called, and publication
detaches the completed definition again. Replacing a caller's slice entries
cannot change the definition. Step functions and objects captured by their
closures remain shared; do not mutate those captures during migration execution.

`Down` is optional. A migration without it is **irreversible**: it can be
applied, but `Rollback` refuses to revert it. That is a legitimate choice for
a migration that destroys data, since a `Down` that recreates an empty column
only pretends to undo it.

To describe a migration for the people who read it, add `Description("...")`.
It is documentation only and is not stored.

## Steps

A step is a `migrate.Func`, `func(ctx context.Context, db *database.Database) error`.
Use `migrate.Schema` for table changes, `migrate.Exec` for SQL and set-based data
changes, or a function for application logic. `Up` and `Down` stop at the first
failed step. [Migration Steps](migration-steps.md) explains setup, checks, errors,
and combining steps.

## Dialect-specific migrations

Most migrations run on every dialect. When one makes sense on a single
database, such as a PostgreSQL-only table type, restrict it with `Only`:

```go
migrate.NewMigration("create-cache", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Exec(query.Raw("CREATE UNLOGGED TABLE cache (key text PRIMARY KEY, value text)")))
	migration.Down(migrate.Schema(schema.Drop("cache")))
	migration.Only(query.Postgres)
})
```

`Add` rejects the migration with `migrate.ErrInvalidMigration` when the
database uses another dialect, before any SQL runs. It never skips a migration
silently, because a schema that quietly differs between environments is a
worse failure than an error at startup. If one migration should behave
differently per database but exist on all of them, leave `Only` out and check
`db.Dialect()` inside a [function step](migration-steps.md#function-steps).

## Ordering and ids

The order in which you add migrations is the order in which they run. The list is
append-only: add new migrations at the end, and never reorder, edit the
`Up` of, or remove a migration that has run anywhere. To change what an applied
migration did, write a new migration.

Because order comes from the list, an id needs no number or date prefix. Name
a migration for the change it makes, in lowercase words joined by hyphens:
`create-users`, `add-users-email-index`, `backfill-users-name`. An id can have
up to 191 bytes of printable characters without whitespace, and ids are unique.
Packages write short ids and let their group prefix them; see
[Migrations from packages](#migrations-from-packages).

Renaming an applied id makes it pending again and leaves the old id missing.
Treat ids as permanent bookkeeping keys.

Go compares IDs byte-for-byte, without case folding or Unicode normalization.
MySQL's [canonical ledger layout](#mysql-ledger-compatibility) preserves that
identity in storage too; the 191-byte limit includes any group prefix.

Keep the migrations in one function and add them in one call, so the order is
visible in one place:

```go
func Migrations() []migrate.Definition {
	return []migrate.Definition{
		migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create("users", func(table *schema.Table) {
				table.ID()
				table.String("email", 255)
			})))
			migration.Down(migrate.Schema(schema.Drop("users")))
		}),
		migrate.NewMigration("add-users-name", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Alter("users", func(table *schema.Table) {
				table.String("name", 100).Nullable()
			})))
			migration.Down(migrate.Schema(schema.Alter("users", func(table *schema.Table) {
				table.DropColumn("name")
			})))
		}),
	}
}
```

## Running migrations

Create a migrator for a database and add the definitions:

```go
migrator := migrate.New(db)
err := migrator.Add(Migrations()...)
```

`Add` registers all the migrations or none. It fails for an invalid definition,
a duplicate id (`migrate.ErrDuplicateID`), or a migration restricted to
another dialect. `New` does no I/O, so nothing touches the database until you
run something.

To apply every pending migration, call `Up`:

```go
err := migrator.Up(ctx)
```

`Up` runs the pending migrations in order, each followed by its row in the
bookkeeping table. It stops at the first failure and leaves earlier committed
migrations recorded. After resolving the failure, another `Up` skips those
recorded migrations. The failed migration may have left unrecorded changes;
inspect partial or uncertain results before retrying, as
[Failures and transactions](#failures-and-transactions) explains. All the
migrations of one `Up` call share a **batch** number. If you supply an outer
transaction, its final outcome controls the joined work.

> [!WARNING]
> `Rollback` executes your `Down` steps, which can delete tables, columns, and
> their data. Review those steps and your recovery plan before running it;
> prefer a forward migration when production data must remain available.

To revert, call `Rollback`. By default it reverts the last batch, which is the
set of migrations the latest `Up` applied. Within each batch, rollback reverses the
registered `Add` order—not `ran_at` timestamps, which can tie or move backward.
Keep the original relative registration order of applied definitions, including
definitions supplied by packages.

```go
err := migrator.Rollback(ctx) // the last batch
```

To revert a fixed number instead, pass `migrate.Steps`. Batches are considered
newest first. This alternative reverts only the last migration; do not run it
after the preceding call:

```go
err := migrator.Rollback(ctx, migrate.Steps(1))
```

`Rollback` checks every target before it runs any of them. If a recorded
migration is not in the migrator, it fails with `migrate.ErrUnknownMigration`,
and if one has no `Down`, it fails with `migrate.ErrIrreversible`. Nothing is
reverted in either case.

An unknown definition sorts before known definitions within its batch. Even
`Steps(1)` cannot skip that ambiguity to revert a known member of the same
target batch. Re-register the original definitions before rolling it back;
`ran_at` is not a safe substitute for their application order.

To see where the database stands, call `Status`:

```go
statuses, err := migrator.Status(ctx)
for _, s := range statuses {
	fmt.Printf("%-24s ran=%t batch=%d\n", s.ID, s.Ran, s.Batch)
}
```

```text
create-users             ran=true batch=1
add-users-name           ran=true batch=1
create-audit             ran=false batch=0
```

`Status` returns the registered migrations in order, with when they ran and
whether they are out of order, followed by recorded migrations that the
migrator does not know (`Missing`). A missing migration usually means the
database is ahead of this version of the code, for example after a rollback
of a deploy.

> [!NOTE]
> `Up`, `Rollback`, and `Status` all take the migration lock and create the
> bookkeeping table if it is missing, so even `Status` writes to a fresh
> database.

## The bookkeeping table

The migrator records applied migrations in a table named `arkoren_migrations`,
which it creates on first use. It has three columns: the migration `id`, the
`batch` that applied it, and `ran_at`, the time in UTC.

If several applications share one database, give each its own table so their
migrations do not see each other, and give each its own lock name as well:

```go
migrator := migrate.New(db, func(migrator *migrate.Builder) {
	migrator.Table("billing_migrations")
	migrator.LockName("billing_migrations")
})
```

You should not edit the table by hand. If you must, for example to mark a
migration as applied that you ran manually, delete or insert the row of that id
and nothing else.

### MySQL ledger compatibility

New MySQL bookkeeping tables use `id VARBINARY(191) NOT NULL` with a full,
single-column `PRIMARY KEY(id)`. The migrator binds IDs as their original UTF-8
bytes for inserts and deletes, not as character strings subject to connection
charset conversion. Case, accents, and distinct Unicode normalization forms
remain distinct identities. Public IDs and group names are unchanged.

For each `Up`, `Rollback`, or `Status`, the migrator resolves the ledger under
its lock on the retained session. A qualified `Table("schema.history")` uses
those components; an unqualified name uses that session's current database.
Without a selected database, use a qualified name or select the database in
your connection configuration. The resolved schema/table is frozen for that
operation's inspection, history reads, inserts, and deletes. A step's `USE`
statement cannot redirect subsequent bookkeeping to a different database.

Before reading history or starting migration steps, it checks the actual
target with `SHOW FULL COLUMNS` and `SHOW INDEX`. Only the canonical ID column
and full single-column primary key are accepted, with no additional unique
indexes. Character columns are unsupported even with `_bin` or `NO PAD`
collations; this is a canonical-layout requirement, not a claim that every such
collation folds case. Fixed `BINARY`, nullable or generated IDs, and composite,
prefix, or expression primary keys are also unsupported.

A confirmed unsupported layout fails with `migrate.ErrInvalidMigration` before
any migration step callback or history-row mutation. Preparation may already
have issued `CREATE TABLE IF NOT EXISTS`; rejection does not mean no SQL ran.
SQL inspection failures retain their native cause for `errors.Is`/`errors.As`.
Hidden or unavailable metadata also stops the operation, without treating an
unverified layout as proof that conversion is needed. Ensure the migration
role can inspect the target's column and index metadata.

The migrator never automatically alters an existing ledger, renames or normalizes
IDs, or converts history. Its ledger preparation needs no `ALTER` privilege;
your migration steps may still need it for application tables. The checks do not
certify arbitrary table dependencies or prevent an administrator from changing
the table outside the migration lock.

#### Converting an existing MySQL ledger

> [!WARNING]
> A blind type or collation change can corrupt migration identity or make applied
> work run again. Conversion is an explicitly authorized operator action, not a
> migration step that can repair its own rejected ledger. Do not rename, trim,
> normalize, or delete IDs to make validation pass.

Before converting an existing ledger, establish and verify an application-specific
repair plan:

1. **Quiesce access.** Stop all migrators, ledger writers, and conflicting
   administrator DDL for the entire repair and verification window. The migration
   lock coordinates cooperating migrators, not arbitrary writers or DDL.
2. **Back up the schema and exact history.** Preserve column/index definitions
   and every `(HEX(id), batch, ran_at)` tuple, including full timestamp precision.
   Keep a recoverable backup and a restoration plan; rendered ID text alone is
   not a byte-preserving record.
3. **Prove the charset and identity mapping.** Compare stored `HEX(id)` values
   with the intended application IDs' UTF-8 bytes, including group prefixes.
   Verify every ID fits within 191 bytes using `OCTET_LENGTH`, not a character
   count, with no truncation or collisions. Non-UTF-8 legacy storage can require
   an owner decision: blindly changing the charset cannot guarantee preservation
   of both physical bytes and intended application identity. Resolve any mismatch
   explicitly before proceeding.
4. **Review dependencies and attributes.** Inspect primary and secondary keys,
   foreign keys, triggers, generated values, and column attributes. Plan for the
   canonical primary key and absence of additional unique indexes without
   silently removing dependencies or changing application semantics.
5. **Perform only the approved conversion.** Use strict SQL mode and a reviewed
   procedure that rejects truncation or lossy conversion. Inspect diagnostics;
   do not accept coercion warnings as success. Use separately authorized operator
   privileges rather than granting the runtime migrator automatic repair rights.
6. **Verify before resuming.** Reinspect the actual canonical column and index
   metadata. Compare every history tuple and the row count against the backup;
   for a byte-preserving conversion, `HEX(id)`, `batch`, and `ran_at` must be
   unchanged. Stop and investigate any difference rather than reconstructing
   history from migration registration order or the current clock.
7. **Reconcile earlier partial work.** Inspect the application schema and data
   for effects of previously failed nontransactional migrations. A valid ledger
   layout does not prove that an unrecorded migration did nothing. Resolve that
   state and approve the replay or recovery plan before allowing `Up` or
   `Rollback` to run again; see [failure recovery](#failures-and-transactions).
   Reconcile IDs with deployment records too: conversion cannot recover history
   already lost or conflated under the old equality rules.

There is no universal one-line `ALTER` recipe: the legacy encoding, history, and
dependencies determine whether a proposed conversion is safe. A successful
conversion does not make MySQL DDL transactional.

## At startup or as a command

There are two places to run `Up`, and the right one depends on how you deploy.

For a service with one instance, or for development, run it at startup,
after the database has connected. Start hooks run in the order you register
them, so register the migrator after the driver:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	composer.Startup(migrator.Up)
}
```

Pass `compose` to `app.Run(ctx, compose)`. Cleanup is active as soon as it is
registered, so migration failure closes the driver. A failed `Up` prevents
application startup; cleanup also runs if composition itself fails.

In production you should run migrations once per deploy, before the new
instances take traffic, from a command that exits when it is done. A Kubernetes
Job, an init container, or a release step in your pipeline all work, and the
command can share your `Migrations` function. After opening and starting the
database driver, register the migrations on its facade:

```go
migrator := migrate.New(db)
err := migrator.Add(Migrations()...)
```

For an apply command, call `migrator.Up(ctx)`; for a rollback command, call
`migrator.Rollback(ctx)`. Return a nonzero exit status if registration or execution
fails, and stop the driver before exiting. Argument parsing and database setup
belong to your command, not the migration definitions.

Running migrations from every instance's start hook is safe, since the lock
serializes them, but it makes every instance wait for the migration, ties a
migration failure to the health of the instance, and gives the application
database credentials that can alter the schema. A separate command can use a
more privileged role than the running service.

## Concurrent instances

`Up`, `Rollback`, and `Status` hold a database lock while they work. If two
migrators start at the same time, one waits until the other finishes, then
sees the migrations as applied and has nothing left to do. The lock name is
`arkoren_migrations` unless you change it with `migrate.Builder.LockName`.

The successful operation retains one physical connection for both the lock and
all protected work: bookkeeping-table preparation, history reads, planning,
each migration's transaction and statements, and bookkeeping writes. It acquires
the lock before preparing the table or reading history. If that session dies,
subsequent SQL fails instead of continuing the old plan on a new connection.
The operation never reconnects or replays migration callbacks after acquiring
the lock.

While waiting for contention, each attempt borrows a session and tries the lock
once. A refused attempt ends its callback and releases any newly acquired pool
connection before waiting to try again. Cleanup failures stop the operation;
they are not treated as another refused attempt.

How far the lock reaches depends on the database:

- **PostgreSQL** uses session advisory locks, which span every process and
  instance connected to the database. If the holder crashes, its connection
  drops and the lock is released.
- **MySQL** uses `GET_LOCK`, which spans every instance connected to the
  server and also ends with the session.
- **SQLite** has an in-process lock and, for a database file on Linux, macOS,
  and the BSDs, an operating system file lock that covers other processes on
  the same host. In-memory databases and other platforms get process-local
  coordination only. The driver does not detect network filesystems; do not
  rely on its file locks for cross-process coordination there.

> [!WARNING]
> PgBouncer in transaction pooling mode breaks PostgreSQL session advisory
> locks: the lock and its release can run on different server connections, so
> the lock is not held or is never released. Run migrations over a direct
> connection or a session-pooled one.

The acquisition wait is bounded, including time waiting for a pool connection.
By default, a migrator waits five minutes, and `migrate.Builder.LockTimeout` changes that;
zero keeps the default and a negative value waits until the context ends.
The acquisition timer stops when the lock is acquired, before table preparation,
history reads, or migration work. Your caller context still controls cancellation
throughout. When acquisition times out, the error wraps both `migrate.ErrLockTimeout` and
`context.DeadlineExceeded`:

```go
migrator := migrate.New(db, func(migrator *migrate.Builder) {
	migrator.LockTimeout(30 * time.Second)
})
```

If your own context ends first, the error wraps its cancellation or deadline
without `migrate.ErrLockTimeout`. Unlock uses a separate, bounded cleanup context
even after cancellation. Cleanup errors are joined with the original failure,
and a callback panic propagates after cleanup.

Pass an ordinary deployment context to the migrator. If you pass an existing
database session or [transaction](transactions.md), the migrator borrows it
instead of escaping to the pool. That caller-owned connection stays reserved
even between refused lock attempts, and an outer transaction controls final
commit and lock release. `WithoutTransaction` cannot escape an already active
transaction.

> [!WARNING]
> Every step must pass its supplied context to facade calls. Using a fresh
> context or `db.Driver()` bypasses the pinned session and can deadlock when
> the pool has only one connection. Keep step work on that context, close
> streaming cursors before further statements, and do not let callbacks retain
> contexts or use them concurrently.

An active operation needs one retained pool connection, not a separate connection
for each migration transaction. Other root-level held locks still reserve pool
capacity. Named locks are nonreentrant: do not acquire the migration's lock
yourself and then call the migrator, even on the same session.

## Failures and transactions

When the database supports transactional DDL, the migrator runs each migration
in a transaction together with its row in the bookkeeping table. If any step
fails, that migration's schema and bookkeeping changes roll back. Earlier
commits and external effects are not undone. This is how PostgreSQL and SQLite
handle the migrator's database work.

Each migration has its own transaction; the complete `Up` or `Rollback` operation
does not. Earlier commits remain applied when a later migration fails. The
lock-owning session spans those boundaries without changing partial-progress
semantics. If you supplied an outer transaction, its final outcome controls all
the joined work instead.

MySQL cannot do this: it commits implicitly around most DDL statements, so
a transaction cannot undo a `CREATE TABLE`. The migrator therefore runs
migrations on MySQL without a transaction. If a migration with two steps fails
on the second, the first has taken effect and is not recorded, and the next
`Up` runs the migration again from its first step.

The same applies to a migration marked `WithoutTransaction`, which you need
for statements that cannot run in a transaction, such as PostgreSQL's
`CREATE INDEX CONCURRENTLY`. Write such a migration so that running it twice
is harmless, with `schema.CreateIfNotExists` and `schema.DropIfExists`, and keep it small:

```go
migrate.NewMigration("create-audit", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Schema(
		schema.CreateIfNotExists("audit", func(table *schema.Table) {
			table.ID()
			table.String("action", 64)
		}),
	))
	migration.Down(migrate.Schema(schema.DropIfExists("audit")))
	migration.WithoutTransaction()
})
```

Existence guards do not verify that an existing table has the expected columns,
or resume a half-completed backfill. Inspect partial state before retrying. In
particular, PostgreSQL `CREATE INDEX CONCURRENTLY` can leave an invalid index;
`IF NOT EXISTS` alone is not a repair for it. MySQL cannot compile
`CreateIfNotExists` when the declaration also creates indexes, so use an explicit
recovery plan for those migrations rather than relying on that guard.

Because MySQL runs without a transaction, put one change in each migration
there, so a failure leaves at most one change half done. In every case, the
error names the migration and the step, as in
`migrate up two-steps: step 2: boom`, and wraps the error the step returned, so
`errors.Is` finds it.

Session pinning prevents work from continuing after losing the lock-owning
connection, but cannot undo already committed or nontransactional effects.
After a connection or commit failure with uncertain results, stop automatic
retries, compare the schema with the bookkeeping rows, and choose an explicit
recovery before running migrations again.

A migration that commits its schema and bookkeeping can still return an error
from transaction cleanup or an after-commit hook. In that case it publishes
`migrate.Applied` or `migrate.RolledBack` with a nil event error and stops before
the next migration; it does not publish `migrate.Failed` for the completed work.
A step that itself returns `database.ErrAfterCommit` still fails; a transactional
migration rolls back. Do not infer completion solely from that sentinel. An error
during final session cleanup does not change events for migrations that already completed.
When you supply an outer transaction, migration success events remain provisional
until that transaction commits.

A step that opens its own `db.WithTransaction` inside a transactional migration
starts a nested transaction, which uses a savepoint. That is fine on
PostgreSQL and SQLite.

## Out-of-order migrations

Sometimes a pending migration sits before one that already ran. This happens
when two branches each add a migration and merge in the opposite order from the
one in which they were deployed. By default, `Up` logs a warning and runs the
late migration anyway. You can choose a stricter or a quieter policy:

```go
migrator := migrate.New(db, func(migrator *migrate.Builder) {
	migrator.OutOfOrder(migrate.OutOfOrderError)
})
```

`OutOfOrderError` fails with `migrate.ErrOutOfOrder` before any migration
runs, which is the safest choice for production, and `OutOfOrderAllow` runs the
late migration without a warning. `Status` marks late migrations with
`OutOfOrder`.

The empty policy selects `OutOfOrderWarn`. Unknown nonempty values fail with
`migrate.ErrInvalidMigration` when `Up`, `Rollback`, or `Status` starts, before
it acquires a session or prepares the bookkeeping table; misspellings do not
silently enable a permissive policy.

## Migrations from packages

A package that needs its own tables should not register anything on its own,
and it should not create tables when it starts. It exports a function that
returns its definitions, wrapped in a **migration group**, and the application
adds them to its migrator. A group gives the package its own sequence, so the
package can append migrations in a later release without disturbing the
application's.

The package writes short ids. The group prefixes each one with its name and a
slash, so `create-users` in the group `acme.users` is stored, reported by
`Status`, and published in events as `acme.users/create-users`:

```go
func Migrations() []migrate.Definition {
	return migrate.NewGroup("acme.users").Migrations(
		migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create("acme_users", func(table *schema.Table) {
				table.ID()
			})))
			migration.Down(migrate.Schema(schema.Drop("acme_users")))
		}),
	)
}
```

Do not prefix ids yourself. A short id that contains a slash is invalid, so a
migration that still carries a hand-written `acme.users/` prefix fails at `Add`
with `migrate.ErrInvalidMigration`, and so does an id that is longer than 191
bytes once prefixed. Migrations outside a group may contain slashes.

The application adds the groups where it chooses, a package's migrations
before the application's own that refer to its tables:

```go
err := migrator.Add(append(users.Migrations(), Migrations()...)...)
```

A group is its own sequence for out-of-order detection. When a new release of
the package appends a migration, it is pending but not out of order, even
though the application's migrations ran after the package's earlier ones.

### Arkoren's own packages

Any group name that is a valid id works for your packages. Arkoren's own
packages that ship migrations use `arkoren.dev:<module>:<package>`, for
example `arkoren.dev:driver/vendor:vendorsearch` for a `vendorsearch` package in
a `driver/vendor` module (an illustrative name), so they never collide
with yours. A driver or package that can run several times against one
database, such as two search indexes, exposes `Builder.Instance(name)` and adds the
name to the group: `arkoren.dev:driver/vendor:vendorsearch:products`.

Arkoren drivers never create tables at runtime. A driver that needs tables
exposes `Migrations()`, and your application must add it to its migrator before
using the driver. Each shipped migration creates one table strictly, without
`IF NOT EXISTS`, so an unexpected leftover table fails rather than being
silently accepted:

```go
err := migrator.Add(driver.Migrations()...)
```

When the driver starts without its tables, its readiness check reports the
contract's `ErrNotFound` error and names `Migrations()`, instead of creating
them.

With PostgreSQL search, import
`arkoren.dev/driver/postgres/postgressearch`, give each instance its own name,
and add its migrations before starting the search drivers:

```go
products, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
	engine.Instance("products")
})
articles, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
	engine.Instance("articles")
})

err = migrator.Add(append(products.Migrations(), articles.Migrations()...)...)
```

If you omit `Builder.Instance` on both instances, both use the default group
and the same ids, and `Add` fails with `migrate.ErrDuplicateID`.

An instance name has 1 to 40 bytes of lowercase letters, digits, and
underscores; the default is the empty name. The tables follow the instance:
`<table>` for the default instance and `<instance>_<table>` for any other, such
as `products_search_documents` for PostgreSQL search. Table names are not
configurable. Renaming an instance
is unsupported: its group, its migration ids, and its tables are all derived
from the name, so the old ones stay applied under the old name. To move data
to a new name, create the new instance and copy the rows yourself.

## Rolling deploys

During a rolling deploy, old and new versions of the code run against the same
schema for a while. A migration therefore has to be safe for the version that
is still running. The usual way is to expand and then contract:

1. Release a migration that only adds: tables, nullable columns, indexes.
   Old code ignores them.
2. Deploy the code that uses the new schema.
3. In a later release, remove what nothing uses anymore.

Never rename or drop something in the same release that stops using it. A
rename is an add, a backfill, a code change, and a later drop.

## Logging and events

The migrator logs each applied and reverted migration through `slog` at the
info level, with the id and the duration, and warnings for out-of-order
migrations. Set a logger with `migrate.Builder.Logger`.

Facade SQL text telemetry is off by default. If you explicitly enable
`database.Builder.QueryText(true)` on the migrator's facade, schema defaults,
raw literals, comments, and backfill SQL are exported verbatim. Audit those
statements before opting in; see [query-text privacy](query-monitoring.md#what-is-recorded-and-what-is-not).

To react to migrations in code, give the migrator the application's event bus
with `migrate.Builder.Events`. It publishes `migrate.Applied`, `migrate.RolledBack`,
and `migrate.Failed`, each carrying the id, group, batch, duration, and the
error for a failure. [Events and telemetry](query-monitoring.md#migration-events)
shows how to listen.

## Testing migrations

Apply, revert, and reapply your definitions against SQLite in memory; use the
real database for dialect-specific changes. [Testing migration steps](migration-steps.md#testing-migrations)
provides the complete test and explains irreversible migrations and dialect limits.

For every function and option, see [the `migrate` API reference](https://pkg.go.dev/arkoren.dev/core/service/database/migrate).
