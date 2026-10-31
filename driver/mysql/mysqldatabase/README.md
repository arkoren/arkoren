# mysqldatabase

Use MySQL through the database contract and the native `go-sql-driver/mysql`
client. Run `go get arkoren.dev/driver/mysql` from your application's module,
then import `arkoren.dev/driver/mysql/mysqldatabase` and
`arkoren.dev/core/service/database`.

## Configure and connect

With your native MySQL DSN in `dsn`, construct a lazy pool:

```go
driver, err := mysqldatabase.New(dsn, func(connection *mysqldatabase.Builder) {
	connection.MaxOpenConns(20)
})
```

Check `err` before using the driver. Construction performs no network I/O;
`Start(ctx)`, `Ping(ctx)`, or the first operation connects. Register `Stop` with
your application's cleanup. It closes the root pool even with a canceled context.
`NewFrom(pool, configure...)` takes ownership of the exact supplied `*sql.DB`;
only positive pool settings apply, and its DSN is not rewritten.

`New` adds `parseTime=true` unless the native parameter list explicitly sets it.
Password text containing `?` or `parseTime=` is not a parameter. Explicit
`parseTime=false` remains disabled; `NewFrom` preserves the native pool's policy.
Malformed native DSNs match `ErrInvalidConfig` without printing supplied values.
The unwrapped native cause remains inspectable but is not redacted diagnostic
output. Use verified TLS and operation deadlines for remote servers.

## Keep protected work in its scope

Wrap the driver with `database.New(driver)`. Given a context named `ctx` and the
`arkoren.dev/foundation/query` import, a retained session can cover several
transactions without changing physical connections:

```go
db := database.New(driver)
err := db.WithSession(ctx, func(ctx context.Context) error {
	return db.WithTransaction(ctx, func(ctx context.Context) error {
		_, err := db.Exec(ctx, query.Raw("UPDATE counters SET value = value + 1"))
		return err
	})
})
```

Handle the returned error. A session alone does not start a transaction or undo
autocommit SQL. Nested transactions use savepoints. MySQL DDL can implicitly
commit and invalidate savepoints, so rollback does not make schema changes atomic.

Callback handles and contexts are synchronous and expire on return. Session I/O
then fails with `database.ErrSessionDone`; transaction I/O fails with
`sql.ErrTxDone`. Cleanup closes outstanding cursors, finishes the transaction,
and releases owned named locks before pool return. Uncertain sessions are
discarded. `database.ErrAfterCommit` means the data committed but cleanup failed:
do not retry that transaction. Native `SQL()` always returns the root pool and
bypasses scoped routing and facade telemetry.
Only a successful callback return authorizes commit; panic and `runtime.Goexit`
also roll back nested and outer transaction work.

## Read query results

Raw `Driver.Select` replaces a caller's existing slice, including clearing it
for an empty result. A scan failure can leave a successfully scanned prefix.
The typed `Database.Select` facade allocates a fresh result per call. Close
streaming cursors promptly; they retain physical connection capacity.

See the [database driver comparison](../../../cmd/arkoren/skills/arkoren/references/guides/database/drivers.md#mysql)
for lock and schema limits and the
[API reference](https://pkg.go.dev/arkoren.dev/driver/mysql/mysqldatabase).
