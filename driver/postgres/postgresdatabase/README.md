# postgresdatabase

Use the PostgreSQL database contract through native pgx and pgxpool. Configure
the driver with `New(dsn, configure...)`, register `Start`, `Stop`, and `Ping` with your
application, and wrap it with `database.New(driver)` for query-builder support.
`NewFrom(pool)` takes ownership of the exact supplied pool; `Stop` closes it.
Configure that pool's connection settings and hooks before passing it in.
`NewFrom` accepts no callbacks because pgxpool limits are fixed at creation.
`New` callbacks edit a `*postgresdatabase.Builder` once in order on one fresh
draft before validation. Nil entries panic; expanded nil or empty slices omit
edits. Completed scalar settings detach from retained drafts. `MaxIdleConns`
remains symmetry-only; use `ConnMaxIdleTime` to expire idle pgx connections.

## Installation and connection

Run `go get arkoren.dev/driver/postgres` from your application's module directory.
Import `arkoren.dev/driver/postgres/postgresdatabase` and
`arkoren.dev/core/service/database`. With your configured PostgreSQL URL in `dsn`:

```go
driver, err := postgresdatabase.New(dsn, func(connection *postgresdatabase.Builder) {
	connection.MaxOpenConns(20)
})
```

Check `err` before using the driver. Construction validates settings without
verifying credentials; `Start`, `Ping`, or the first query connects. Use verified
TLS for remote production servers and operation contexts with deadlines.
`New` rejects nonzero `pool_min_conns` and `pool_min_idle_conns`: native pgxpool
would start those connections during construction. If you need eager minima,
create the native pool at your application's startup boundary and transfer it
with `NewFrom`. The native health-check period must be positive. Invalid DSN
messages omit supplied values; `errors.As` still exposes the native parse cause,
which can contain the original DSN and must not be logged as redacted output.
See the [driver guide](../../../cmd/arkoren/skills/arkoren/references/guides/database/drivers.md#postgresql)
for TLS, native bulk copy, and a disposable local server.

## Keep protected SQL on one session

Use `WithSession` when a session lock must protect several transactions or
autocommit statements. All facade operations using the callback context stay on
one physical connection; losing it causes SQL to fail, never reconnect or replay.

The fragment below assumes the configured `driver`, your report ID `id`, and a
`context.Context` named `ctx`. Import `arkoren.dev/foundation/query` for raw SQL.

```go
db := database.New(driver)
err := db.WithSession(ctx, func(ctx context.Context) error {
	unlock, acquired, err := db.TryLock(ctx, "nightly-report")
	if err != nil || !acquired {
		return err
	}
	defer unlock(ctx)

	return db.WithTransaction(ctx, func(ctx context.Context) error {
		_, err := db.Exec(ctx, query.Raw("UPDATE reports SET ready = true WHERE id = ?", id))
		return err
	})
})
```

`Driver.WithSession(ctx, func(database.Driver) error)` is the raw-driver
equivalent. Sessions do not begin implicit transactions. Nested sessions borrow
the connection but have separate callback lifetimes. A session inside a
transaction stays in that transaction; only `WithTransaction` adds a savepoint.
Do not use callback drivers or contexts concurrently or after return. Session
handles and cursors expire with `database.ErrSessionDone`; transaction handles
expire with `sql.ErrTxDone`. Outstanding cursors close automatically on return.
Canceling an enclosing scope also rejects operations that supply a fresh
context; passing `context.Background()` does not resume canceled callback work.

Raw `Driver.Select` replaces the destination slice's previous elements. An empty
result leaves it empty, and a scanning failure can leave a partially read result.
The facade's typed `Database.Select` uses a fresh result for each call.

## Locks and cleanup

Locks are non-reentrant, including within a transaction and across scopes on the
same physical session. A repeated `TryLock` returns `nil, false, nil`; `Lock`
waits until cancellation or release. Transaction locks belong to the outermost
transaction and survive nested rollback or panic. Their returned unlock is a
no-op. Session locks outside transactions end at explicit unlock or the acquiring
callback's return. Outer session locks survive child commits and rollbacks.

The connection remains acquired through transaction finalization and unlock.
Cleanup has a detached five-second budget, joins errors with callback failures,
and discards sessions when acquisition, rollback, or unlock is uncertain. An
error wrapping `database.ErrAfterCommit` means the outer transaction committed
but cleanup failed: **do not retry the transaction**. Panics propagate unchanged
after cleanup. Explicit unlock is idempotent, even with an expired context.

> **Warning:** Use a direct PostgreSQL connection or session-mode pooling.
> PgBouncer transaction pooling can change the server session between commands
> and breaks managed locks, including locks acquired inside transactions.

Root blocking `Lock` releases each refused attempt's connection before polling.
A successful root lock still holds one pool slot until unlock, so root queries
need spare capacity. A pinned session waiting on a raw root holder can still
exhaust that spare capacity. For contended protected work, leave the session on
`TryLock` refusal before retrying. No FIFO lock ordering is promised.

`driver.DB()` always exposes the root pool, including on callback drivers. Its
operations bypass scopes, transaction routing, and facade telemetry. Raw native
advisory-lock and transaction-control SQL is not managed by these lifetimes.

See the [API reference](https://pkg.go.dev/arkoren.dev/driver/postgres/postgresdatabase)
and [database guide](../../../cmd/arkoren/skills/arkoren/references/guides/database/overview.md).
