---
title: Raw SQL and Locking
nav: Raw SQL & Locking
description: Run hand-written SQL through the facade, mix raw fragments into the builder, lock rows for update, and take named locks across processes.
order: 30
---

# Raw SQL and Locking

## Introduction

The [query builder](query-builder.md) covers the statements most code needs,
but SQL is larger than any builder. A recursive query, a window function, a
vendor-specific operator, or a hand-tuned report is easier to write as SQL
than to describe with method calls. Arkoren lets you drop to raw SQL at any
point, and the facade runs it the same way it runs a builder: with the same
transaction, the same telemetry, and the same row scanning.

Raw statements supply a whole query; raw fragments keep the rest in the builder.
When the work also needs coordination, choose the lock by what it protects.
**Row locks** make a query claim the rows it reads until the transaction ends.
**Named locks** coordinate work by name, for work that only one instance of your
service should do at a time. Their cross-process reach depends on the driver.

## Raw statements

`query.Raw` wraps a complete SQL statement and its arguments. The facade
accepts it wherever it accepts a builder. Use the configured `db`, `ctx`, and
`User` from the [database overview](overview.md), import
`arkoren.dev/foundation/query`, and supply your cutoff time as `since`:

```go
admins, err := db.Select[User](ctx,
	query.Raw("SELECT id, email FROM users WHERE role = ? AND created_at > ?", "admin", since))

_, err = db.Exec(ctx,
	query.Raw("UPDATE accounts SET balance = balance - ? WHERE id = ?", 100, 1))
```

Write `?` for every value, on every database. The facade rewrites the
placeholders to what the driver expects, such as `$1` and `$2` on PostgreSQL,
so you do not change placeholder syntax for each driver. This does not make
vendor-specific SQL portable. The number of placeholders must equal the
number of arguments, or the statement fails before it reaches the database.
A question mark inside a supported quote or comment is copied, not counted.
Outside those spans, write `??` for a literal question mark, such as
PostgreSQL's `jsonb` operator.

### Placeholder scanning

The scanner understands a limited SQL text subset, not every dialect's lexer:

- Single-quoted strings, double-quoted text, and backtick-quoted text are copied
  unchanged. Adjacent doubled quote characters stay inside quoted text for
  placeholder handling.
- `/* ... */` block comments nest on ANSI and PostgreSQL. On MySQL and SQLite,
  the first `*/` ends the comment.
- `--` line comments end at a carriage return (CR) or line feed (LF) on
  PostgreSQL. The other dialects use LF. On MySQL, `--` starts a comment only
  when followed by whitespace or a control character; `--?` is not a comment.

Backslash-escaped string literals and PostgreSQL dollar-quoted text are not
understood. Do not place those constructs in SQL passed through the scanner;
bind their contents as data where possible. Compiling raw SQL checks nonblank
text and bindings, not whether the database accepts its grammar or operators.
An unterminated quote or comment is not a SQL syntax error the compiler detects.

The facade does not classify whether raw SQL returns rows or validate its names.
Choose `Exec`, `Select`, or `Find` for the result you expect. Keep the SQL text
trusted and bind variable values. For a request-derived `email`:

```go
// Never: user input becomes part of the SQL.
query.Raw("SELECT * FROM users WHERE email = '" + email + "'")

// Always: the value travels separately from the SQL.
query.Raw("SELECT * FROM users WHERE email = ?", email)
```

### When to use raw SQL

Prefer the builder when it can express the statement, because it quotes names
for each dialect and rejects mistakes before the query runs. Reach for raw SQL
when the statement is the point: a common table expression, a window function,
a `LATERAL` join, a database function, or a query you have tuned by
hand and want to keep readable.

PostgreSQL `COPY` streaming uses a native protocol rather than the facade's row
interface. Use the driver's [native bulk copy](drivers.md#using-native-postgresql-operations)
for that task.

Between the two there is a middle path. You rarely need to give up the
builder for one awkward expression, because it has raw escape hatches for
fragments.

### Raw sources and fragments

