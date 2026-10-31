# sqlitedatabase

Use a local SQLite database through the database contract and the pure-Go
`modernc.org/sqlite` driver. Run `go get arkoren.dev/driver/sqlite` from your
application's module, then import `arkoren.dev/driver/sqlite/sqlitedatabase` and
`arkoren.dev/core/service/database`.

## Open a file or an isolated memory database

For a file path or native SQLite URI in `dsn`, construct a lazy pool:

```go
driver, err := sqlitedatabase.New(dsn)
```

Check `err` before use. `Start(ctx)`, `Ping(ctx)`, or the first operation opens
the database; construction does not open files or run schema DDL. Register
`Stop` with your application's cleanup. `NewFrom(pool, configure...)` takes
ownership of the supplied `*sql.DB` and applies only positive pool settings;
it does not rewrite the DSN or apply the memory-connection default.
Its native connections must implement `database/sql/driver.ConnBeginTx`, as the
pinned modernc driver does. Transactions on other native profiles return
`errors.ErrUnsupported` rather than falling back to a contextless `BEGIN`.

For isolated tests, use `New(":memory:")`. Memory DSNs default to one connection,
including `:memory:` with parameters, `file::memory:`, and `file:` URIs whose
decoded `mode` parameter is `memory`. Text such as `mode=memory` inside a file
path does not select this policy. An explicit positive `MaxOpenConns` overrides
the default; private memory connections then have independent databases. Named
shared-cache memory databases share their native identity. Closing the last
connection destroys the database, so avoid connection expiry for memory pools.

`New` adds `_time_format=sqlite` unless the native parameters already set it;
filename text does not select the timestamp policy. This keeps emitted timestamp
text in SQLite's format. The pinned native driver can also read Go's time text,
but that does not make mixed text representations safe for SQL comparisons.
Store times in UTC for consistent text ordering.

## Keep scoped SQL on one connection

With an existing `ctx` and the `arkoren.dev/foundation/query` import:

```go
db := database.New(driver)
err := db.WithSession(ctx, func(ctx context.Context) error {
	return db.WithTransaction(ctx, func(ctx context.Context) error {
		_, err := db.Exec(ctx, query.Raw("UPDATE counters SET value = value + 1"))
		return err
	})
})
```

Handle the returned error. Sessions do not start implicit transactions or undo
autocommit statements. Explicit nested transactions use savepoints. Cancellation
requests synchronous native rollback before managed locks release; it does not
use an independent `sql.Tx` cancellation worker. Native `BEGIN` still receives
the caller's context. The bounded savepoint-cleanup context does not put a time
limit on native `Commit` or `Rollback`, whose interfaces take no context.
Panic and `runtime.Goexit` paths roll back too. Cleanup closes outstanding
cursors before releasing the retained connection. Callback contexts and drivers must be used synchronously;
after return, session I/O fails with `database.ErrSessionDone` and transaction
I/O fails with `sql.ErrTxDone`. `database.ErrAfterCommit` means committed data
with failed cleanup; do not retry the transaction. Native `SQL()` returns the
root pool and deliberately bypasses scopes and facade telemetry.

Raw `Driver.Select` replaces previous destination elements. An empty result
leaves the slice empty; a scan failure can leave the successfully scanned prefix.
The typed facade's `Database.Select` creates a fresh result for each call.
`Find` and `Select` preserve native cursor-close failures alongside scan errors.
A native cleanup failure makes the retained session unsafe to reuse or commit,
even if the callback ignores the immediate read error.

See the [database driver comparison](../../../cmd/arkoren/skills/arkoren/references/guides/database/drivers.md#sqlite)
for file-lock platform limits and the
[API reference](https://pkg.go.dev/arkoren.dev/driver/sqlite/sqlitedatabase).
