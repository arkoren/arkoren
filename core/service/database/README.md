# Arkoren Database

`arkoren.dev/core/service/database` defines the SQL `Driver` contract and the typed
`Database` facade. It uses Arkoren's foundation modules and core services,
with no third-party dependencies.

Applications select an independently versioned driver module, then create a
facade with `database.New(driver)` for typed `Find`, `Select`, `Cursor`,
`Exec`, `Paginate`, `CursorPaginate`, `Each`, and transactions.

## Installation

From your application's module directory, install core and the selected driver:

```sh
go get arkoren.dev/core
go get arkoren.dev/driver/sqlite
```

Import `arkoren.dev/core/service/database`, `arkoren.dev/foundation/query`, and
your selected driver package, such as `arkoren.dev/driver/sqlite/sqlitedatabase`.
The [database guide](../../../cmd/arkoren/skills/arkoren/references/guides/database/overview.md)
shows construction, lifecycle wiring, and your first migrated tables.

## Query builder

`Exec`, `Select`, `Find`, and `Cursor` accept any `query.Statementer`: a
`query.Query`, or `query.Raw(sql, args...)` for SQL the builder cannot
express. They compile it for the driver's dialect. Drivers report their
dialect by implementing `Dialect()` on the `Driver` interface.
The bundled Postgres, MySQL, and SQLite drivers report theirs.

Raw SQL always uses `?` placeholders; the dialect rewrites them (`$n` on
Postgres). `Select`, `Find`, and `Cursor` reject a published query that returns no
rows (an INSERT, UPDATE, or DELETE without `Returning`) with
`query.ErrInvalidQuery`. A `RawStatement` is opaque and always accepted.

The examples use `db := database.New(driver)` with your selected SQL driver,
your `User` and `Event` row types, and an I/O context `ctx`. To collect active
users, call `Select`:

```go
users, err := db.Select[User](ctx, query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "name")
	statement.Where("active", true)
}))
```

To fetch one row using hand-written SQL, pass its values separately:

```go
user, err := db.Find[User](ctx, query.Raw("SELECT * FROM users WHERE id = ?", id))
```

To change a row, call `Exec`; `affected` reports the affected-row count:

```go
affected, err := db.Exec(ctx, query.Table("users", func(statement *query.Builder) {
	statement.Where("id", id)
	statement.Update(map[string]any{"name": name})
}))
```

To traverse results without collecting them all, create a cursor:

```go
events := db.Cursor[Event](ctx, query.Table("events"))
```

`Select` collects rows; `Cursor` returns a lazy sequence whose `Iter` method
yields rows and errors as they are read.
The cursor joins iteration/scan failures with close failures before yielding
the final error. If you stop iteration early, it still closes rows, but cleanup
failure can only reach telemetry: an iterator cannot yield after you stop it.
Use `db.Driver().Query` and explicitly handle `Rows.Close` when your application
must return an early-stop cleanup error; that raw path bypasses facade scopes.

For hand-written SQL with many parameters, `query.RawNamed` binds `:name`
placeholders from a map, such as one built by `query.Values`, and runs
through the facade like any other statement:

```go
_, err := db.Exec(ctx, query.RawNamed(
	"UPDATE users SET name = :name WHERE id = :id", query.Values(user)))
```

## Pagination and batching

`Paginate`, `CursorPaginate`, and `Each` take a `query.Query` (not raw SQL
text) and read its orders with `Inspect`. Raw SQL stays available as a source
through `query.FromRaw`, or through `db.Driver()`.

The following examples use your `Post` row type with `id`, `title`, and
`created_at` columns. To show numbered pages, read parameters from an existing
HTTP request `req`, then call `Paginate`. It counts with `Query.Count` and
fetches the page:

```go
params, err := request.Page(req, pagination.WithMaxPerPage(50))
page, err := db.Paginate[Post](req.Context(), query.Table("posts", func(statement *query.Builder) {
	statement.Where("published", true)
	statement.OrderByDesc("created_at")
	statement.OrderBy("id")
}), params)
```

