# arkoren.dev/core/service/database/migrate

Ordered schema migrations for `arkoren.dev/core/service/database`, recorded in a
bookkeeping table (`arkoren_migrations` by default).

When a feature needs a new table or a backfill, record the change as a migration
so every database applies it in the same order. The example below registers a
table change with an existing `db` database.

Install `arkoren.dev/core` with `go get arkoren.dev/core`. Import
`arkoren.dev/core/service/database/migrate` for the migrator,
`arkoren.dev/foundation/query/schema` for schema operations, and
`arkoren.dev/foundation/query` for SQL fragments. Here `ctx` is your deployment
context; focused snippets expose error results, which your caller must check
before continuing.

Declare migrations with `NewMigration(id, configure...)`. Each callback edits
the same `*MigrationBuilder` once, synchronously in order, and returns no value.
Settings return that draft. The published `Definition` is immutable: owned step
and dialect slices are copied on input and publication, so retained drafts do
not change it. Step functions and their captures remain shared, and are not run
during construction. Nil callback entries panic; expanded nil or empty slices
make no edits. Application panics propagate. Construction retains the first
domain error; missing `Up` is checked only after all callbacks, and `Add`
rejects invalid definitions before SQL runs.
`Exec` and `Schema` also copy their statement and operation slices immediately,
without running migration work. Replacing an input slice entry cannot change
the published step; arbitrary statement arguments still retain their own
ownership rules.

```go
migrator := migrate.New(db, func(migrator *migrate.Builder) {
	migrator.OutOfOrder(migrate.OutOfOrderError)
})

err := migrator.Add(
	migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Schema(schema.Create("users", func(table *schema.Table) {
			table.ID()
			table.String("email", 255).Unique()
		})))
		migration.Down(migrate.Schema(schema.Drop("users")))
	}),
)
err = migrator.Up(ctx) // apply pending migrations as one batch
statuses, err := migrator.Status(ctx)
```

To undo an applied change, use `Rollback`. These calls are alternatives: the
first reverts the last batch, while the second reverts the last two migrations.
Down steps may delete tables and their data; review them before rollback.

```go
err := migrator.Rollback(ctx)
err = migrator.Rollback(ctx, migrate.Steps(2))
```

Rollback selects the newest batch first and reverses Add order within each
batch, even if timestamps tie or the wall clock moves backward. Preserve the
relative Add order of applied migrations. An unknown recorded id takes
precedence over known ids in its batch and blocks rollback; restore its original
definition before retrying.

- Add order is run order. The list is append-only.
- An unrecognized nonempty `OutOfOrder` label makes `Up`, `Rollback`, and
  `Status` return `ErrInvalidMigration` before session acquisition or SQL.
  An empty label still selects the default warning policy.
- Ids need no number or date prefix: order comes from Add order. Name the
  change (`create-users`, `add-users-email-index`). A package writes short ids
  and its group prefixes them (`acme.users/create-users`).
- Ids are stable. Renaming an applied migration runs it again.
- MySQL ledger IDs use `VARBINARY(191) NOT NULL` with a full, single-column
primary key. Existing unsupported layouts are rejected before migration
  callbacks; the migrator never converts them automatically. See below before
  upgrading an existing MySQL ledger.
- Migrations run in a transaction on PostgreSQL and SQLite. MySQL runs without
  one. `WithoutTransaction` migrations must be idempotent.
- A step runs with the migration's transaction in its context. A
  `WithTransaction` inside a step is a nested transaction.
- Every operation retains one physical session for its lock, planning,
  bookkeeping, and migration work. Pass the supplied context to every facade
  operation; root-driver calls or fresh contexts bypass protection and can
  deadlock a one-connection pool. Session loss stops work without replay.
- A lock serializes concurrent migrators (see below).
- Packages export `Migrations() []migrate.Definition`, wrapped in
  `migrate.NewGroup(name).Migrations(...)`, which prefixes each id with the group
  name and a slash (a short id containing `/` is `ErrInvalidMigration`, as is a
  combined id over 191 bytes); nothing self-registers. Out-of-order
  detection applies within each group, so a package release that appends a
  migration never counts as out of order against the application's own.

## Dialects

A migration runs on every dialect unless `Only` restricts it. `Add` returns
`migrate.ErrInvalidMigration` when the database uses another dialect; it never
skips silently. For logic that differs per dialect, switch on `db.Dialect()`
inside a migration step (`migrate.Func`) instead.

```go
migrate.NewMigration("create-cache", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Exec(query.Raw("CREATE UNLOGGED TABLE cache (id INTEGER)")))
	migration.Down(migrate.Schema(schema.Drop("cache")))
	migration.Only(query.Postgres)
})
```

### Existing MySQL ledgers

`Up`, `Rollback`, and `Status` verify the actual `id` column and primary key with
`SHOW FULL COLUMNS` and `SHOW INDEX` on the lock-owning session. This happens
before history reads, any Up/Down callback, or ledger-row mutation. A confirmed
unsupported layout returns `ErrInvalidMigration`, names its actual metadata,
and requests explicit operator review. No migration event represents an
unstarted step. Missing/hidden metadata and SQL inspection errors fail closed;
SQL errors preserve their original cause and do not prescribe a conversion.

