---
title: Drivers
description: Choose and configure SQLite, PostgreSQL, or MySQL, with clear compatibility differences.
summary:
  - Compare database drivers by transaction, locking, and SQL capabilities.
  - Configure connections, verify readiness, and own pool shutdown.
  - Understand memory-database limits and native-client escape hatches.
order: 20
---

# Drivers

Choose SQLite for an embedded database or isolated tests. Choose PostgreSQL or
MySQL when several application instances need a shared database server. All
three implement the database contract; their SQL dialects and coordination
boundaries still matter. There is no simulated memory driver: SQLite's memory
mode runs a real SQLite engine.

## Compatibility matrix

This compares the shipped Arkoren drivers and query compiler, not every feature
of each database product. Named locks coordinate cooperating callers, not all
SQL writers. Persistence assumes a persistent database and suitable server or
filesystem configuration; the driver does not supply replication or backups.

| Driver | Persistent Storage | Nested Transactions | Transactional DDL | Row Locks | Cross-Process Named Locks | RETURNING | Upserts |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [SQLite](#sqlite) | Partial — File-backed only; memory contents end with the last connection. | Yes — Savepoints. | Yes | No | Partial — Same-host file locks on supported Unix systems; not memory or network filesystems. | Yes | Yes |
| [PostgreSQL](#postgresql) | Yes | Yes — Savepoints. | Yes | Yes | Yes — Within one database; requires direct or session-pooled connections. | Yes | Yes |
| [MySQL](#mysql) | Yes | Partial — Savepoints; DDL can end them implicitly. | No | Partial — Query locking clauses target MySQL 8. | Yes — Within one server. | No | Partial — Uses native duplicate-key update or INSERT IGNORE semantics. |

All three support typed reads, writes, cursors, retained sessions, and named
locks. [Pagination](../queries/pagination.md) and
[batching](batching-and-streaming.md) operate through the same facade. Read
[Locking](locking.md) for lifetime rules and
[migration deployment](../migrations/deployment.md) before relying on DDL rollback.

## SQLite

SQLite runs in your process without a database server. Install its driver from
your application's module directory:

```sh
go get arkoren.dev/driver/sqlite
```

### Connecting

Import `arkoren.dev/driver/sqlite/sqlitedatabase` and
`arkoren.dev/core/service/database`. For a local application file:

```go
driver, err := sqlitedatabase.New("app.db")
db := database.New(driver)
```

Handle the construction error before using the driver. Construction validates
the required, nonblank DSN and nonnegative pool limits but opens the database
lazily. With your startup context, verify the connection before accepting work:

```go
err := driver.Start(ctx)
```

`Start` is a connectivity check, equivalent to `Ping`, not a background worker.
After successful startup, use the shared [typed read and write examples](overview.md).
Constructors do not create application tables; use
[migrations](../migrations/overview.md).

When work has drained and all callbacks and cursors have finished, release the
owned pool with `driver.Stop(cleanupCtx)`. SQLite's stop ignores that context
and closes through `database/sql`. In an Arkoren application, register startup
and cleanup using [application composition](../application/composition.md).

### Memory Databases

For a disposable test database, use an alternative construction:

```go
driver, err := sqlitedatabase.New(":memory:")
```

`New` defaults recognized memory DSNs to one open connection. A private memory
database belongs to a connection, so overriding that default with several
connections can produce independent databases with different tables. Closing
the last connection destroys the database, including pool expiry. Avoid
connection lifetime/idle expiry when the memory database must survive.

Memory databases have only process-local named locks. File-backed databases on
Linux, macOS, and BSD also use sibling lock files for other processes on the
same host. Other operating systems and network filesystems have no supported
cross-process lock guarantee. SQLite is not a horizontally scaled database.

### Connection Settings

For a file database, bound connections explicitly when needed:

```go
driver, err := sqlitedatabase.New("app.db", func(connection *sqlitedatabase.Builder) {
	connection.MaxOpenConns(4)
	connection.MaxIdleConns(2)
})
```

Positive limits replace native settings. Zero leaves them unchanged except
`New`'s memory default. `ConnMaxLifetime` and `ConnMaxIdleTime` optionally bound
connection reuse; zero does not disable an existing supplied-pool limit.
Negative settings are rejected by `New`.

`New` adds `_time_format=sqlite` unless the DSN explicitly sets it. DATE,
DATETIME, and TIMESTAMP values round-trip using that format, but SQLite compares
timestamps as text: write UTC consistently for correct ordering and comparisons.
Configure per-connection database policies through the native DSN where needed;
a one-off PRAGMA on the root pool does not configure every connection.

For a file-backed application that needs foreign-key enforcement and a bounded
wait for concurrent writers, set native pragmas in the DSN:

```go
dsn := "file:app.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
driver, err := sqlitedatabase.New(dsn)
```

SQLite permits one writer at a time. A busy timeout lets competing writes wait
up to 5,000 milliseconds rather than immediately returning SQLITE_BUSY.
Foreign-key enforcement is per connection and is not enabled by declaring a
schema foreign key alone. Add `_pragma=journal_mode(WAL)` if your deployment
needs WAL's reader/writer behavior, after checking its filesystem requirements.

### Native Pools

If you already own a `*sql.DB` opened with the SQLite driver, transfer it with
`sqlitedatabase.NewFrom(pool)`. The Arkoren driver then owns shutdown. `NewFrom`
does not rewrite the DSN or apply the memory single-connection default; configure
both on the supplied pool yourself. Its positive builder policies apply to the
existing pool.

Native connections supplied to SQLite must implement
`database/sql/driver.ConnBeginTx`, as the pinned modernc driver does. Unsupported
native profiles fail transactions with `errors.ErrUnsupported` rather than
falling back to a BEGIN that drops cancellation. Cancellation finalizes rollback
before managed locks release, but native Commit/Rollback have no context argument
and can outlive cleanup budgets.

`driver.SQL()` returns the root `*sql.DB`, useful for `Stats()`. Native SQL bypasses
facade telemetry, transaction routing, and retained-session lifetime checks.
Keep protected work on the facade's callback context.

### Running Locally

No server is required. After `driver.Start(ctx)`, verify a facade call with
`db.Find[int](ctx, query.Raw("SELECT 1"))`, importing
`arkoren.dev/foundation/query`; the result is `1`. Stop the driver when finished.
Memory data disappears on last connection close; stopping a file-backed driver
does not delete the database. Keep `app.db`, its `-wal` and `-shm` files, and
`app.db.*.lock` sibling files out of version control.

See the [`sqlitedatabase` API](https://pkg.go.dev/arkoren.dev/driver/sqlite/sqlitedatabase).

## PostgreSQL

Use PostgreSQL for a shared SQL server with transactional DDL and advisory locks
within a database. Provision the database, a role with application permissions,
and a DSN with the TLS policy required by your deployment. Install:

```sh
go get arkoren.dev/driver/postgres
```

### Connecting

Import `arkoren.dev/driver/postgres/postgresdatabase` and
`arkoren.dev/core/service/database`. Here `dsn` is your configured PostgreSQL URL
or key-value DSN, not a hardcoded production credential:

```go
driver, err := postgresdatabase.New(dsn)
db := database.New(driver)
```

Handle construction errors before using the driver. Blank or invalid DSNs and
negative limits match `postgresdatabase.ErrInvalidConfig`. Parse diagnostics omit
supplied values, but unwrapped native errors can still expose them; do not log
those causes or the DSN.

Construction performs no network I/O. `driver.Start(ctx)` or `driver.Ping(ctx)`
verifies connectivity through pgxpool. Use that as a startup/readiness check, then
use the shared [typed operations](overview.md). Drain callbacks and cursors before
`driver.Stop(cleanupCtx)`: stop waits for checked-out connections. If its context
ends, it returns that error while pool closure continues in the background.

### Connection Settings

If the application's connection budget is twenty, configure the pool at construction:

```go
driver, err := postgresdatabase.New(dsn, func(connection *postgresdatabase.Builder) {
	connection.MaxOpenConns(20)
	connection.ConnMaxIdleTime(10 * time.Minute)
})
```

Import `time`. Zero retains pgxpool settings: by default the maximum is the larger
of four and the CPU count, maximum lifetime is one hour, and maximum idle time is
thirty minutes. `MaxIdleConns` is accepted for symmetry but has no effect; pgxpool
has no idle-count limit. Use idle time instead.

Nonzero native `pool_min_conns` and `pool_min_idle_conns` are rejected because
they would connect during construction; `pool_health_check_period` must be
positive. For eager minimum connections, create the native pool at application
startup and transfer ownership with `NewFrom`.

### Native Pools

With an existing `*pgxpool.Pool`, call `postgresdatabase.NewFrom(pool)`. Configure
its `pgxpool.Config` before creating it: `NewFrom` has no builder callbacks.
The Arkoren driver owns the pool and closes it on stop.

`driver.DB()` exposes that root pool for pgx features such as COPY, LISTEN, and
batches. For example, `driver.DB().Stat()` reads native utilization. SQL issued
through it bypasses facade scopes and telemetry, even if the accessor was reached
from a transaction driver. Do not use it for work protected by a managed lock.

For a native bulk import, assume an existing `imports` table with a text `email`
column and import `github.com/jackc/pgx/v5`:

```go
copied, err := driver.DB().CopyFrom(ctx, pgx.Identifier{"imports"}, []string{"email"},
	pgx.CopyFromRows([][]any{{"ada@example.com"}, {"grace@example.com"}}))
```

On success, `copied` is 2. This native operation is outside facade transactions;
use a native pgx transaction if several native operations must be atomic. For
LISTEN, acquire and retain a native connection until listening ends, then release
it; a pool-level LISTEN alone does not pin subsequent notification reads.
Consult the [pgx API](https://pkg.go.dev/github.com/jackc/pgx/v5) for native
batches and notification handling rather than assuming facade scope propagation.

> [!WARNING]
> PgBouncer transaction pooling cannot preserve session advisory locks across
> operations. Use a direct or session-pooled connection for managed named locks
> and migrations; otherwise acquisition and release can reach different sessions.

### Running Locally

With Docker installed and port 15432 free, start a disposable loopback-only server:

```sh
docker run --name app-postgres -d -p 127.0.0.1:15432:5432 -e POSTGRES_USER=app -e POSTGRES_PASSWORD=local-only -e POSTGRES_DB=app postgres:17
```

Wait for readiness using `docker exec app-postgres pg_isready -U app -d app`.
When it reports accepting connections, use this development-only DSN:

```go
driver, err := postgresdatabase.New("postgres://app:local-only@127.0.0.1:15432/app?sslmode=disable")
```

Call `driver.Start(ctx)`, wrap with `database.New(driver)`, and read
`db.Find[int](ctx, query.Raw("SELECT 1"))`; success returns 1. These credentials
and disabled TLS are only for the disposable loopback server. Stop your
application and driver before removing it. The following command deletes the
container and its anonymous data volume; never use it for data you need to keep:

```sh
docker rm -f -v app-postgres
```

See the [`postgresdatabase` API](https://pkg.go.dev/arkoren.dev/driver/postgres/postgresdatabase).
PostgreSQL's separate search adapter is configured in
[Search Drivers](../search/drivers.md#postgres); constructing a database driver
does not construct or migrate that adapter.

## MySQL

Use MySQL when your application already operates a MySQL server. Provision a
database and role, then supply the native Go driver's DSN with appropriate TLS
and permissions. The query compiler's row-lock syntax targets MySQL 8; do not
infer full MariaDB compatibility from a compatible wire protocol. Install:

```sh
go get arkoren.dev/driver/mysql
```

### Connecting

Import `arkoren.dev/driver/mysql/mysqldatabase` and
`arkoren.dev/core/service/database`. With a configured native `dsn`:

```go
driver, err := mysqldatabase.New(dsn)
db := database.New(driver)
```

Handle construction errors before use. Blank or malformed DSNs and negative
limits match `mysqldatabase.ErrInvalidConfig`. Parse messages omit supplied
values; unwrapped native causes are not safe diagnostic output.

Construction opens a lazy `database/sql` pool. `driver.Start(ctx)` or
`driver.Ping(ctx)` checks connectivity before serving traffic. Use the shared
[typed operations](overview.md) after startup. Finish callbacks and cursors
before `driver.Stop(cleanupCtx)`, which ignores the context and closes the owned
pool through `database/sql`.

### Connection Settings

To keep this process within its connection budget:

```go
driver, err := mysqldatabase.New(dsn, func(connection *mysqldatabase.Builder) {
	connection.MaxOpenConns(20)
	connection.MaxIdleConns(5)
})
```

Positive settings replace native limits; zero leaves them unchanged. Without
configuration, `database/sql` supplies the pool defaults, including an unlimited
open-connection count. `ConnMaxLifetime` and `ConnMaxIdleTime` optionally limit
reuse. Account for held locks and sessions as well as request concurrency.

`New` adds `parseTime=true` unless the DSN's parameter list explicitly sets it.
This enables DATETIME scanning into `time.Time`; explicit `parseTime=false`
remains false. MySQL has no timezone-aware datetime type: use a consistent UTC
storage and connection policy.

### Native Pools

Transfer a `*sql.DB` opened with the native MySQL driver through
`mysqldatabase.NewFrom(pool)`. The Arkoren driver owns it afterward. `NewFrom`
applies positive builder policies but does not add `parseTime=true`; configure
the original DSN if time scanning needs it. `driver.SQL().Stats()` exposes
native utilization. Native SQL bypasses facade telemetry and context scopes.

MySQL DDL can implicitly commit and invalidate savepoints. Schema changes and
migration bookkeeping are not generally atomic; follow
[deployment and recovery](../migrations/deployment.md) rather than relying on a
transaction around DDL. Named locks use `GET_LOCK`, span a server, and hash
names longer than 64 bytes. They still obey the shared managed lifetime rules.

MySQL upserts ignore the builder's conflict target: any unique key can trigger
an update. `DoNothing` uses INSERT IGNORE, which is not identical to PostgreSQL's
conflict-only behavior. `Returning` is unsupported even when a wire-compatible
server offers a native returning feature. To read a generated row, select it by
an application-owned unique key inside the same transaction.

The [MySQL ledger requirement](../migrations/troubleshooting.md#mysql-ledger)
applies to migration history, not your application table collation. The runner
checks canonical binary storage and never repairs legacy history automatically.

### Running Locally

With Docker installed and port 13306 free, start a disposable loopback-only server:

```sh
docker run --name app-mysql -d -p 127.0.0.1:13306:3306 -e MYSQL_ROOT_PASSWORD=local-root-only -e MYSQL_DATABASE=app -e MYSQL_USER=app -e MYSQL_PASSWORD=local-only mysql:8.4
```

Repeat `docker exec app-mysql mysqladmin ping -h 127.0.0.1 -uapp -plocal-only --silent`
until it succeeds, then connect with the application user:

```go
driver, err := mysqldatabase.New("app:local-only@tcp(127.0.0.1:13306)/app")
```

Call `driver.Start(ctx)`, wrap with `database.New(driver)`, and read
`db.Find[int](ctx, query.Raw("SELECT 1"))`; success returns 1. This plaintext DSN
and disposable password are not production settings. After stopping the application
and driver, the following command deletes the container and anonymous data volume;
use it only for this disposable data:

```sh
docker rm -f -v app-mysql
```

See the [`mysqldatabase` API](https://pkg.go.dev/arkoren.dev/driver/mysql/mysqldatabase).
