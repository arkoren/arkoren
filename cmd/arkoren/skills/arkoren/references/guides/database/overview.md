---
title: Database Overview
nav: Overview
description: Create a database facade, choose SQLite, PostgreSQL, or MySQL, manage its pool, and map rows to structs.
order: 10
---

# Database Overview

## Introduction

To read SQL rows into Go structs, give your code a `database.Database`
**facade**. It runs
statements, scans the rows into your structs, and carries transactions in the
`context.Context`. Underneath it sits a **driver**, the part that knows how
to talk to one particular database, and you choose it once when you build the
facade.

Statements come from the `query` package, a builder for parameterized SQL
that is described in [Query builder](query-builder.md). The facade compiles
them for the database your driver reports, so the same code runs on SQLite in
a test and on PostgreSQL in production. When the builder cannot express what
you need, you write the SQL yourself, as described in
[Raw SQL and locks](raw-sql-and-locks.md).

Start by choosing the driver for your database, then register its connection
pool with your application's lifecycle before running queries. The facade and
drivers follow the pattern described in
[Facades and drivers](../core-concepts/facades-and-drivers.md).

## Choosing a driver

Arkoren ships a driver for each of the three most common SQL databases. Each
is its own Go module, so your service only downloads the one it uses.

**PostgreSQL** is the usual choice for a production service. It supports
everything the toolkit offers: `RETURNING`, upserts, row locks, advisory
locks that span every instance of your service, and transactional schema
changes. Install it with `go get arkoren.dev/driver/postgres`.

**MySQL** works the same way for teams that run MySQL or MariaDB. It supports
upserts and row locks (MySQL 8 or later for `SKIP LOCKED` and `NOWAIT`), but
not `RETURNING`. Install it with `go get arkoren.dev/driver/mysql`.

**SQLite** needs no server: the database is a file, or lives in memory. It is
a good fit for tests, command-line tools, and small single-host services.
Install it with `go get arkoren.dev/driver/sqlite`.

Each driver implements the contract on its database's own Go driver: SQLite
and MySQL on `database/sql`, PostgreSQL on `pgx` and its connection pool.
[Drivers](drivers.md) compares the three in detail.

## Creating a database

For a first query without a server, use SQLite in memory. Install its module
with `go get arkoren.dev/driver/sqlite`, then wrap the driver in the facade.

Focused snippets keep error results visible but omit routine error handling.
Handle those errors in your application before using the returned values:

```go
import (
	"arkoren.dev/core/service/database"
	"arkoren.dev/driver/sqlite/sqlitedatabase"
)

driver, err := sqlitedatabase.New(":memory:")

db := database.New(driver)
```