For next/previous navigation without a count query, use `CursorPaginate`.
It fetches one extra row to detect another
window, and encodes the ordered column values into `next_cursor` and
`prev_cursor`:

```go
params, err := request.Cursor(req)
feed, err := db.CursorPaginate[Post](req.Context(), query.Table("posts", func(statement *query.Builder) {
	statement.OrderByDesc("created_at")
	statement.OrderByDesc("id")
}), params)
```

A bad or mismatched cursor returns a 400 problem. To paginate in a job without
HTTP, build the parameters directly; `Normalize` is applied for you:

```go
page, err := db.Paginate[Post](ctx,
	query.Table("posts", func(statement *query.Builder) {
		statement.OrderBy("id")
	}),
	pagination.PageParams{Page: 2, PerPage: 50})
```

For a grouped report, use `FromRaw` as the source. It takes `?` placeholders on
every dialect. Here your `Row` type has `AuthorID` and `Posts` integer fields
tagged `db:"author_id"` and `db:"posts"`, and `params` is a `pagination.PageParams`:

```go
report, err := db.Paginate[Row](ctx,
	query.FromRaw(query.Raw(`SELECT author_id, COUNT(*) AS posts
FROM posts WHERE created_at >= ? GROUP BY author_id`, since), func(statement *query.Builder) {
		statement.As("r")
		statement.OrderByDesc("posts")
		statement.OrderBy("author_id")
	}),
	params)
```

To process a large export in bounded memory, use `Each`. It reads batches of
500 rows with keyset queries:

```go
posts := db.Each[Post](ctx, query.Table("posts", func(statement *query.Builder) {
	statement.OrderBy("id")
}), 500)
```

Like `Cursor`, `posts.Iter()` yields each row with an error result. Stop on an
iteration error before processing that row.

Errors: `ErrNoOrder` (no `OrderBy`), `ErrCursorColumn` (an ordered column has no
field in `T`), `ErrCursorValue` (a key value is NULL or unusable), and
`ErrInvalidBatchSize` (`Each` size below one). A malformed cursor, different
ordering, or invalid signature fails with `pagination.ErrCursorDecode`.
Unsigned tokens can be edited into another valid position.

Cursors are plain base64 JSON by default, so a client can forge a position (only the position;
the query still applies). To sign them, build a codec and set `Builder.CursorCodec`:

```go
codec, err := pagination.NewCursorCodec(secret, oldSecret) // validated key.Shared values; exactly 32 bytes each
if err != nil {
	return err // Do not silently fall back to unsigned cursors.
}
db := database.New(driver, func(connection *database.Builder) {
	connection.CursorCodec(codec)
})
```

A database with a codec rejects unsigned cursors, so enabling it invalidates cursors clients hold.
Signing protects integrity, not secrecy or authorization. Cursors do not bind a
user, tenant, filter, dataset, or expiry; retain authorization filters on every
query. Both encoders reject tokens above the decoders' 4096-byte complete-token
limit, including the signature when signing is enabled.

Caveats:

- The last `OrderBy` column must be unique (usually `id`), or rows that share
  the other keys can repeat or vanish between pages. Uniqueness is your
  responsibility; the facade does not verify it.
- Ordered columns must be fields of `T`, matched by `db` tag (or lowercase
  field name). A qualified column such as `p.id` matches `id`.
- NULL keys are unsupported: use non-nullable ordered columns or explicitly
  filter NULL rows out of the selected dataset. Every fetched key is checked,
  including lookahead and short final batches. These checks cannot discover
  NULL partitions already excluded by a seek. SQL nullable values retain their
  `driver.Valuer` method sets for non-NULL continuation binding.
- All three methods need a published SELECT query. They derive their own `Limit`
  (`Paginate` also `Offset`), so the builder must not have either: a preset
  `Limit` or `Offset` fails with `database.ErrPaged` before any query runs
  (`Each` yields it as its first item).