You may only need a database expression to extract a year or compare a
lowercased title; the rest of the query can stay in the builder. Add that one
expression with `SelectRaw`, `WhereRaw`, `HavingRaw`, or `SetRaw`. Keep the SQL
text fixed. `WhereRaw`, `HavingRaw`, and `SetRaw` accept arguments through `?`
placeholders and use the scanner described above. This example uses the
PostgreSQL/MySQL `EXTRACT` expression; raw expression syntax is not translated
for SQLite:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.SelectRaw("EXTRACT(YEAR FROM created_at) AS year")
	statement.WhereRaw("lower(title) LIKE ?", "%go%")
})
```

`SelectRaw` is different: it has no arguments, copies its expression verbatim,
and does not scan placeholders or turn `??` into `?`. Do not put bind parameters
there. For PostgreSQL's JSON operator in a selection, write a single `?`:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.SelectRaw("meta ? 'featured' AS featured")
	statement.Where("published", true)
})
```

The selection keeps `meta ? 'featured'` exactly as written; only the ordinary
`Where` value becomes a bound argument. In an argument-binding raw condition,
the same operator instead needs `??`, as in `WhereRaw("meta ?? ?", "featured")`.

Keep fragment text trusted and constant; pass variable values as bound
arguments. A raw condition retains SQL operator precedence. When you apply
[scopes](query-builder.md#reusing-conditions-with-scopes), the builder wraps
the entire nonempty user condition chain before ANDing each grouped scope.
An OR inside `WhereRaw` therefore cannot bypass a scope on a read, update,
delete, or count. This grouping does not validate arbitrary SQL, and a whole
`query.Raw` statement has no builder scopes.

`FromRaw` starts a `SELECT` from a raw subquery, so you can write the hard part
as SQL and still filter, order, and paginate it with the builder:

```go
report := query.FromRaw(query.Raw(`SELECT author_id, COUNT(*) AS posts
FROM posts WHERE created_at >= ? GROUP BY author_id`, since), func(statement *query.Builder) {
	statement.As("r")
	statement.Where("posts", ">", 5)
	statement.OrderByDesc("posts")
	statement.OrderBy("author_id")
})
```

The subquery is rendered as `FROM (...) AS "r"`, named by `As`; the default
alias is `t`. This is how you paginate a raw report, as shown in
[Pagination](pagination.md). A builder with a raw source can only select, so
it cannot become an `INSERT`, `UPDATE`, or `DELETE`.
The source's arguments bind before outer conditions. PostgreSQL placeholder
numbering continues across the source and outer query; a named raw source keeps
the named-binding behavior described below.

## Named parameters

Positional `?` placeholders get unwieldy in a long statement. `query.RawNamed`
takes `:name` placeholders instead, with the values in a map:

```go
admins, err := db.Select[User](ctx, query.RawNamed(
	"SELECT id, email FROM users WHERE role = :role AND created_at > :since",
	map[string]any{"role": "admin", "since": since}))
```

The facade runs a named statement like any other, inside the context's
transaction and with telemetry. Each `:name` becomes the driver's placeholder
and binds the map entry of that name; a name may appear more than once.
Bindings follow the names' order in the SQL, not map iteration order, and a
repeated name binds its value again at each occurrence. A name starts with an
ASCII letter or underscore, followed by ASCII letters, digits, or underscores.
Entries that no placeholder uses are ignored, so the map from
[`query.Values`](query-builder.md#writing-structs) can be passed whole:

```go
_, err = db.Exec(ctx, query.RawNamed(
	"UPDATE users SET name = :name, email = :email WHERE id = :id",
	query.Values(user)))
```

A placeholder without a map entry fails the statement before it reaches the
database, with an error that wraps `query.ErrInvalidQuery`. Quotes and comments
follow the [same scanner rules](#placeholder-scanning) as `query.Raw`. `::` is
kept as written so PostgreSQL casts such as `id::text` work, and a `?` is plain
text in a named statement.
There is no `??` escape in named mode: question marks are copied as written.
`Raw` copies its argument slice and `RawNamed` copies its map at construction;
objects stored as argument values remain shared rather than recursively copied.

## Owning a raw cursor

The facade's lazy `Cursor` closes its rows on completion or consumer stop. It
joins close errors into a final iteration error while the consumer still accepts
results; after consumer stop, close failures can only reach telemetry. See
[streaming error handling](pagination.md#reading-a-single-query-as-a-stream).

If your caller must receive an early-close error, use a raw driver's `Query` and
explicitly combine the iteration result with `Rows.Close`, for example with
`errors.Join`. Compile the definition for that driver's dialect first and pass
its `SQL()` and `Args()`; raw driver calls do not rewrite placeholders for you.
Close rows on every path, including early stop and panic cleanup.

`db.Driver()` is the root driver, not the one selected by a facade session or
transaction context. It deliberately bypasses those scopes and facade telemetry.
When using the raw `Driver.WithSession` or `Driver.WithTransaction` contracts,
use the callback's supplied driver for scoped queries instead; do not retrieve
the root again. Never keep that handle or its cursor after the callback ends.

## Dialect differences

The builder hides most differences between databases, and `query.Raw` hides
the placeholder syntax. What remains is the SQL itself, which is yours. The
facade reports the driver's dialect, so you can choose between versions when
a statement differs. Here `Store` is your application type with a
`db *database.Database` field; the implementation also uses the standard-library
`context` and `fmt` packages. Supply a positive retention period in `days`:

```go
func (store *Store) Purge(ctx context.Context, days int) error {
	var statement query.Statementer

	switch store.db.Dialect() {
	case query.Postgres:
		statement = query.Raw("DELETE FROM events WHERE created_at < now() - make_interval(days => ?)", days)
	case query.MySQL:
		statement = query.Raw("DELETE FROM events WHERE created_at < NOW() - INTERVAL ? DAY", days)
	case query.SQLite:
		statement = query.Raw("DELETE FROM events WHERE created_at < datetime('now', ?)", fmt.Sprintf("-%d days", days))
	default:
		return fmt.Errorf("purge requires PostgreSQL, MySQL, or SQLite")
	}

	_, err := store.db.Exec(ctx, statement)
	return err
}
```

Keep a statement like this in one function, so the rest of your code stays
portable. A project that runs SQLite in tests and PostgreSQL in production
benefits from keeping such functions few, since a test then exercises a
different statement than production does.

The builder has its own limits per dialect, and it tells you at build time
rather than at the database:

- `Returning` works on PostgreSQL and SQLite, and fails on MySQL.
- Upserts work on PostgreSQL, SQLite, and MySQL, and fail on ANSI.
- `ILIKE` works on PostgreSQL only.
- `IS DISTINCT FROM` and `IS NOT DISTINCT FROM` fail on MySQL, including their
  use in column comparisons and joins. Raw text does not get this operator check.
- Row locks work on PostgreSQL and MySQL 8, and fail on SQLite.

Each of these failures matches `query.ErrUnsupported`, so a test on SQLite
catches a PostgreSQL-only feature only if it runs that code path. Compile the
statement for the production dialect with `BuildFor(query.Postgres)` in a unit
test to check it without a server.
This verifies compilation, not vendor execution or SQL hidden inside raw text.

## Locking rows

When two requests read a row and then update it, both can act on the same old
value. A **row lock** prevents that: the database holds locks on the rows a query
read until the transaction ends, and conflicting writes or locking reads wait.
Ordinary snapshot reads do not necessarily wait.
`ForUpdate` locks the rows for writing, and `ForShare` lets other readers lock
them too but blocks writers:

```go
query.Table("accounts", func(statement *query.Builder) {
	statement.Where("id", 1)
	statement.ForUpdate()
})
```

On PostgreSQL, combining either lock mode with structured `Distinct`, `GroupBy`,
or `Having` clauses fails compilation with `query.ErrUnsupported`. Raw selection
expressions are opaque: the compiler does not detect an aggregate hidden inside
`SelectRaw`, so compilation is not proof that such a locking query can execute.

A lock only lasts as long as its transaction, so run a locking query inside
`WithTransaction` and use the context it gives you, as [Transactions](transactions.md)
describes.

Here `Account` is your row type with a numeric `Balance` field; `id` and `amount`
are application inputs, with `amount` in the same units as `Balance`:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	account, err := db.Find[Account](ctx, query.Table("accounts", func(statement *query.Builder) {
		statement.Where("id", id)
		statement.ForUpdate()
	}))
	if err != nil {
		return err
	}

	account.Balance -= amount
	_, err = db.Exec(ctx, query.Table("accounts", func(statement *query.Builder) {
		statement.Where("id", id)
		statement.Update(map[string]any{"balance": account.Balance})
	}))
	return err
})
```

Outside a transaction the lock is released as soon as the statement ends, so
it protects nothing.

Two modifiers decide what happens when a row is already locked. By default the
query waits. `NoWait` fails at once instead, and `SkipLocked` skips the locked
rows and returns the others. `SkipLocked` is the building block for a job
queue, because many workers can each claim a different row without waiting
for one another. Use the transaction's `ctx` here, and change the job's state
before committing so the next worker cannot claim it again:

```go
job, err := db.Find[Job](ctx, query.Table("jobs", func(statement *query.Builder) {
	statement.Where("state", "ready")
	statement.OrderBy("id")
	statement.Limit(1)
	statement.ForUpdate()
	statement.SkipLocked()
}))
```

```sql
SELECT * FROM "jobs" WHERE "state" = $1 ORDER BY "id" ASC LIMIT 1 FOR UPDATE SKIP LOCKED
```

When every ready job is taken, `Find` returns `database.ErrNoRows`. Both
modifiers require `ForUpdate` or `ForShare`, and they cannot be combined.
SQLite has no row locks and fails with `query.ErrUnsupported`; its writers
lock the whole database instead.

## Named locks

Some work must run in one place at a time, though nothing in the database
represents it: a nightly report, a cache rebuild, a migration. For these,
`Lock` takes a lock by name. It blocks until the lock is held, and returns a
function that releases it. Here `buildReport(context.Context) error` is your
application's report function:

```go
unlock, err := db.Lock(ctx, "nightly-report")
if err != nil {
	return err
}
defer unlock(ctx)

return buildReport(ctx)
```

Locks are exclusive and not reentrant, so a second `Lock` with the same name
waits, even in the same goroutine, until the first is released. The unlock
function may be called more than once. If `ctx` ends while waiting, `Lock`
returns an error that matches the context's error, so a lock request never
hangs past a timeout you set:

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()

unlock, err := db.Lock(ctx, "nightly-report")
if errors.Is(err, context.DeadlineExceeded) {
	return nil // another instance is already running the report
}
if err != nil {
	return err
}
defer unlock(ctx)
```

An empty name is a programming error: `Lock` fails with
`database.ErrEmptyLockName`. Any other name is valid at any length.

### Skipping work another instance is doing

When one instance running the work is enough and the others should not wait,
use `TryLock`. It returns at once with `acquired` set to false, and a nil
error, if another holder has the lock. This is the shape of a periodic job that
every instance schedules but only one runs at a time.

`Reports` is your application type with a `db *database.Database` field and a
`build(context.Context) error` method. The example uses `context` and `errors`
from the standard library, and preserves a lock-release error alongside a
report error:

```go
func (reports *Reports) Nightly(ctx context.Context) (err error) {
	unlock, acquired, err := reports.db.TryLock(ctx, "nightly-report")
	if err != nil {
		return err
	}
	if !acquired {
		return nil // another instance is running the report
	}
	defer func() {
		err = errors.Join(err, unlock(ctx))
	}()

	return reports.build(ctx)
}
```

`TryLock` takes the same lock as `Lock` under the same rules: names, scope,
exclusivity, and idempotent unlock are identical, and a `Lock` waits for a
`TryLock` holder and the reverse. An empty name still fails with
`database.ErrEmptyLockName`, and an ended `ctx` is an error, not a refusal.
When `acquired` is false the returned unlock is nil, so check `acquired`
before calling it. The lock is not queued: a second instance that skips this
run does not run it later, so schedule the job often enough that a skipped run
is harmless.

"Without waiting" concerns lock contention. Acquiring a pooled connection and
network I/O can still wait under the supplied context. Inside a session or
transaction, the driver uses the existing connection, never a second one.

### Keeping protected SQL on the lock-owning session

A **session** is one retained physical connection. Use `WithSession` when the
lock protects database work that must stop if its lock-owning connection dies:

```go
err := db.WithSession(ctx, func(ctx context.Context) error {
	_, acquired, err := db.TryLock(ctx, "nightly-report")
	if err != nil || !acquired {
		return err
	}
	return buildReport(ctx)
})
```

All facade SQL receiving this context, including transactions, uses the same
connection as the lock. The callback automatically releases its remaining locks
and closes outstanding cursors before returning the connection. Connection loss
fails subsequent work; it never reconnects or replays a stale plan. Returning an
error does not undo autocommit statements or earlier committed transactions.

#### Nested sessions and callback lifetime

Nested sessions borrow the connection but own their new nontransactional locks:
those locks end with the inner callback, while parent locks remain held. Sessions
inside a transaction preserve it, and new locks then belong to the outermost
transaction. Taking a held name again is not reentrant, even on that connection.

Use the callback context synchronously from one goroutine. It and its driver and
cursors expire on return (`database.ErrSessionDone`); transaction callbacks use
`sql.ErrTxDone`. Native access and `db.Driver()` deliberately bypass both scopes.
Arbitrary raw SQL session state is not automatically reset.

#### Pool capacity and contention

Raw held PostgreSQL/MySQL locks each reserve a pool connection until unlock.
Root waiters release contended connections between attempts, but a pinned
session already reserves its connection and cannot release it while waiting.
When mixing scoped waiters with raw holders that need query capacity, use
`TryLock` and end a contended session before retrying outside it, as above. Raw
holders can still reserve every connection; increasing the pool is not a
guarantee of deadlock freedom. Lock acquisition has no FIFO fairness guarantee.

### What a lock covers

The reach of a lock depends on the driver, because each database offers a
different mechanism:

- **PostgreSQL** uses advisory locks, which belong to the database. They
  coordinate every process and every instance that connects to it. A
  connection pooler in transaction mode, such as PgBouncer, can send the lock
  and its release to different server connections, so connect directly, or use
  session mode, for services that use locks.
- **MySQL** uses `GET_LOCK`, which also spans every instance on the server. A
  name longer than 64 bytes, MySQL's limit, is hashed to fit.
- **SQLite** takes a lock shared by everything in the same process. For a
  database stored in a file, on Linux, macOS, and the BSDs, it also takes a
  file lock beside the database file, so processes on the same host
  coordinate. An in-memory database and Windows get the in-process lock only.
  Do not rely on cross-process coordination on network file systems.

If the process dies or the connection drops, PostgreSQL and MySQL release the
lock, since it belongs to the session.

### Locks inside a transaction

With a context from `WithTransaction`, `Lock` and `TryLock` tie the lock to the
transaction. It is held until the transaction commits or rolls back, whichever
the database, and the returned unlock does nothing. This is useful when the
lock guards the data the transaction changes:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := db.Lock(ctx, "invoice-numbers"); err != nil {
		return err
	}

	// Only one transaction at a time runs this section.
	return assignNextInvoiceNumber(ctx)
})
```

A lock taken inside a nested transaction is handed to the outermost one, so
it is held until the whole unit of work ends.

A `TryLock` inside a transaction that finds the lock held elsewhere returns
`acquired` false and leaves the transaction usable.

Taking a lock records a `LOCK` span (`TRY_LOCK` for `TryLock`) when telemetry
is on; releasing it does not. See [Events and telemetry](query-monitoring.md).

For every function and option, see
[the `database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database) and
[the `query` API reference](https://pkg.go.dev/arkoren.dev/foundation/query).