Construction validates configuration but opens no connection. This private
database starts empty on first use and disappears when its last connection
closes. Keep the driver until consumers finish, then call `driver.Stop(ctx)`;
[Starting and stopping](#starting-and-stopping) shows application-owned cleanup.

For a persistent server database, use a PostgreSQL or MySQL driver instead.
Import `os` and your selected driver's package,
`arkoren.dev/driver/postgres/postgresdatabase` or
`arkoren.dev/driver/mysql/mysqldatabase`. These are separate alternatives:

```go
driver, err := postgresdatabase.New(os.Getenv("DATABASE_URL"))
```

```go
driver, err := mysqldatabase.New(os.Getenv("DATABASE_URL"))
```

Neither constructor verifies connectivity; register `Start` to check it before
serving traffic. Empty DSNs and negative pool limits are construction errors.
For a persistent SQLite database, replace `":memory:"` with `"file:app.db"`.

### Data source names

The first argument is the **data source name** (DSN), the string that says
where the database is and how to sign in. Its format belongs to the
underlying Go driver, so each database has its own:

| Database | Local-development DSN |
| --- | --- |
| PostgreSQL (pgx URL) | `postgres://app:secret@localhost:5432/app?sslmode=disable` |
| MySQL (Go driver format) | `app:secret@tcp(localhost:3306)/app` |
| SQLite (file URI) | `file:app.db?_pragma=busy_timeout(5000)` |

The server examples assume a local database and application user already exist.
Use verified TLS for remote production connections; the
[driver setup](drivers.md) explains TLS and disposable local servers.

A DSN contains the password, so read it from the environment or a secret
store and never commit it. [Secrets](../secrets/overview.md) shows how.

The MySQL and SQLite drivers add one setting you would otherwise forget. The
MySQL driver appends `parseTime=true`, so `DATETIME` columns scan into
`time.Time`. The SQLite driver appends `_time_format=sqlite`, which makes
`time.Time` values round-trip through the file. Both leave your DSN alone
when it already sets the same option.

Only actual DSN parameters select those policies: MySQL credential text does
not disable `parseTime`, and SQLite filename text does not disable
`_time_format`. Explicit parameter values, including `parseTime=false`, are
preserved. See the [driver profiles](drivers.md) before transferring a native pool.

> [!NOTE]
> A private SQLite database in memory exists per connection, so the SQLite
> driver defaults recognized memory DSNs to a single connection. An explicit
> shared-cache memory URI can share data; use `:memory:` for an isolated test.
> For a file database,
> set `busy_timeout` as shown above, so
> writers wait for each other instead of failing at once with "database is
> locked".

## Starting and stopping

The facade has no lifecycle of its own. The driver owns the connection pool,
and it has three methods that fit the application lifecycle described in
[Lifecycle and ownership](../application/lifecycle-and-ownership.md): `Start`
verifies that the database is reachable, `Stop` closes the pool, and `Ping`
checks that the connection is still alive.

Register them inside the composer passed to `app.Run(ctx, compose)`. Import
`arkoren.dev/core/application`; `driver` is the already-constructed driver:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	composer.Check("database", driver.Ping)
}
```

If the database is down when the service starts, `Start` fails and the
application stops with a clear error before it serves traffic. While the
application runs, the check keeps the readiness probe honest: when the
database stops answering, the service reports that it is not ready and your
platform can route traffic elsewhere.

Without an application, for example in a script or a test, you do not need to
call `Start`. The pool opens lazily and the first query connects. Call
`driver.Stop(ctx)` when you are done to close it.

## Tuning the connection pool

Every connection to a server costs memory on the server, and servers accept a
limited number of them. The driver therefore keeps a **pool** of connections
and lends one to each query. To cap a PostgreSQL pool, import
`arkoren.dev/driver/postgres/postgresdatabase` and pass its configured `dsn`:

```go
driver, err := postgresdatabase.New(dsn,
	func(connection *postgresdatabase.Builder) {
		connection.MaxOpenConns(20)
	},
)
```

`MaxOpenConns` caps the connections in use at once. When every connection
is busy, the next query waits for one, so the cap is also a backpressure
limit. Pick it so that all instances of your service together stay below the
server's connection limit. Other settings tune connection retention:
`MaxIdleConns` is how many connections stay
open while idle, which saves reconnecting under steady traffic.
`ConnMaxLifetime` and `ConnMaxIdleTime` retire connections that are old or idle, which helps when a
proxy or firewall closes quiet connections.

Zero preserves native defaults: database/sql permits unlimited open connections;
pgx defaults to max(4, CPUs), one-hour lifetime and 30-minute idle age.
MaxIdleConns affects only database/sql pools; PostgreSQL accepts it for symmetry
with no effect. A negative value makes New return an error. A transaction holds
one connection for its whole length, so keep
transactions short, as [Transactions](transactions.md) explains.

## Running queries

The facade has four ways to run a statement, and each takes a `context.Context`
so a canceled request stops its query. Describe the rows you expect with a
struct whose fields carry `db` tags.

The fragments below assume that `ctx` is your operation's context, `db` is the
facade constructed above, and `query` is imported from
`arkoren.dev/foundation/query`. Create the `users` table through your
[migrations](migrations.md) before running them:

```go
type User struct {
	ID    int64  `db:"id"`
	Email string `db:"email"`
	Name  string `db:"name"`
}
```

### Reading several rows

`Select` returns every matching row. The type argument says what to scan
into, and the result is a collection you can range over or transform, as
described in [Collections](../collections/overview.md):

```go
users, err := db.Select[User](ctx,
	query.Table("users", func(statement *query.Builder) {
		statement.Select("id", "email", "name")
		statement.Where("active", true)
		statement.OrderBy("email")
	}))

for _, user := range users.Items() {
	fmt.Println(user.Email)
}
```

When no row matches, `Select` returns an empty collection and no error.

At the raw driver boundary, all three shipped `Driver.Select` implementations
replace existing destination-slice elements after a successful query; an empty
result leaves a zero-length destination instead of retaining old rows. They do
not append a new result to a previous one.

### Reading one row or value

`Find` returns exactly one row, the first if several match. Without an explicit
order, that first row is unspecified; an equality on a unique key avoids ties:

```go
user, err := db.Find[User](ctx, query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "email", "name")
	statement.Where("id", 7)
}))
```

`Find` also reads a single value. Scan into a plain type when the query
selects one column:

```go
total, err := db.Find[int64](ctx,
	query.Table("users", func(statement *query.Builder) {
		statement.SelectRaw("COUNT(*)")
	}))
