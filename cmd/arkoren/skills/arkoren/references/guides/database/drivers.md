---
title: Database Drivers
nav: Drivers
description: Choose between the SQLite, PostgreSQL, and MySQL database drivers, connect and configure each, and see how locking and upserts differ.
order: 110
---

# Database Drivers

## Introduction

Choose a database driver for your application's storage and concurrency needs.
All three drivers use the same facade and column-mapping rules; SQL dialects,
locking, and schema behavior differ as described below. There is no memory
database driver: use SQLite in memory for isolated database tests.

To choose a driver, start from what you run:

- **SQLite** suits tests, tools, desktop or edge applications, and single-host
  services. It needs no server but allows only one writer at a time.
- **PostgreSQL** is the default choice for production services. It handles many
  concurrent writers, has rich types such as `jsonb`, and supports `RETURNING`
  on the query builder.
- **MySQL** (and MariaDB) is the choice when your organization already runs it.
  It handles concurrent writers well, but does not support `RETURNING`.

You can change drivers without rewriting facade calls, but native SQL and types
still differ. Run [integration tests](../testing/integration-tests.md) against
the database you deploy.

## Compatibility matrix

Compare capabilities exposed through Arkoren's database facade and query builder,
not every SQL feature of each server. All three drivers support queries, pinned
sessions, and transactions; the differences below affect portability.

