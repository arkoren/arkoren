---
title: Query Monitoring
description: Observe the database with transaction and migration events and per-query spans and metrics, and log slow queries.
order: 90
---

# Query Monitoring

## Introduction

To find slow statements, use telemetry; to react to a completed transaction or
migration, use events. **Events** announce things that
happen once and that your code may want to act on: a transaction committed or
rolled back, a migration was applied or failed. **Telemetry** measures the
operations themselves: a span and a duration for every query, so you can see
which statements are slow and which fail.

Events require an explicit bus. Telemetry uses an explicit service or the
[process default](../observability/telemetry.md#the-process-default), initially a
no-op; changing that default also affects databases constructed without explicit
telemetry. Neither can change the outcome of an operation: a listener that fails is
logged, and telemetry only observes. Neither records
query arguments or the text of database errors, since those hold the data your
application stores. [What is recorded](#what-is-recorded-and-what-is-not) lists
exactly what is.

To enable both, import `arkoren.dev/core/service/database` and pass the existing
`app`'s bus and telemetry to the facade around your configured `driver`.
The [database overview](overview.md) establishes that driver and its lifecycle:

```go
db := database.New(driver, func(connection *database.Builder) {
	connection.Events(app.Events())
	connection.Telemetry(app.Telemetry())
	connection.Namespace("app")
})
```

## Events

An event is a typed value that the database publishes on the bus when something
happens. The database publishes events for transactions, and the migrator
publishes its own.

### Transaction events

The database publishes two events, both about the outermost
[transaction](transactions.md):

| Event | Published | Name |
| --- | --- | --- |
| `database.TransactionCommitted` | after the commit succeeded | `database.transaction.committed` |
| `database.TransactionRolledBack` | when the body, context, or commit failed | `database.transaction.rolled-back` |

Both carry a `database.TransactionEvent` with the `System` (`postgresql`,
`mysql`, or `sqlite`), the `Namespace` you configured, and the `Err` that ended
the transaction, which is `nil` for a commit. A transaction rolls back because
its function returned an error, its context ended, or the commit failed. A panic
rolls back too, but it continues to the caller instead of becoming an event.
Driver cleanup failure after a confirmed commit returns `database.ErrAfterCommit`
but still publishes the committed event and runs hooks. A body-provided error
bearing that marker still requests rollback of the current transaction.

Listening is the usual event bus call, covered in [Events](../events/overview.md).
For example, with `logger := app.Logger()`, log every rollback:

```go
_, err := app.Events().Listen(database.TransactionRolledBack,
	func(ctx context.Context, e database.TransactionEvent) error {
		logger.WarnContext(ctx, "transaction rolled back", "database", e.Namespace)

		return nil
	})
```

A few details matter when you write listeners:

- Nested transactions publish nothing. The event describes the unit of work
  that the database actually committed or abandoned.
- A listener runs synchronously, on the goroutine that called
  `WithTransaction`, before it returns. Keep it short, and hand slow work to a
  queue.
- The listener receives the context that was passed to `WithTransaction`,
  outside the transaction, so database calls it makes are not part of the
  finished transaction. Any enclosing session remains active: those calls use
  its retained connection, and the context cannot outlive that session callback.
- A listener's error or panic is logged by the bus and never changes the
  result of the transaction. The commit has already happened by the time a
  listener sees it.
- The committed event is published before the
  [after-commit hooks](transactions.md#running-code-after-commit) run.

The payload carries the error but no statements or values. If you log `e.Err`,
remember that database errors sometimes quote the offending row, for example
`duplicate key value (email)=(alice@example.com)`; decide what your logs may
hold.

Because events are plain values, they are also the simplest way to count
transactions. A counter built on the application's telemetry turns each
rollback into a metric:

```go
rollbacks := app.Telemetry().Meter("example.com/shop").
	Counter("shop.db.rollbacks", telemetry.Unit("{rollback}"), telemetry.Keys("db.namespace"))

_, err = app.Events().Listen(database.TransactionRolledBack,
	func(ctx context.Context, e database.TransactionEvent) error {
		rollbacks.Add(ctx, 1, telemetry.String("db.namespace", e.Namespace))

		return nil
	})
```

### Migration events

A migrator publishes its own events when you give it the bus with
`migrate.Builder.Events`. `migrate.Applied` follows each migration that `Up` applies,
`migrate.RolledBack` follows each one `Rollback` reverts, and `migrate.Failed`
reports the migration that stopped the run, whichever direction it was going:

```go
migrator := migrate.New(db, func(migrator *migrate.Builder) {
	migrator.Events(app.Events())
})

_, err := app.Events().Listen(migrate.Failed,
	func(ctx context.Context, e migrate.MigrationEvent) error {
		logger.ErrorContext(ctx, "migration failed", "id", e.ID, "batch", e.Batch, "duration", e.Duration)

		return nil
	})
```

The payload is a `migrate.MigrationEvent`: the migration `ID` and `Group`, the
`Batch`, whether it was a `Rollback`, how long it took, and the `Err` of a
failure. The names are `database.migration.applied`,
`database.migration.rolled-back`, and `database.migration.failed`. A migration
that runs in a transaction also commits one, so if the migrator's database has
an event bus, `database.transaction.committed` is published for each
migration; listeners that care about application transactions only can give the migrator its own
facade over the same driver, created without `Builder.Events`.

The migrator also logs each completed migration with `slog`, which is enough for most
deployments. Events are useful when a command should do more than log, such as
posting to a chat channel when a production migration fails. See
[Migrations](migrations.md).

## Telemetry

When telemetry is started, every `Exec`, `Select`, `Find`, `Cursor`, `Lock`, and
`TryLock` on the facade records a client span and a duration measurement. `Paginate`,
`CursorPaginate`, and `Each` use these calls, so each of their queries is
recorded, and migrations record theirs too, since they run through the same
facade.

The database follows the telemetry you pass with `Builder.Telemetry`. Without it, it
uses the process default, which is a no-op until you set one, and it looks up
the default each time an operation runs, so installing telemetry after creating
the database still works. With telemetry off, an operation costs no allocations.
The application, the telemetry drivers for OpenTelemetry and Prometheus, and
how to export the data are described in
[Telemetry](../observability/telemetry.md).

### Spans

Each span is a client span named after the operation, following the OpenTelemetry
database conventions:

| Call | Span name | Notes |
| --- | --- | --- |
| `Exec`, `Select`, `Find` with a table builder | `INSERT users` | the operation and the table |
| the same with raw SQL or a builder without a table | `SELECT` | the operation only |
| a statement whose first word is not a common verb | the database system, such as `postgresql` | |
| `Cursor` | `SELECT users` | lasts from the query until the cursor is exhausted, fails, or is abandoned |
| `Lock` | `LOCK` | the acquisition only, not the time the lock is held |
| `TryLock` | `TRY_LOCK` | the attempt only; a refused attempt is not an error, so the span has no `error.type` |

The attributes are:

- `db.system.name`: `postgresql`, `mysql`, or `sqlite`, derived from the driver's
  dialect; `other_sql` for anything else. `Builder.System` overrides it.
- `db.namespace`: the name you set with `Builder.Namespace`, usually the database
  name. It is omitted when empty. Set it whenever the application uses more than
  one database; see [Multiple databases](multiple-databases.md#telling-databases-apart-in-telemetry).
- `db.operation.name`: `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `CREATE`, `ALTER`,
  `DROP`, or `TRUNCATE`. Other verbs are left out so that the set of values
  stays small.
- `db.collection.name`: the table, known only when the statement is a query
  builder over a table.
- `db.query.text`: omitted by default. Explicit `Builder.QueryText(true)` exports
  the compiled SQL verbatim, including any literals and comments.
- `error.type`: when the operation failed.

A transaction has no span of its own, because the conventions define none.
Operations inside it become children of whatever span is in the context, such
as the HTTP request, so a trace shows the statements of one request together.
To see where a transaction starts and ends, use the events above.

### Metrics

Every operation also records a measurement in the histogram
`db.client.operation.duration`, in seconds, with buckets from one millisecond to
ten seconds. The metric carries `db.system.name`, `db.namespace`,
`db.operation.name`, `db.collection.name`, and `error.type`. Those keys are few
and bounded on purpose, so the metric stays cheap: a table has a small set of
values, and no statement or parameter is ever a label. From it you can chart the
p99 duration of `SELECT` on `orders`, or the error rate per database.

### Failures

An operation that returns an error marks its span as failed and records the
error's **type** in `error.type`, for example `*pgconn.PgError` or `*sqlite.Error`.
The sentinels of the database package and the context errors have readable
names such as `context.DeadlineExceeded`. Nothing else about the error is
recorded: the span has no exception event, and its status message contains only
the error type, not the original error text, because that text frequently
includes values.

Not finding a row is not a failure. `Find` returning `database.ErrNoRows`
leaves the span successful. And a statement that fails to build, for example an
invalid column name, never reaches the database, so it records nothing.

## What is recorded and what is not

Because telemetry usually leaves your network for a vendor, the database records
as little as possible about your data:

| Recorded | Never recorded |
| --- | --- |
| the operation, the table, the database name and kind | bound argument values |
| SQL text only after explicit `QueryText(true)` | the text of errors |
| the type of an error | lock names |
| how long the operation took | returned row values |

Query text is **off by default**; operation labels, spans, and metrics still
work without it. Bound arguments are never attached, but opting in exports the
SQL exactly as compiled, without redacting literals or comments. Values passed
to structured `Where` or `Insert` remain separate bindings. Values written into
raw SQL, `SelectRaw`, or schema defaults remain text in the statement.

> [!WARNING]
> Enable `QueryText(true)` only for statements audited as safe to export. Raw
> literals, SQL comments, migration backfills, and schema defaults can contain
> secrets or personal data. The setting applies to migration SQL too when the
> migrator uses that facade. It is not a SQL sanitizer.

For raw SQL, keep variable values in arguments:

```go
query.Raw("SELECT * FROM users WHERE email = ?", email)
```

Only when the remaining SQL text, including names and comments, is safe for your
telemetry destination, explicitly opt in for that database:

```go
db := database.New(driver, func(connection *database.Builder) {
	connection.QueryText(true)
})
```

Omit this setting, or set it to false, to keep SQL text out of spans. Neither
choice changes query execution or the operation metrics.

## Logging slow queries

The database emits no slow-query event, because a query is not an event
but a measurement. There are two good ways to find slow ones.

### Finding slow queries across instances

Start with the duration metric and the spans. With the OpenTelemetry or
Prometheus driver, alert on the p99 of `db.client.operation.duration` per
`db.operation.name` and `db.collection.name`, and use the trace of a slow request
to see which statement took the time. This costs nothing in your code, and it
shows you the slowest statements across all instances.

### Logging individual slow queries

If you also need a log line for each slow statement, wrap the driver. A driver can
wrap another driver, and the facade accepts the wrapper wherever it accepts a
driver. Import the standard-library `context`, `log/slog`, and `time` packages
alongside `database`. First, define the wrapper and preserve native-driver discovery:

```go
type SlowLog struct {
	database.Driver

	logger    *slog.Logger
	threshold time.Duration
}

// Unwrap lets db.DriverAs find the driver underneath.
func (recorder *SlowLog) Unwrap() database.Driver {
	return recorder.Driver
}
```

This custom wrapper logs the SQL text regardless of the facade's `QueryText`
setting. Use it only for audited SQL, or remove the `sql` field from the log.
It does not log bound arguments, but literals and comments remain visible:

```go
func (recorder *SlowLog) observe(ctx context.Context, statement string, start time.Time) {
	if elapsed := time.Since(start); elapsed >= recorder.threshold {
		recorder.logger.WarnContext(ctx, "slow query", "duration", elapsed, "sql", statement)
	}
}
```

Wrap each query method with the same deferred observation. For `Exec`:

```go
func (recorder *SlowLog) Exec(ctx context.Context, statement string, args ...any) (int64, error) {
	defer recorder.observe(ctx, statement, time.Now())

	return recorder.Driver.Exec(ctx, statement, args...)
}
```

Override `Select` and `Find` in the same way. Also wrap transaction callback
drivers, or queries inside transactions bypass the logger:

```go
func (recorder *SlowLog) WithTransaction(ctx context.Context, fn func(tx database.Driver) error) error {
	return recorder.Driver.WithTransaction(ctx, func(tx database.Driver) error {
		return fn(&SlowLog{Driver: tx, logger: recorder.logger, threshold: recorder.threshold})
	})
}
```

Retained sessions need the same treatment, including transactions started
through them:

```go
func (recorder *SlowLog) WithSession(ctx context.Context, fn func(session database.Driver) error) error {
	return recorder.Driver.WithSession(ctx, func(session database.Driver) error {
		return fn(&SlowLog{Driver: session, logger: recorder.logger, threshold: recorder.threshold})
	})
}
```

The embedded `database.Driver` forwards everything the wrapper does not
override, including `Dialect`, which a wrapper must keep reporting. The
`WithTransaction` and `WithSession` overrides wrap callback drivers as well, so
the overridden methods are timed inside either scope, and `Unwrap` keeps
`db.DriverAs` working. Put the
wrapper between the driver and the facade:

```go
slow := &SlowLog{Driver: driver, logger: logger, threshold: 200 * time.Millisecond}
db := database.New(slow)
```

The log line holds verbatim SQL and the duration, not bound arguments. Unlike
default facade telemetry, it explicitly includes SQL text. The wrapper does not
time `Lock` or the `Query` call behind `Cursor`. You can override `Query` the
same way, but it then measures only how long the database takes to answer, not
the whole iteration; the cursor span in telemetry covers the iteration.

## Testing

To assert what your code reports, use the in-memory telemetry fake from
`telemetrytest`. It records spans and metrics instead of exporting them. This
test uses your isolated SQLite `driver`, with the `tags` table already migrated.
Import `testing`, `arkoren.dev/core/service/telemetry`,
`arkoren.dev/core/service/telemetry/telemetrytest`, and `arkoren.dev/foundation/query`:

```go
func TestInsertIsTraced(t *testing.T) {
	fake := telemetrytest.New()
	db := database.New(driver, func(connection *database.Builder) {
		connection.Telemetry(fake.Telemetry(t))
	})

	_, err := db.Exec(t.Context(), query.Table("tags", func(statement *query.Builder) {
		statement.Insert(map[string]any{"name": "go"})
	}))
	if err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "INSERT tags",
		telemetry.String("db.system.name", "sqlite"),
		telemetry.String("db.collection.name", "tags"),
	)
	fake.AssertHistogram(t, "db.client.operation.duration", 1,
		telemetry.String("db.operation.name", "INSERT"),
	)
}
```

Events need no fake: create an `event.New()` bus, listen on the topics you care
about, pass the bus with `Builder.Events`, and assert on what the listener
collected. See [Testing](../testing/overview.md).

For every function and option, see [the `database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database),
[the `migrate` API reference](https://pkg.go.dev/arkoren.dev/core/service/database/migrate),
[the `event` API reference](https://pkg.go.dev/arkoren.dev/core/event), and
[the `telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