```

### Writing data

`Exec` runs a statement that changes data and returns the number of rows it
changed:

```go
changed, err := db.Exec(ctx,
	query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
		statement.Update(map[string]any{"active": false})
	}))
```

### Streaming rows

`Cursor` reads rows one at a time instead of loading them
all, which suits large results. It is covered in [Pagination](pagination.md).

## Mapping rows to structs

A column fills the field whose `db` tag has the same name. A field without a
tag maps to its lowercase name, so `Email` matches a column called `email`.
Use `db:"-"` to skip a field, and embed a struct to share fields between
types:

```go
type Timestamps struct {
	CreatedAt time.Time `db:"created_at"`
}

type Post struct {
	ID    int64  `db:"id"`
	Title string `db:"title"`
	Timestamps
}
```

Every column in the result needs a field. A query that returns a column the
struct lacks fails with `query.ErrUnknownColumn`, which is how a renamed
column is caught instead of silently ignored. This is why `SELECT *` is best
kept for structs that mirror the whole table. When the struct holds only some
of the columns, list them with `Select("id", "title")`.

### NULLs and JSON columns

A column that can be `NULL` needs a field that can hold it. Use a pointer, or
one of the `sql.Null` types from the standard library:

```go
type Author struct {
	ID    int64          `db:"id"`
	Email *string        `db:"email"`
	Bio   sql.NullString `db:"bio"`
}
```

Scanning `NULL` into a plain `string` fails, which is again the point: the
type says the column may be missing.

To store a Go value in a JSON column, use `database.JSON`:

```go
type Account struct {
	ID       int64                         `db:"id"`
	Settings database.JSON[map[string]bool] `db:"settings"`
}
```

PostgreSQL, MySQL, and SQLite hand JSON columns back as different Go types,
and `database.JSON` hides that: it accepts both, and writes the encoded
document as a string that every database stores in `json`, `jsonb`, or text
columns. Read the value from its `V` field. Pass a `database.JSON` value as a
query argument or in an `Insert` to write it.

`database.JSON[T]` uses `encoding/json/v2`. For zero values, nil pointers encode
as JSON `null`, nil maps as `{}`, nil ordinary slices as `[]`, and nil byte
slices as an empty base64 JSON string (`""`). `Value` returns a JSON document
string, not SQL NULL. A SQL NULL scan resets `V` to its zero value; an invalid
source or decode error leaves its previous value unchanged. Custom JSON methods
can choose another valid representation or return an error.

## Handling errors

`Find` fails with `database.ErrNoRows` when nothing matches. Check for it with
`errors.Is`, and decide whether a missing row is an error in your domain:

```go
user, err := db.Find[User](ctx, query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "email", "name")
	statement.Where("id", id)
}))
if errors.Is(err, database.ErrNoRows) {
	return ErrUserNotFound
}
```

The error also matches `sql.ErrNoRows`, so code written for `database/sql`
keeps working. Other errors come from the database or the Go driver, for
example a constraint violation or a canceled context, and the facade returns
them as they are so you can inspect them with `errors.Is` and `errors.As`. An
invalid statement, such as a column name that is not a valid identifier,
fails before any query runs with an error that matches `query.ErrInvalidQuery`.

## The dialect is a value

Databases disagree on small things: PostgreSQL numbers placeholders as `$1`,
MySQL quotes names with backticks, SQLite has no row locks. Each driver
reports its **dialect**, and the facade compiles every builder for it. The
dialect is an ordinary runtime value, not a type parameter, so the same code
can run against different databases:

```go
if db.Dialect() == query.SQLite {
	// skip the part that needs row locks
}
```

Most code never looks at it. It matters when you write SQL that only one
database understands, as [Raw SQL and locks](raw-sql-and-locks.md#dialect-differences)
shows.

## One database per driver

Create exactly one `*database.Database` for each driver and share it. A
transaction travels in the `context.Context` and is joined by facades over the
same driver, so a second facade over the same driver works, but a facade over
another driver, such as a second PostgreSQL database, does not join. Keeping
one facade per driver avoids the confusion. To work with several databases,
see [Multiple databases](multiple-databases.md).

The facade also records a client span and a duration metric for every
query or lock acquisition when telemetry is on, without telemetry allocations
when it is off.
[Events and telemetry](query-monitoring.md) describes what it reports.

## Where to go next

With a connection in place, [Query builder](query-builder.md) shows how to
express statements, [Schema builder](schema-builder.md) and
[Migrations](migrations.md) create and evolve the tables they read, and
[Transactions](transactions.md) groups statements into one unit of work.

For every function and option, see
[the `database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
