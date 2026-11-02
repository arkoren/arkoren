---
title: Overview
description: Register background work with a scheduler and distinguish calendar timing from handler execution.
order: 10
summary:
  - Distinguish recurring calendar entries from typed deferred tasks.
  - Register work before starting a scheduler and understand its shutdown boundary.
  - Run entries manually, inspect definitions, and observe run outcomes.
---

# Overview

Session cleanup and daily reports need to run even when no request arrives.
The `schedule` service connects those application functions to a calendar.
It also accepts one-off work, such as sending a reminder ten minutes after a
user action.

## Scheduling Model

An **entry** is a named function with a recurring calendar specification. A
**task** is a named, typed payload definition for one-off jobs. Both register
on a `*schedule.Scheduler`; its driver decides when to deliver the work.

The scheduler owns registration, payload decoding, cooperative entry timeouts,
panic recovery, and run observations. The driver owns timers, persistence,
overlap coordination, and missed-tick behavior. Choose and construct one on
[Drivers](drivers.md) before using the snippets below.

Memory runs independent timers in every process and loses deferred jobs on
restart. Temporal and Restate retain engine state and coordinate calendar
delivery across workers. Neither shared timing nor an overlap policy guarantees
exactly-once application effects: make repeat deliveries harmless.

Focused snippets keep error results visible but omit routine propagation checks.
Complete application code must handle those errors.

## Registering Work

With an existing `scheduler`, define a cleanup entry using your application's
`pruneSessions` function, which has type `func(context.Context) error`. Import
`time` and `arkoren.dev/core/service/schedule`:

```go
prune := schedule.NewEntry("prune-sessions", pruneSessions, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Every(5 * time.Minute))
	entry.Timeout(time.Minute)
})
```

This matches UTC minutes :00, :05, :10, and so on, rather than five minutes
after construction. The timeout cancels the function's context; it cannot stop
code that ignores cancellation. Pass that context to database and network calls.

Register the definition before starting the scheduler:

```go
err := scheduler.Add(prune)
```

`Add` validates the complete batch and adds all entries or none. Names are
1–64 lowercase letters, digits, or hyphens, with no leading or trailing hyphen;
entries and tasks share one namespace. [Calendar Schedules](calendar-schedules.md)
explains calendar fields and overlap policy. For payload-bearing work created
by an application action, use [Deferred Tasks](deferred-tasks.md) instead.

## Running Schedulers

During application composition, register the configured scheduler as a runner.
With an existing `composer` from `arkoren.dev/core/application`:

```go
composer.Run(scheduler.Run)
```

`Run` hands the registered entries and dispatcher to the driver, then blocks
until cancellation or driver failure. Once it starts, `Add` and `Handle` return
`schedule.ErrRunning`. The facade is single-use: a second `Run` also returns
that error, even after the first finishes.

Every worker must register matching names and compatible handlers before
`Run`; only names and JSON payloads travel through the engine, not Go closures.
Keep captured services alive for the full execution lifetime.

Cancellation requests shutdown, not forced callback termination. In particular,
memory stops admitting timer work but drains admitted callbacks without a
driver timeout, on contexts detached from runner cancellation. Bound entry and
task work explicitly. The application's runner-wait budget can expire and let
cleanup proceed while callbacks still use their dependencies. See
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md) and the
[driver-specific shutdown rules](drivers.md).

## Manual Runs

For an operator action or a focused test, run a registered entry without
waiting for a tick:

```go
err := scheduler.RunNow(ctx, "prune-sessions")
```

`RunNow` waits for the function and applies its timeout, panic recovery,
logging, events, and telemetry. It works independently of `Run` and preserves
caller cancellation. It bypasses the driver's calendar and overlap guards,
so it can overlap a scheduled run even with `WithoutOverlapping`. Use
application-level coordination if that concurrency would be unsafe.

For remote operations, `scheduler.MCPTools("jobs")` supplies an `entries` tool
for definitions and next calendar matches, and a `run` tool that waits for
`RunNow`. Select tools explicitly on a
[protected MCP endpoint](../mcp/securing-mcp.md); omit `run` for read-only callers.
Availability policy does not replace resource authorization in application code.

## Inspecting Entries

To review configured execution bounds without running work, filter the
registered definitions:

```go
limited := scheduler.Entries().Filter(func(_ int, entry schedule.Entry) bool {
	return entry.TimeoutValue() > 0
})
```

`Entries` returns an eager `collection.Slice[schedule.Entry]` snapshot in
registration order, not execution history. Replacing its elements cannot edit
the scheduler, and later registrations do not update it. Entries and specs are
immutable; their execution functions and captured dependencies remain shared.

## Events and Telemetry

If you need run notifications, select an event bus when constructing the
scheduler. With an existing `driver` and application `app`:

```go
scheduler := schedule.New(driver, func(clock *schedule.Builder) {
	clock.Events(app.Events())
	clock.Telemetry(app.Telemetry())
})
```

Events are off by default. Configured events report failed and deliberately
skipped runs, accepted deferrals, and accepted cancellations. They do not prove
delivery or that cancellation removed pending work. See
[Error Handling](error-handling.md) and
[Built-in Events](../events/built-in-events.md#scheduled-tasks).

Without explicit telemetry, the scheduler follows `telemetry.Default()` when
each run starts. Enabled telemetry emits a `schedule run` span and count and
duration metrics labeled by name, entry/task kind, and `ok`, `skipped`, or
`failed` outcome. Calls made with the handler context become child spans.
Payloads, job IDs, and error text are excluded from automatic telemetry;
failure logs and events still carry errors. Configure the shared service using
[Telemetry](../observability/telemetry.md).

For every function and option, see [the `schedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule).