- `FromRaw` queries are wrapped as subqueries. Optimizer barriers inside the
  raw SQL (`GROUP BY`, `DISTINCT`, `LIMIT`) can force the database to
  materialize the whole result, which slows deep pages and `Count`.
- `Each` runs separate queries, not one snapshot. A consistent view requires a
  transaction with snapshot-preserving isolation; PostgreSQL read committed still
  takes a new snapshot per statement. Cancellation is checked between buffered
  rows, not only before the next query.
- `Paginate` and `CursorPaginate` reapply fixed default limits (100 rows per page,
  and page 10,000 for offset pages). Higher request-parser limits do not raise
  those facade limits.

Build errors are returned before the driver is called.

## Telemetry privacy

Database spans include bounded operation labels and known table names, not
bound arguments or error messages. Query text is disabled by default. If you
enable `connection.QueryText(true)` in a construction callback, spans include
compiled SQL verbatim. SQL literals and comments are not redacted, even when
bound arguments use placeholders. Enable text only for statements safe to
export to your telemetry backend.

## Transactions

`WithTransaction` puts the transaction in the context it hands to `fn`. Keep
using the `*Database` you already have, and pass that context. Here `insertOrder`
is your insert query and `orders.Reserve` is your application's reservation
operation using the same database:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := db.Exec(ctx, insertOrder); err != nil {
		return err // rolls back
	}

	return orders.Reserve(ctx, items) // joins this transaction
})
```

- `fn` returning nil commits. An error rolls back and is returned. A panic
  rolls back and is re-raised.
- `Exec`, `Select`, `Find`, `Cursor`, `Paginate`, `CursorPaginate`, `Each`,
  and `Lock` run in the transaction their context carries, when it belongs to
  the same driver. A call made with the outer `ctx` runs outside it.
- Create one `*Database` per driver. Two `*Database` values on one driver
  share transactions; a `*Database` on another driver does not join.
- A `WithTransaction` inside a transaction is a nested transaction. It rolls
  back only its own work, and that work is final only when the outermost
  transaction commits. The SQL drivers implement it with savepoints.
  `ErrNestedTransaction` means the driver cannot nest.
- Using the context after the transaction ends fails with `sql.ErrTxDone`.
  A transaction is one connection: do not use its context from several
  goroutines at once.
- Synchronous code that receives the context, such as `event.Bus` listeners,
  joins the transaction. A goroutine that outlives it gets `sql.ErrTxDone`.
- `db.AfterCommit(ctx, fn)` runs `fn` after the outermost transaction commits,
  in registration order, with the context the outermost `WithTransaction` was
  called with. An error, panic, or failed commit discards the hooks, and so
  does a rollback of the nested transaction that registered them. Outside a
  transaction `fn` runs immediately. It is best effort: a crash after the
  commit loses the hooks, so use it for cache invalidation, broadcasts, and
  notifications, not for work that must not be lost. Every hook runs even when
  one fails; their errors are joined and returned by the outermost
  `WithTransaction` wrapped with `database.ErrAfterCommit`, which means the
  transaction committed: don't retry it. A panic in `fn` propagates out of the
  outermost `WithTransaction` and skips later hooks.
- There are no transaction options (read-only, isolation).

## Sessions

`db.WithSession(ctx, fn)` reserves one physical connection without beginning a
transaction. Use its callback context for all protected SQL, locks, and explicit
transactions. Loss of the connection fails subsequent SQL rather than silently
reconnecting or replaying work. An error does not undo earlier autocommit writes.
Nested sessions borrow the connection but expire independently; new locks outside
transactions end with their acquiring callback, while transaction locks last
until the outermost transaction ends. Close cursors before further SQL.

Use each callback synchronously from one goroutine. After return, session contexts
fail with `database.ErrSessionDone`, and transaction contexts with `sql.ErrTxDone`.
`Driver()` and native accessors bypass both scopes and facade telemetry.
See the [session and lock guide](../../../cmd/arkoren/skills/arkoren/references/guides/database/raw-sql-and-locks.md#keeping-protected-sql-on-the-lock-owning-session)
and [API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
