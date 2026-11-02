---
title: Deployment
description: Ship schema changes safely through rolling deployments, concurrent runners, and failure recovery.
summary:
  - Run registered migrations as a controlled deployment task.
  - Keep lock ownership and SQL on one retained physical session.
  - Plan nontransactional recovery and compatible rolling schema changes.
order: 30
---

# Deployment

Deploy schema changes as a dedicated job or command before new instances take
traffic. That runner constructs the database, registers the approved definitions,
calls `Up`, and exits on error. Keep a backup and a recovery plan appropriate to
the change; migration history is a completion ledger, not a backup.

This page assumes [definitions and registration](overview.md) are already in
place. Use an ordinary deployment context, not one carrying an application
transaction. The context's deadline must cover the work, not only lock acquisition.

## Controlled Runs

For a production runner, reject unexpected out-of-order changes and bound the
wait for capacity and the migration lock. Import `time` and
`arkoren.dev/core/service/database/migrate`:

```go
migrator := migrate.New(db, func(runner *migrate.Builder) {
	runner.OutOfOrder(migrate.OutOfOrderError)
	runner.LockTimeout(time.Minute)
})
```

Register definitions in their stable dependency order, check `Add` errors, then
run `migrator.Up(ctx)`. Success means the pending migrations completed and their
history was recorded under that operation's scope. On failure, stop deployment
and [inspect the outcome](troubleshooting.md#partial-changes) rather than rerunning
automatically. Keep old code compatible with the intermediate schema.

`OutOfOrderError` refuses pending definitions preceding applied ones in the same
group before any migration runs. The default, `OutOfOrderWarn`, logs and runs
them; `OutOfOrderAllow` runs without warning. Groups are independent sequences,
so a package appending a new migration does not become late merely because
application migrations were registered after the package.

Running `Up` in every instance's startup is suitable for development and simple
single-instance deployments, but production replicas would compete for the lock,
delay startup, and tie migration failures to instance health. A dedicated runner
keeps that work separate. The lock does not make an incompatible schema change
safe for already-running code.

## Lock Ownership

`Up`, `Rollback`, and `Status` all acquire the configured named lock. The default
table and lock name are `arkoren_migrations`; if you customize them, keep all
runners for the same history on the same target and lock. Different lock names
do not serialize one another.

Each acquisition attempt reserves a session and calls `TryLock` once. On
contention, a newly acquired session ends before the runner waits and retries,
returning capacity to the pool. After acquisition, preparation, history reads,
planning, steps, transactions, and bookkeeping stay on one physical connection.
Connection loss fails the operation without reconnecting or replaying its plan.

The default lock budget is five minutes. Zero selects that default; negative
`LockTimeout` waits until the caller's context ends. This budget covers pool/session
acquisition and lock contention, then stops when the lock is acquired. It does
not limit DDL or data changes. Budget expiry matches both `migrate.ErrLockTimeout`
and `context.DeadlineExceeded`; a caller deadline that ends first is returned
without the lock-timeout marker.

Cleanup releases locks with a bounded context independent of caller cancellation.
Cleanup errors join the operation error. Panics are re-raised after cleanup.
A lost or uncertain session is not silently pooled as a healthy replacement.

> [!WARNING]
> Every step must forward its supplied context to facade calls. A fresh context,
> `db.Driver()`, or a native root pool call bypasses the retained connection and
> lock scope, and can deadlock a one-connection pool. Close streaming rows before
> another statement or a transaction boundary. Do not separately acquire the
> migration lock: managed locks are not reentrant.

PostgreSQL requires a direct or session-pooled connection, not PgBouncer
transaction pooling. MySQL locks span a server; SQLite cross-process locking is
limited to supported same-host file-backed profiles. See
[provider scope](../database/locking.md#retained-sessions). A caller-owned session
or transaction remains reserved during failed acquisition attempts, so prefer
an ordinary context for deployments. Other held root locks can also consume
all pool capacity.

## Commit Boundaries

PostgreSQL and SQLite normally run each migration's steps and bookkeeping in
one transaction. MySQL migrations run without that transaction because DDL can
implicitly commit. `WithoutTransaction()` also disables the per-migration
transaction on transactional-DDL dialects. Use it when a database statement
cannot run in a transaction, such as PostgreSQL `CREATE INDEX CONCURRENTLY`.
That statement can leave an invalid index after failure; `IF NOT EXISTS` alone
does not repair it.

There is no transaction around the complete `Up` or `Rollback` operation.
Earlier migration commits survive a later failure. Without a per-migration
transaction, earlier statements can survive even when the current migration
has no ledger record. Such work must be safe to repeat after deliberate recovery.
`CreateIfNotExists` or `DropIfExists` can help, but object existence alone does
not establish that a partially applied object has the correct shape.
On MySQL, `CreateIfNotExists` cannot compile when the declaration also creates
indexes, since the generated index statements have no IF NOT EXISTS support.
Keep nontransactional migrations small and give each change a specific recovery plan.

Do not pass a preexisting transaction to a deployment runner: the migrator joins
it, making final commit and lock release depend on that outer transaction.
`WithoutTransaction` cannot escape a transaction the caller already started.
An explicit nested transaction inside a transactional step creates a savepoint
or fails with `database.ErrNestedTransaction` for a driver without nesting.

A confirmed migration commit followed by cleanup or an after-commit hook failure
still leaves the change and ledger committed. Inspect history before taking a
recovery action; the returned error is not proof the migration was undone.

## Rolling Deploys

Old and new instances can run against the same schema during a deployment.
Use expand and contract to keep both working:

1. Add compatible structures, such as a nullable column or a new table, without
   removing old names. Assess index creation and table-alter locking separately.
2. Deploy code that tolerates the expanded schema and, where needed, supports
   the transition between old and new data representations.
3. Backfill and verify data with restartable, bounded work appropriate to its size.
4. After old instances and dependent jobs are gone, enforce new constraints or
   remove unused structures in a later release.

Do not rename or drop a column in the same release that stops using its old
name. Test migrations on the target engine with representative data and measure
lock duration. A valid schema operation does not guarantee an online change.

## Migration Events

To attach in-process deployment observations, provide an existing event bus:

```go
migrator := migrate.New(db, func(runner *migrate.Builder) {
	runner.Events(bus)
})
```

`migrate.Applied`, `migrate.RolledBack`, and `migrate.Failed` describe individual
migrations, not an all-or-nothing deployment. A confirmed migration commit with
post-commit failure publishes success with nil event error but stops later work.
A step error is failure even if it happens to wrap `database.ErrAfterCommit`.
Outer session cleanup does not rewrite outcomes already emitted. Events inside
a caller-owned transaction remain provisional until its outer commit.

Construction defaults to `slog.Default()` for migration logs and no event bus.
Neither logs nor events replace verifying recorded history and the resulting
schema after recovery. Continue with [Troubleshooting](troubleshooting.md) if
execution or cleanup did not complete successfully.

For execution and configuration contracts, see the
[`migrate` API reference](https://pkg.go.dev/arkoren.dev/core/service/database/migrate).
