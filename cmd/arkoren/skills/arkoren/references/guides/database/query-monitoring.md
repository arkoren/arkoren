---
title: Monitoring
description: Observe database activity through events, spans, metrics, and privacy-conscious diagnostics.
summary:
  - Record database spans and duration metrics without exporting SQL by default.
  - Interpret transaction events and cursor timing correctly.
  - Inspect native pool capacity without bypassing scoped execution.
order: 60
---

# Monitoring

A slow database call can include time waiting for a connection, executing SQL,
and reading results. Facade telemetry records client-side operations so you can
relate that time to a request or job. Transaction events report outcomes, not
every query. Configure a telemetry provider through
[Telemetry](../observability/telemetry.md) before expecting exported data.

## Operation Telemetry

With an existing `driver` and `signals` of type `*telemetry.Telemetry`, configure
the facade using `arkoren.dev/core/service/database`:

```go
db := database.New(driver, func(connection *database.Builder) {
	connection.Telemetry(signals)
	connection.Namespace("application")
})
```

`Namespace` labels the database name; it does not select a database or change
the connection. Without an explicit telemetry value, the facade resolves
`telemetry.Default()` on each operation. Disabled telemetry exports nothing.
The database system label comes from the driver's dialect unless overridden
with `System`.

`Exec`, `Select`, `Find`, and consumed `Cursor` operations create client spans
and record `db.client.operation.duration` in seconds. Lock acquisition uses
`LOCK` or `TRY_LOCK`; unlock is not traced. Pagination produces its underlying
count/read operations, and batching produces one read per batch. Transactions
have no separate facade span.

A cursor span lasts through iteration and ends on exhaustion, failure, or early
stop. It therefore includes time spent consuming rows, not merely server SQL
execution. Statement-build errors occur before an operation span starts.
Not-found errors count as successful outcomes. Other failures record an error
class, not error text that might contain private values.

Builder queries supply a table label. Raw SQL supplies only a recognized leading
verb; comments and CTEs are not parsed into operation names or table labels.
Keep namespaces stable rather than using user IDs as metric dimensions.

## SQL Privacy

SQL text is disabled by default. Bound argument values are never added by the
facade, but literals and comments in SQL can still contain sensitive data.
Only after auditing every statement sent through this facade, opt into verbatim
compiled text:

```go
db := database.New(driver, func(connection *database.Builder) {
	connection.Telemetry(signals)
	connection.QueryText(true)
})
```

This alternative construction adds `db.query.text` to spans, not metric labels.
It does not redact SQL. Review exporter access and retention as well as the
query itself; parameter binding alone does not make all statement text public.

## Transaction Events

To observe commits and rollbacks, pass an existing event bus:

```go
db := database.New(driver, func(connection *database.Builder) {
	connection.Events(bus)
})
```

`database.TransactionCommitted` fires after confirmed outermost commit and before
after-commit hooks. `database.TransactionRolledBack` fires when the outermost
transaction body, context, or commit fails. Nested transactions emit neither.
`TransactionEvent` contains `System`, `Namespace`, and `Err`; `Err` is nil for
a confirmed commit even if later cleanup or a hook fails. A rollback event is
not proof that a connection-loss outcome has been reconciled on the server.

Use the typed [event bus](../events/overview.md) to register listeners. Events
are in-process observations, not a durable audit log. Migration events have a
separate [deployment contract](../migrations/deployment.md#migration-events).

## Pool Diagnostics

When requests stall before SQL executes, inspect pool utilization alongside
operation durations. PostgreSQL exposes `driver.DB().Stat()`; MySQL and SQLite
expose `driver.SQL().Stats()`. These are native clients described in
[Drivers](drivers.md), not portable facade metrics.

Held named locks, unfinished sessions, transactions, and streaming cursors all
consume capacity. Finish them before increasing limits. Native pool calls and
`db.Driver()` bypass facade telemetry and context routing; do not move protected
SQL onto the root pool merely to diagnose it.

For configuration and event types, see the
[`database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
