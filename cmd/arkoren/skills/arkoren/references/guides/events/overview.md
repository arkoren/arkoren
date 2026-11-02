---
title: Overview
description: Publish typed local notifications and choose between best-effort observation and error-returning dispatch.
order: 10
summary:
  - Define typed topics and publish notifications on a shared in-process bus.
  - Choose whether listener failures are logged or returned to the caller.
  - Understand synchronous delivery, transaction context, and durability limits.
---

# Overview

When a report becomes available, several parts of your application might want to
observe it without the report service importing each observer. An **event bus**
connects a named notification to local listener functions. Arkoren calls those
functions directly; it does not store notifications or send them to another process.

Use the bus for short, best-effort observations. If work must survive a crash,
record a durable handoff where the application owns the change; adding a queue
call inside a listener does not protect the time before that call succeeds.

Focused snippets leave routine error handling to your application. Examples about
failure decisions retain the relevant checks.

## Choosing a Bus

With an existing application, use `bus := app.Events()`. It returns the same
nonnil `*event.Bus` throughout that application's lifetime. Pass this bus to
components that need it rather than passing the whole application.

For an isolated component or test, construct a separate bus:

```go
import "arkoren.dev/core/event"

bus := event.New()
```

Each bus has independent registrations. It needs no startup or shutdown and has
no resources to close. To choose where best-effort listener failures are logged,
use a construction callback; `slog` comes from `log/slog`:

```go
bus := event.New(func(events *event.Builder) {
	events.Logger(slog.Default())
})
```

This is an alternative construction. Settings are copied when construction ends;
retaining the builder does not reconfigure the bus. The logger remains shared.
Callbacks run once in order, later logger settings win, and nil selects
`slog.Default()` when a failure is logged. A supplied nil callback or callback
panic propagates. The application's own bus uses its application logger.

## Defining Topics

A **topic** associates an event name with its payload type. Define one topic per
name in the package that owns the fact. A report notification needs its stable
identifier, not its contents or a download credential:

```go
type ReportReadyEvent struct {
	ReportID string
}

var ReportReady = event.NewTopic[ReportReadyEvent]("reports.ready")
```

Typed calls check the payload at compile time, but routing uses the name. Topics
with the same name share listeners even if constructed separately. Give each
name exactly one payload type: a typed listener silently skips a nonnil payload
of another type.

Names contain dot-separated tokens of 1–64 lowercase letters, digits, or
hyphens, with no leading or trailing hyphen in a token and at most 255 bytes
overall. `reports.ready` is valid; `Reports.Ready` is not. Construction does not
validate the name. Registration and dispatch return `event.ErrInvalidName`.

If a notification needs no data, define a **signal** instead:

```go
var ReportsCleared = event.NewSignal("reports.cleared")
```

Publish it with `bus.Signal(ctx, ReportsCleared)`; listeners take `event.Void`.
These signals are unrelated to [workflow signals](../workflows/signals-and-waiting.md).

## Registering Observers

Register before the operation can publish. With `ctx` from `context` and `logger`
an existing `*slog.Logger`, an observer can record only the report identifier:

```go
unsubscribe, err := bus.Listen(ReportReady, func(ctx context.Context, notice ReportReadyEvent) error {
	logger.InfoContext(ctx, "report ready", "report_id", notice.ReportID)

	return nil
})
```

Registration returns an error and a removal function. Check the error during
wiring before retaining or calling `unsubscribe`. Keep the registration for its
owner's lifetime; [Writing Listeners](listeners.md#removing-listeners) explains
request, test, and composition ownership. Registering after publication does not
replay an earlier event.

## Publishing Events

After the report is available, announce that fact from the application layer
that knows it happened:

```go
bus.Emit(ctx, ReportReady, ReportReadyEvent{ReportID: "report-2026-11"})
```

`Emit` returns no result. A listener error or panic is logged with the event name,
delivery stops, and later listeners are skipped. Panics include a diagnostic
stack in that log. The operation's result is unchanged. A nil bus makes `Emit`
and `Signal` no-ops.

If a callback must be allowed to fail the operation, use `Dispatch` and handle
its error at the boundary that can still reject or roll back the change:

```go
err := bus.Dispatch(ctx, ReportReady, ReportReadyEvent{ReportID: "report-2026-11"})
```

`Dispatch` returns the first listener error and does not recover listener panics.
It does not undo work that already happened. A direct function call is often
clearer for a mandatory dependency; [Event Auditing](auditing.md) shows where
transactional dispatch can enforce a required record.

## Delivery Boundaries

Both publishing methods run listeners synchronously on the publisher's goroutine.
Exact-name listeners run first in registration order, then matching pattern
listeners in their registration order. A slow listener delays the publisher;
there is no automatic retry, buffering, or independent execution.

Listeners receive the supplied context unchanged, including cancellation,
deadlines, and values. With a database transaction's callback context, compatible
database calls in a listener join that transaction. They do not observe a durable
commit yet. Use [after-commit hooks](../database/transactions.md#after-commit)
for best-effort effects that must wait for commit, or persist pending work in the
transaction when losing it is unacceptable. Never keep using a completed
transaction context from a background goroutine.

The bus supports concurrent registration and publication. Different publishers
can call the same listener concurrently, so the listener owns synchronization
for its state. Registration changes affect later snapshots; removing a listener
does not cancel a callback already selected for delivery.

Use [queues](../queues/overview.md) for worker delivery and
[broadcasting](../broadcasting/overview.md) for messaging between processes.
Neither is supplied by this bus. Arkoren components publish on the bus only
when configured to do so, apart from the application's own lifecycle events;
[Built-in Events](built-in-events.md) is the discovery catalog.

For all functions and options, see the [`event` API reference](https://pkg.go.dev/arkoren.dev/core/event).