| Driver | `RETURNING` | Upserts | Nested transactions | Transactional DDL | Cross-process named locks | Native pool access |
| --- | --- | --- | --- | --- | --- | --- |
| [SQLite](#sqlite) | Yes | Yes | Yes | Yes | Partial — File-backed databases on Linux, macOS, and the BSDs only. | Yes |
| [PostgreSQL](#postgresql) | Yes | Yes | Yes | Yes | Yes | Yes |
| [MySQL / MariaDB](#mysql) | No | Partial — Conflict targets are ignored; any unique key can trigger the update. | Partial — DDL implicitly commits and invalidates savepoints. | No | Yes — GET_LOCK coordinates connections to the same server under managed ownership. | Yes |

Nested transactions use savepoints. MySQL upserts use `ON DUPLICATE KEY UPDATE`,
so they cannot select a particular conflict key as PostgreSQL and SQLite can.
The MySQL dialect rejects the builder's `Returning`, including when connected
to MariaDB; native server features do not change this Arkoren limitation.

Named locks coordinate callers using the same database on PostgreSQL and the
same server on MySQL. SQLite file locks coordinate processes on the same host
only on the platforms listed above; otherwise its locks are process-local.
The driver does not detect network filesystems. Do not rely on these file locks
for cross-process coordination on a network filesystem.
Native pool operations bypass facade sessions, transactions, and telemetry;
they are not a portable substitute for a missing builder capability.

The matrix describes shipped interfaces and compiler support, not a connectivity
test or proof that a deployment accepts every statement. The native-pool profiles
below still apply; run provider integration tests with your server version,
configuration, schema, and data before relying on a capability.

## Wiring a driver

Run installation commands from your application's module directory. Examples use
`database` from `arkoren.dev/core/service/database`, an existing `app`
from the [lifecycle and ownership guide](../application/lifecycle-and-ownership.md),
and a `context.Context` named `ctx`. As in the [overview](overview.md), focused
fragments leave routine error checks to your application; check construction
errors before registering hooks or wrapping the driver.

The pool-statistics examples use standard-library `fmt`; SQL success checks use
`query` from `arkoren.dev/foundation/query`. Construction does not create your
application tables; apply [migrations](migrations.md) before querying them.

`New` validates arguments and creates a lazy pool without verifying credentials.
The first `Start`, `Ping`, or query opens connections. Register `Start` to check
connectivity at startup, `Stop` to close the pool, and `Ping` for health:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	composer.Check("database", driver.Ping)
}
```

Then wrap the driver in the facade, which most of your code uses:

```go
db := database.New(driver)
```

The background `Ping` check makes database outages affect
[readiness](../first-steps/deploying.md) without restarting the process.

A blank DSN, or a negative pool setting, makes the constructor return an error
that wraps the driver's `ErrInvalidConfig`, such as
`postgresdatabase.ErrInvalidConfig`.

PostgreSQL/MySQL DSN parse failures use credential-safe top-level messages while
preserving native causes for deliberate `errors.Is`/`errors.As` inspection. Do
not log an unwrapped native parse error as if its text had been redacted.

For work the facade does not cover, take the driver out of the facade with
`DriverAs` and use its native handle: `SQL() *sql.DB` on SQLite and MySQL, and
`DB() *pgxpool.Pool` on PostgreSQL. This PostgreSQL-specific fragment imports
`arkoren.dev/driver/postgres/postgresdatabase` and standard-library `errors`:

```go
driver, ok := db.DriverAs[*postgresdatabase.Driver]()
if !ok {
	return errors.New("not a PostgreSQL driver")
}

pool := driver.DB()
```

> [!WARNING]
> The native handle is always the root pool, including on raw driver callback
> handles. A query run through it bypasses sessions and executes outside the
> transaction, is neither committed nor rolled back with it, and
> records no [telemetry](query-monitoring.md). Inside a transaction, use the
> facade with the context `WithTransaction` gives you.

If something else already opened the connection pool, wrap it with the
driver's `NewFrom` instead of `New`. The driver then owns the pool, and `Stop`
closes it, so let one owner close it.

## SQLite

SQLite embeds the database in your process through pure-Go `modernc.org/sqlite`;
it needs neither cgo nor a server. Choose it for tests, tools, and single-host services.

### Installation

```sh
go get arkoren.dev/driver/sqlite
```

### Connecting

Pass a file path, a `file:` URI, or `:memory:` to `New`:

```go
import "arkoren.dev/driver/sqlite/sqlitedatabase"

driver, err := sqlitedatabase.New("app.db")
```

For tests, use an in-memory database:

```go
driver, err := sqlitedatabase.New(":memory:")
```

Every private memory connection would open its own empty database. The driver
recognizes `:memory:` with optional parameters, `file::memory:` (including an
escaped memory filename), and `file:` URIs whose actual `mode` parameter is
`memory`. It defaults these profiles to one
connection unless you set `Builder.MaxOpenConns` yourself. A new in-memory driver
normally starts empty; an explicitly shared-cache memory URI can share an
existing database in the process. Ordinary file paths containing the text
`mode=memory` do not select the memory policy. The database disappears when its
last connection closes, so
do not set `Builder.ConnMaxLifetime` or `Builder.ConnMaxIdleTime` on an in-memory
driver; closing the connection discards the database.

The constructor adds `_time_format=sqlite` unless explicitly set, enabling
`time.Time` round trips through `DATE`, `DATETIME`, and `TIMESTAMP` columns from
the [schema builder](schema-builder.md). SQLite compares timestamps as text:
write every value in UTC with `t.UTC()` for reliable ordering.
Only a real `_time_format` query parameter suppresses this default; a filename
containing that text does not. DSN option text is not part of the opened filename.

### Configuration

The driver has no settings of its own beyond the pool, because SQLite is
configured through pragmas in the DSN. A file-backed service that serves
requests usually wants WAL mode, a busy timeout, and foreign keys:

```go
dsn := "file:app.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

driver, err := sqlitedatabase.New(dsn,
	func(connection *sqlitedatabase.Builder) {
		connection.MaxOpenConns(8)
	},
)
```

Each `_pragma` runs on every new connection, which matters because SQLite
settings such as `foreign_keys` are per connection and are off by default.
`journal_mode(WAL)` lets readers continue while a writer works, and persists in
the file. `busy_timeout` makes a connection wait for the write lock instead of
failing at once.

All drivers expose `MaxOpenConns`, `MaxIdleConns`, `ConnMaxLifetime`, and
`ConnMaxIdleTime`. Zero preserves native defaults, except SQLite's in-memory
limit of one. SQLite/MySQL `NewFrom` apply only positive limits, do not rewrite
the DSN, and transfer pool ownership. `New` rejects negatives; `NewFrom` ignores
them. Callbacks run once in order; nil callbacks panic. See each API reference
for the full construction contract.

For SQLite `NewFrom`, the native connections must implement
`database/sql/driver.ConnBeginTx`, as the pinned modernc driver does. Unsupported
native profiles fail transactions with `errors.ErrUnsupported`; they do not fall
back to a contextless BEGIN. Configure memory pooling and time formatting before
transferring a native pool, because `NewFrom` does not infer either policy.

### Querying rows

Wrap the driver with `database.New(driver)` and use the shared
[selecting rows example](overview.md#running-queries) against either the file or
in-memory database; no SQLite-specific query wiring is needed.

### Behavior and limits

SQLite allows one writer at a time. When several connections write at once, the
extra writers wait for the lock, and without a busy timeout they fail
immediately with `SQLITE_BUSY` ("database is locked"). Set a
busy timeout, as above, or set `Builder.MaxOpenConns(1)`, which makes Go queue the
queries in your process so SQLite never sees contention. With a single
connection, run every query inside a transaction through the context that
`WithTransaction` gives you; a query that goes around it waits for the
connection the transaction holds, and never gets it.

`Lock(ctx, name)` takes an in-process lock shared by every SQLite driver in
the process. For a file-backed database on Linux, macOS, and the BSDs, it
also takes an operating system file lock (`flock`) on a sibling file named
after the database and the lock, so the lock holds across processes on the
same host too. The kernel drops the file lock when a process exits, so a
crashed holder never blocks the others. On other platforms, and for in-memory
databases, the lock covers one process only. On supported platforms, the driver
attempts `flock` without detecting network filesystems; do not rely on
cross-process coordination when the database is on a network filesystem.
A canceled `ctx` stops the wait
with an error that wraps `context.DeadlineExceeded` or `context.Canceled`.
[Raw SQL and locks](raw-sql-and-locks.md) shows how to use locks.

SQLite supports `RETURNING` and upserts (`ON CONFLICT`), so the query builder's
`Returning` and `OnConflict` work. SQLite stores JSON as text; read and write
it through `database.JSON[T]`, which accepts the text or bytes each database
returns. Transactions use SQLite's defaults, with no isolation options, and
nested transactions use savepoints like on the other drivers.

Transaction cancellation finalizes SQLite rollback synchronously before managed
locks are released. Native Commit/Rollback methods have no context parameter,
so finalization can outlive the cleanup budget; the driver does not claim to
interrupt those calls. If cleanup leaves transaction state uncertain, the owning
connection is discarded before its locks are released, rather than returned for
unrelated work. Keep callback work synchronous and close its cursors first.

### Inspecting the native pool

For `database/sql` diagnostics, read the configured driver's pool statistics:

```go
stats := driver.SQL().Stats()
fmt.Println(stats.OpenConnections, stats.InUse)
```

These are current pool counts, not query telemetry. Do not close this shared
pool separately or use it for SQL that must join a facade session or transaction.

### Running locally

SQLite needs no server. With a driver created from `:memory:`, call
`driver.Start(ctx)` and then `db.Find[int](ctx, query.Raw("SELECT 1"))` on its
facade; a successful result is `1`. Stop the driver when finished; its in-memory
data disappears. For persistence, use `app.db` in your application directory.
Ignore `app.db`, `app.db-wal`, `app.db-shm`, and `app.db.*.lock` in version control.
Stopping a file-backed driver does not delete those files.

### API references

For the full list of functions and options, see
[the `sqlitedatabase` API reference](https://pkg.go.dev/arkoren.dev/driver/sqlite/sqlitedatabase).

## PostgreSQL

The PostgreSQL driver uses `pgxpool`, not `database/sql`. Choose it for concurrent
writers, rich types, and a server shared by replicas. `DB()` exposes native pgx
operations, including `COPY`, `LISTEN`, and batches.

### Installation

```sh
go get arkoren.dev/driver/postgres
```

### Connecting

Pass a connection URL, or a `key=value` string, to `New`:

```go
import "arkoren.dev/driver/postgres/postgresdatabase"

driver, err := postgresdatabase.New("postgres://app:secret@db.internal:5432/app?sslmode=verify-full")
```

The example assumes an existing PostgreSQL server, application role, database,
and a trusted server certificate matching `db.internal`. Supply `sslrootcert`
when your server uses a private CA. The password belongs in a
[secret store](../secrets/overview.md), not in the source. Build the URL at startup with `net/url` so that special characters in
the password are escaped. SQL sent directly through the native client uses
numbered placeholders (`$1`, `$2`). Through the facade, use `?` in
[`query.Raw`](raw-sql-and-locks.md#raw-statements); it rewrites them for you.

### Configuration

Pool sizes are set in construction callbacks:

```go
driver, err := postgresdatabase.New(dsn,
	func(connection *postgresdatabase.Builder) {
		connection.MaxOpenConns(20)
		connection.MaxIdleConns(5)
		connection.ConnMaxLifetime(30 * time.Minute)
		connection.ConnMaxIdleTime(5 * time.Minute)
	},
)
```

Here `dsn` is your configured PostgreSQL connection string and `time` is the
standard-library package. The builder sets the `pgxpool` limits: `MaxOpenConns` is its maximum pool
size (default: the larger of 4 and the number of CPUs), `ConnMaxLifetime`
its connection lifetime (default: one hour), and `ConnMaxIdleTime` its idle
time (default: 30 minutes). `pgxpool` has no limit on idle connections, so
`MaxIdleConns` is accepted for symmetry with the other drivers and has no
effect.

`New` rejects nonzero native `pool_min_conns` or `pool_min_idle_conns`, because
those settings would cause pgxpool to connect during construction. It also
rejects an effective `pool_health_check_period` at or below zero with
`ErrInvalidConfig`. This preserves lazy construction instead of silently
discarding requested minima.

Keep `MaxOpenConns` below the server's `max_connections` divided by the replica
count, leaving room for migrations and administrators. Connection rotation can
help with proxy or failover disconnections.

Pass a pool created with `pgxpool.NewWithConfig` to `NewFrom(pool)`. It accepts
no callbacks, because pgxpool limits are fixed at creation. Configure the native
pool first. NewFrom takes ownership; Stop closes the exact pool.

If you need eager minimum connections, prepare a `pgxpool.Config`, then acquire
the native pool with `pgxpool.NewWithConfig(ctx, config)` **inside an application
Startup callback**, not during composition. After acquisition, wrap it with
`postgresdatabase.NewFrom(pool)` and immediately register that driver's `Stop`
as cleanup. Wire dependent startup work only after the owner has published its
facade. The native configuration, not `NewFrom`, owns the pool limits and health
period; construction is no longer claimed to be lazy for that native acquisition.

URL settings include `connect_timeout`, `application_name`, and `search_path`.
For TLS, `sslmode=disable` is for local development; `require` encrypts;
`verify-full` also verifies the server certificate and is recommended across
untrusted networks. `sslrootcert` selects the CA.

### Querying rows

Wrap the driver with `database.New(driver)` and follow the shared
[selecting rows example](overview.md#running-queries). To retrieve generated
columns after a write, use the [RETURNING example](query-builder.md#inserting-rows);
PostgreSQL returns those values in the same statement.

### Behavior and limits

Queries have no implicit timeout. Pass a context with a deadline, or set
`statement_timeout` on the server or role, so that a stuck query cannot hold a
connection forever.

`Stop` waits until every connection in use returns to the pool, so close your
cursors and finish your transactions before shutdown. If the context passed to
`Stop` ends first, `Stop` returns its error and the pool finishes closing in
the background.

`Lock(ctx, name)` uses PostgreSQL advisory locks, so the lock spans every
process and host connected to the same database. The name is hashed to the
64-bit key that advisory locks use, so any nonempty name is valid. Outside a
transaction, the lock holds a connection from the pool until you unlock, so
keep the pool larger than the number of locks you expect to hold at once.
Root waiters use nonblocking attempts and release contended connections between
them. Raw holders can still reserve every connection and starve root queries.
Inside a transaction, the driver retains that transaction's physical connection
and takes session-level advisory locks. It ends the transaction, releases those
locks, and only then returns the connection. Fresh nested acquisitions therefore
survive savepoint rollback and recovered panics.

Use direct or session-pooled PostgreSQL connections for these locks, not
transaction-mode PgBouncer. Managed advisory locks belong to the physical
session, which transaction pooling does not preserve across transactions.

Use `WithSession` for protected SQL spanning multiple transactions: SQL and locks
then share one physical session, and session loss cannot reconnect and continue
the callback. A pinned scope cannot release its connection while waiting. When
mixing scoped waiters with raw holders that need root query capacity, use
`TryLock` and end a contended session before retrying outside it. Failed cleanup
discards the connection; failure after a confirmed commit wraps `ErrAfterCommit`.

`RETURNING` and upserts (`ON CONFLICT (columns) DO UPDATE`) are supported by
the query builder. JSON columns, preferably `jsonb`, work through
`database.JSON[T]`. Transactions use PostgreSQL's default isolation level,
READ COMMITTED; the facade has no option to change it per transaction, so use
a lock or `SELECT ... FOR UPDATE` where you need stronger guarantees. Nested
transactions are savepoints, and PostgreSQL slows down once a transaction has
more than about 64 of them open, so avoid a nested transaction per row.

### Using native PostgreSQL operations

For bulk copy, use the configured `driver`, an existing `imports` table with
a text `email` column, and `pgx` from `github.com/jackc/pgx/v5`:

```go
copied, err := driver.DB().CopyFrom(ctx, pgx.Identifier{"imports"}, []string{"email"},
	pgx.CopyFromRows([][]any{{"ada@example.com"}, {"grace@example.com"}}))
```

On success, `copied` is `2`. Native calls bypass facade telemetry and scoped
transactions; use a native pgx transaction when the copy must be atomic with
other native operations. The same pool exposes pgx batches. For `LISTEN`, retain
one native connection and release it after listening; a pool-level command alone
does not keep subsequent notification reads on that connection. See
[pgx documentation](https://pkg.go.dev/github.com/jackc/pgx/v5) for these native APIs.

### Running locally

With Docker installed and port `15432` free, create a disposable local server:

```sh
docker run --name app-postgres -d -p 127.0.0.1:15432:5432 -e POSTGRES_USER=app -e POSTGRES_PASSWORD=local-only -e POSTGRES_DB=app postgres:17
docker exec app-postgres pg_isready -U app -d app
```

Wait until the readiness command reports `accepting connections`, then connect:

```go
driver, err := postgresdatabase.New("postgres://app:local-only@127.0.0.1:15432/app?sslmode=disable")
```

Call `driver.Start(ctx)` to verify the credentials, then query `SELECT 1` through
the facade as shown for SQLite. The result is `1`. These credentials and disabled
TLS are for this loopback-only development server, not production.

After stopping your application and its driver, remove the disposable server.
This deletes the container and its anonymous data volume; do not run it for data
you need to keep:

```sh
docker rm -f -v app-postgres
```

### API references

For the full list of functions and options, see
[the `postgresdatabase` API reference](https://pkg.go.dev/arkoren.dev/driver/postgres/postgresdatabase).

## MySQL

The MySQL driver connects through `go-sql-driver/mysql` and works with MySQL
and MariaDB. Choose it when MySQL is what your team already operates.

### Installation

```sh
go get arkoren.dev/driver/mysql
```

### Connecting

MySQL uses the DSN format of its Go driver, with the host inside `tcp(...)`:

```go
import "arkoren.dev/driver/mysql/mysqldatabase"

driver, err := mysqldatabase.New("app:secret@tcp(db.internal:3306)/app?tls=true")
```

The constructor adds `parseTime=true` to the DSN unless it sets `parseTime`
itself, so `DATETIME` and `TIMESTAMP` columns scan into `time.Time`. Placeholders
in hand-written SQL are `?`.
The check uses native DSN parameter boundaries after the database component:
passwords or socket paths containing `parseTime=` do not select the policy.
An explicit `parseTime=false` parameter is preserved rather than overwritten.

### Configuration

With your MySQL connection string in `dsn`, configure the pool using `time`
from the standard library:

```go
driver, err := mysqldatabase.New(dsn,
	func(connection *mysqldatabase.Builder) {
		connection.MaxOpenConns(20)
		connection.MaxIdleConns(5)
		connection.ConnMaxLifetime(5 * time.Minute)
	},
)
```

Keep `Builder.ConnMaxLifetime` shorter than the server's `wait_timeout` and any
proxy's idle timeout. A connection that the server has already closed
otherwise fails the next query that uses it. The rest of the configuration is
in the DSN: `tls` (`true`, `skip-verify`, `preferred`, or the name of a
registered configuration) for encryption, `timeout`, `readTimeout`, and
`writeTimeout` for network timeouts, and `charset` and `loc` for text and time
handling. The Go driver's documentation lists them all.

Zero settings preserve `database/sql` defaults: unlimited open connections, up
to two idle connections, and no lifetime or idle-age limit. Negative settings
make `New` fail. `NewFrom` applies only positive limits to the supplied pool,
does not add `parseTime`, and transfers ownership to the driver.

### Querying rows

Wrap the driver with `database.New(driver)` and use the shared
[selecting rows example](overview.md#running-queries). For writes, use `Exec` and
its affected-row count. Read a generated row back by an application-owned unique
key inside the same transaction; MySQL does not support the builder's `Returning`.

### Behavior and limits

`Lock(ctx, name)` polls `GET_LOCK` with a zero timeout and waits under your
context between attempts. Root waiters release contended connections between
attempts; a raw held lock reserves one pool connection until unlock. The lock
spans every process connected to the same server. MySQL limits lock names
to 64 bytes; the driver shortens a longer name to a hash of it, so any nonempty
name is valid. Inside a transaction, the lock stays held until the transaction ends,
because `GET_LOCK` belongs to the session and not to the transaction.
The transaction and its locks use the same retained connection, including with
a one-connection pool. The driver commits or rolls back, unlocks, and only then
returns it to the pool. Nested rollback cannot release a fresh outer-owned lock.

`WithSession` retains one connection for protected SQL across several transaction
boundaries without starting a transaction itself. A lost session never reconnects
or replays the callback. Raw holders can still exhaust a bounded pool, and already
pinned waiters cannot surrender their connection while waiting; use `TryLock` and
end the session on contention when mixing them with raw holders needing root
queries. Failed cleanup discards the connection. Cleanup failure after confirmed
commit wraps `ErrAfterCommit`; neither it nor successful session cleanup makes
MySQL DDL transactional.

MySQL upserts use `ON DUPLICATE KEY UPDATE`: any unique key can trigger the
update, ignoring `OnConflict` columns. `Returning` fails with
`query.ErrUnsupported`; `Exec` returns only affected-row counts. Use the
[read-back approach above](#querying-rows-2) for generated values.

MySQL commits implicitly around DDL statements such as `CREATE TABLE`, which
ends the transaction and its savepoints, so a nested transaction that runs DDL
fails. Transactions use the server's default isolation level, REPEATABLE READ
for InnoDB. JSON columns work through `database.JSON[T]`.

Migration bookkeeping has a separate compatibility requirement: the migrator
creates canonical `VARBINARY(191)` IDs and checks the resolved ledger's actual
column/index metadata before steps or history mutation. Legacy character-column
ledgers, including `_bin` and `NO PAD` text layouts, are not automatically
converted. Metadata inspection must be permitted; an inspection failure is not
proof that conversion is needed. See [MySQL ledger compatibility](migrations.md#mysql-ledger-compatibility)
for the exact layout, frozen target, and operator conversion prerequisites.
This policy applies to the migrator's ledger, not your application tables.
Ledger preparation itself needs no `ALTER` privilege; operator repair remains
a separately authorized action.

### Inspecting the native pool

For `database/sql` pool diagnostics, use the configured driver:

```go
stats := driver.SQL().Stats()
fmt.Println(stats.OpenConnections, stats.InUse)
```

The result reports live pool usage. `SQL()` also exposes native SQL operations,
but those bypass facade sessions, transactions, and telemetry. `Stop` owns pool
closure; do not close it separately.

### Running locally

With Docker installed and port `13306` free, start a disposable MySQL server:

```sh
docker run --name app-mysql -d -p 127.0.0.1:13306:3306 -e MYSQL_ROOT_PASSWORD=local-root-only -e MYSQL_DATABASE=app -e MYSQL_USER=app -e MYSQL_PASSWORD=local-only mysql:8.4
docker exec app-mysql mysqladmin ping -h 127.0.0.1 -uapp -plocal-only --silent
```

Repeat the readiness command until it succeeds, then connect with the application
user rather than root:

```go
driver, err := mysqldatabase.New("app:local-only@tcp(127.0.0.1:13306)/app")
```

Call `driver.Start(ctx)` to check credentials, then query `SELECT 1` through the
facade; success returns `1`. The example uses loopback-only plaintext access and
disposable credentials. Configure verified TLS and real secrets in production.

After stopping your application and driver, remove the container and its anonymous
data volume. This deletes its data; use it only for disposable development data:

```sh
docker rm -f -v app-mysql
```

### API references

For the full list of functions and options, see
[the `mysqldatabase` API reference](https://pkg.go.dev/arkoren.dev/driver/mysql/mysqldatabase).

## Other databases

Arkoren has no generic SQL driver. For another database, follow
[Writing a driver](../drivers/writing-a-driver.md) for the contract and
conformance suite. SQLite and MySQL demonstrate `database/sql` integration
with `query.Targets` for scanning.