New ledgers are created with binary ID storage. `CREATE TABLE IF NOT EXISTS`
does **not** alter an existing ledger. Character columns, even those using a
`_bin` or `NO PAD` collation, are not accepted as the canonical layout. Fixed
`BINARY` is not accepted either because it pads values. Other columns and the
configured table/group/ID identities remain unchanged.
Additional unique indexes are rejected because they can impose narrower
prefix/expression equality even when the primary key itself is byte-exact;
nonunique secondary indexes do not change that uniqueness invariant.

An unqualified MySQL table is resolved against the retained session's current
database once. Qualified names are quoted component by component. All metadata,
history, inserts, and deletes then use that frozen target, even if a callback
changes the current database. IDs bind as their original UTF-8 bytes only at
the MySQL ledger seam; they are not normalized, hashed, or renamed.

**Do not run a blanket ALTER or charset conversion to bypass the guard.**
Before an operator explicitly converts an existing ledger:

1. Stop all migrators, ledger writers, and conflicting DDL. Back up the schema
   and every `(HEX(id), batch, ran_at)` tuple, including unregistered history.
   The migration lock coordinates cooperating migrators, not arbitrary admin DDL.
2. Prove every stored ID's bytes equal the intended Go UTF-8 identity, satisfy
   the unchanged 1–191-byte printable/no-whitespace definition, and fit 191 bytes
   without truncation. Do not infer byte identity from the charset label alone.
3. Review non-UTF-8 data explicitly. For example, a latin1 character can have
   different stored bytes from its Go UTF-8 identity. If preserving stored
   bytes and preserving application identity conflict, stop for an owner
   decision; the library will not silently transcode or reinterpret history.
4. Inspect indexes, foreign keys, triggers, generated expressions, and required
   column attributes. Use strict SQL mode. Change only the approved ID storage
   and preserve the complete primary key and all other history fields.
5. Reconcile previously failed nontransactional migrations against the actual
   application schema. Their steps might already have committed despite a
   failed ledger insert; conversion does not make replay safe or mark them done.
6. Verify the canonical column/index metadata and exact equality of all saved
   history tuples before resuming. Keep table names, group prefixes and ID bytes.

The migration role needs inspection access but no new `ALTER` privilege.
Operator conversion is separate from migration execution. Native charset,
permission and metadata behavior must be verified against your supported MySQL
version; a fake SQL protocol test is not backend evidence.

## Horizontal scaling

`Up`, `Rollback`, and `Status` hold the lock named by `Builder.LockName` while they work.

| Dialect    | Lock                                                        | Scope                                   |
| ---------- | ----------------------------------------------------------- | --------------------------------------- |
| PostgreSQL | session advisory lock                                       | every instance on the database          |
| MySQL      | `GET_LOCK`                                                  | every instance on the server            |
| SQLite     | in-process lock, plus `flock` on `<dbfile>.<hash>.lock`     | one host; Linux, macOS, and BSDs only   |

SQLite in memory, on other operating systems, or on a network file system has
no cross-process lock, and is not a horizontal-scaling database.

**PgBouncer.** Transaction pooling breaks session advisory locks, because the
lock and unlock can reach different server connections. Run migrations over a
direct connection or a session-pooled one.

**Lock timeout.** `Builder.LockTimeout(d)` bounds the wait for the lock,
including pool acquisition, not the migrations. Zero uses 5 minutes; a negative value waits until the
context ends. A timeout fails with an error that wraps `migrate.ErrLockTimeout`
and `context.DeadlineExceeded`.

When the caller context ends first, the error preserves that context's cause
without `ErrLockTimeout`. Cleanup has its own bounded context. A confirmed
migration commit followed by cleanup failure keeps its success event, returns
the failure, and stops before the next migration; inspect status before retrying.

## Idempotent migrations

A migration without a transaction (`WithoutTransaction`, and every MySQL
migration) may stop half way and run again. Make each step safe to repeat, and
prefer `schema.CreateIfNotExists` and `schema.DropIfExists`:

```go
migrate.NewMigration("create-audit", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Schema(schema.CreateIfNotExists("audit", func(table *schema.Table) {
		table.ID()
		table.String("action", 64)
	})))
	migration.Down(migrate.Schema(schema.DropIfExists("audit")))
	migration.WithoutTransaction()
})
```

## Rolling deploys

Old and new code run together during a rolling deploy. Use expand and
contract: release additive migrations first (new tables, nullable columns,
indexes), deploy code that uses them, and remove unused schema in a later
release. Do not rename or drop in the release that stops using the old name.

In production, run migrations as a Job, an init container, or a dedicated
command that exits when done, not from `compose.Startup`. With startup migrations,
every instance competes for the lock and a failed migration takes instances down.
Registering migrations with `compose.Startup` is suitable for development and
single instances.

See the [migration guide](../../../../cmd/arkoren/skills/arkoren/references/guides/database/migrations.md)
and [API reference](https://pkg.go.dev/arkoren.dev/core/service/database/migrate).
