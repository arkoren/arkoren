---
title: Scheduled Tasks Overview
nav: Overview
description: Define named calendar entries and typed one-off tasks, run them with a scheduler, control overlapping runs, and test with memory.
order: 10
---

# Scheduled Tasks Overview

> [!NOTE]
> Hatchet references on this page describe a pending, unpublished integration,
> not available support. Its [retained notes](drivers.md#hatchet) are development
> context; choose memory, Temporal, or Restate for an available driver.

## Introduction

Most applications have work that nobody asks for at the moment it happens:
pruning expired records every five minutes, sending a report at three in the
morning, reminding a user ten minutes after they left a cart behind. The
`schedule` service runs that work. It knows two kinds of it. An **entry** is a
named function that runs repeatedly on a calendar. A **task** is a typed
one-off job that you defer to a later moment and that carries a payload.

The service splits the work in two. The **scheduler**, `*schedule.Scheduler`,
owns *what* runs: the entries you add and the task handlers you register. A
**driver** owns *when* it runs: timers, persistence, and coordination between
processes. The [memory driver](drivers.md#memory) keeps timers in the process
and suits development and a single replica. The [Temporal](drivers.md#temporal)
and [Restate](drivers.md#restate) drivers hand
the timing to an engine, coordinating ticks across replicas and retaining
deferred tasks across worker restarts. This does not guarantee exactly-once
handler effects. Your entries and handlers do not change when you switch,
but [driver limits and retry behavior](drivers.md#compatibility-matrix) do.

Focused snippets keep error results visible but omit routine propagation checks.
Handle those errors in your application; failure examples and test assertions
retain their checks.

## Defining entries

An entry has a name, a function, and a **spec** that says when it runs.
`schedule.NewEntry` publishes the entry after its construction callbacks set
the spec with `EntryBuilder.When`. Here, `pruneSessions` is your application's
`func(context.Context) error` function that deletes expired sessions:

```go
import "arkoren.dev/core/service/schedule"

prune := schedule.NewEntry("prune-sessions", pruneSessions, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Every(5 * time.Minute))
})
```

The function has the signature `func(context.Context) error`. Names are one to
64 characters of lowercase letters, digits, and inner hyphens, because drivers
use them as identifiers in the engine. The context carries the entry's
timeout, if any, and the telemetry span of the run, so cache, database, and
storage calls made with it appear as children of the run.

Callbacks run directly once, synchronously in order on one fresh draft.
Settings return that same draft; the published entry is immutable, even if a
callback retains the draft. Final validation follows all callbacks. `When` is
required: omission never infers a schedule. A nil execution function is a
normal declaration error, not a construction panic. Supplied nil construction
callbacks and callback panics propagate; expanded nil or empty callback slices
supply no edits. The execution function is retained, not run during construction.

A spec starts from a time zone, either `schedule.UTC()` or `schedule.In(location)`,
and narrows down with fields. Fields you leave unset default sensibly: a field
finer than the coarsest one you set is zero, and a coarser one matches every
value. So `Hour(3)` means 03:00:00 every day, `Weekdays().Hour(9).Minute(30)`
means 09:30 from Monday to Friday. A spec with only date or weekday fields
runs at midnight; a zone alone, such as `schedule.UTC()`, is invalid:

```go
report := schedule.NewEntry("weekly-report", sendReport, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().On(time.Monday).Hour(6))
})

digest := schedule.NewEntry("digest", sendDigest, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Weekdays().Hour(9).Minute(30))
})
```

`Every` steps through the calendar instead: `Every(5 * time.Minute)` runs at
:00, :05, :10, and so on. The step must divide its unit evenly, so a quarter
of an hour is valid and seven minutes is not, and it must stay under 24 hours.
When the calendar is easier to say as cron, `Cron("0 3 * * *")` takes a
five-field expression, or six with seconds first. A cron expression cannot be
combined with the field methods. Every `Spec` method returns a copy and keeps the
first error, so a bad spec shows up when you add the entry, never as a panic.
Choosing the zone is covered in [Time Zones and DST](time-zones-and-dst.md).

If rebuilding an index takes longer than expected, avoid starting another
rebuild on the next tick. `WithoutOverlapping` skips that tick, while `Timeout`
cancels a run's context after a duration. Your `rebuildIndex` function must
honor that context to stop in time:

```go
reindex := schedule.NewEntry("reindex", rebuildIndex, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Every(time.Minute))
	entry.Timeout(45 * time.Second)
	entry.WithoutOverlapping()
})
```

The entry's timeout defaults to unlimited; `Timeout(0)` removes that limit and
a negative duration is an error. Engine drivers can impose their own run
bounds; see [driver configuration](drivers.md). A later callback can replace
valid timing or timeout settings, but cannot clear the first invalid input. For shared policy, pass a
common callback followed by a local callback; there is no entry `Derive` method.

## Creating and running the scheduler

Build the scheduler with the bundled memory driver and add the entries:

```go
import "arkoren.dev/core/service/schedule/memoryschedule"

driver := memoryschedule.New()
scheduler := schedule.New(driver)

err := scheduler.Add(prune, report, digest, reindex)
```

Register `Run` as an application runner. With an existing application `app`,
import `arkoren.dev/core/application` and pass this callback to
`app.Run(ctx, compose)`:

```go
compose := func(composer *application.Composer) {
	composer.Run(scheduler.Run)
}
```

`Add` is all or none: if one entry is invalid or its name is taken, nothing is
added and the error says which. Entries and task names share one namespace.
`Run` hands the entries to the driver and blocks until shutdown or a driver
failure. Register it with `composer.Run` during composition, which cancels its
context at shutdown
(see [Lifecycle and Ownership](../application/lifecycle-and-ownership.md)).
Registration closes when `Run` starts: `Add` and `Handle` afterwards return
`schedule.ErrRunning`, and so does a second `Run`.

Cancellation requests driver shutdown; it does not prove that callbacks have
finished. The [memory driver](drivers.md#behavior-and-limits) deliberately drains
admitted executions on contexts detached from runner cancellation, without a
driver drain timeout. Entry timeouts are cooperative, and task handlers must
bound their own work. The application's runner-wait budget can expire and allow
cleanup to proceed while such work remains active; it cannot kill the callbacks.

Entries and handlers are closures, and only a name and a JSON payload travel
through the driver. Every process that runs the scheduler must therefore
register the same names before `Run`. With an engine driver, any one of those
processes may receive a tick, so a deployment where replicas differ in what
they register will fail the runs that land on the wrong replica.

To run an entry right now, for an operator command or a test, call `RunNow`
with its name. It goes through the same path as a scheduled run, so the
timeout, panic recovery, logging, telemetry, and the failure event all apply:

```go
err := scheduler.RunNow(ctx, "prune-sessions")
```

`RunNow` bypasses the driver's calendar and overlap coordination. It can overlap
a scheduled execution even when the entry uses `WithoutOverlapping`; use
application-level locking when concurrent manual and scheduled runs are unsafe.

To run entries from outside the process, `scheduler.MCPTools("jobs")` returns
[native MCP tools](../observability/diagnostic-tools.md).
`entries` lists definitions and their next run; `run` calls `RunNow` and waits.
Select and register tools explicitly on your protected endpoint. Omit `run` for
observation-only callers, and apply [capability policy](../mcp/securing-mcp.md)
with `Tool.WithAvailability`.
Resource authorization belongs in the shared typed application handler.

## Inspecting registered entries

To review timeout policy without running jobs, inspect the existing `scheduler`:

```go
limited := scheduler.Entries().Filter(func(_ int, entry schedule.Entry) bool {
	return entry.TimeoutValue() > 0
})
```

`Entries` returns a `collection.Slice[schedule.Entry]` in registration order;
`limited` keeps that order. This is a detached eager snapshot, not a live query
or a list of completed runs. Later `Add` calls do not update it, and replacing
elements cannot change registrations. The method copies the outer slice;
entries and specs are immutable, while execution functions and their captures
remain shared. The collection wrapper adds no ownership or execution behavior.

## Overlapping runs

By default, an entry can start again on the next tick while its previous run
is still active. If concurrent runs would interfere with each other, use
`WithoutOverlapping` to tell the driver to skip the new tick instead. The
memory driver does this with a flag per entry in the process. Temporal sets its
overlap policy to skip, Hatchet runs the entry on a task limited to one run per
entry name and cancels the newer run, and Restate runs the entry inline in the
tick invocation rather than through its runner. The engines coordinate these
policies across replicas; memory coordinates only within
one process. These policies apply to driver-fired runs, not `RunNow`.

`WithoutOverlapping` controls concurrency; it does not make a run safe to
repeat. When a driver can deliver a tick twice, for example after a
retry, the entry itself must be idempotent. [Scheduler Drivers](drivers.md)
lists which drivers do that.

## Deferring tasks

A task is for work that happens once, later, because of something a user did.
Declare a package-level value that fixes the payload type:

```go
type Reminder struct {
	UserID int `json:"user_id"`
}

var Remind = schedule.NewTask[Reminder]("remind")
```

Before `scheduler.Run`, register the handler. Here, `sendReminder` is your
application's mail-sending function, not a scheduler API:

```go
err := scheduler.Handle(Remind, func(ctx context.Context, reminder Reminder) error {
	return sendReminder(ctx, reminder.UserID)
})
```

When the user action occurs, defer the task with its payload:

```go
id, err := scheduler.After(ctx, Remind, Reminder{UserID: 42}, 10*time.Minute)
```

`After` defers by a delay and `At` by an absolute time. Both return the job's
identifier, a string the driver chose, and fail with `schedule.ErrUnknownName`
when no handler is registered for the task. The payload is encoded as JSON, so
it must round-trip through `encoding/json`; keep it small and put an identifier
in it rather than the data itself. To withdraw a job, pass its identifier to
`Cancel`:

```go
err = scheduler.Cancel(ctx, id)
```

Canceling an unknown or finished job is not an error, and with the Hatchet
driver a job that has already started is not stopped. Whether deferred jobs
survive a restart depends on the driver: the memory driver drops them, and the
engine drivers keep them. A driver that cannot defer returns
`schedule.ErrUnsupported`.

## Events and telemetry

Events and explicit telemetry are optional. With an existing `app`, add them
when constructing the scheduler, before registering entries or running it:

```go
scheduler := schedule.New(driver, func(clock *schedule.Builder) {
	clock.Events(app.Events())
	clock.Telemetry(app.Telemetry())
})
```

With `Builder.Events`, every failed run publishes `schedule.run.failed` and every
run that returns `schedule.ErrSkipped` publishes `schedule.run.skipped`. A
successful `At` or `After` publishes `schedule.task.deferred` with the task name,
the job identifier, and when it is due, never the payload, and a successful
`Cancel` publishes `schedule.task.canceled`. Because canceling an unknown or
finished job also succeeds, that event means the cancellation was accepted, not
that a pending job was removed.
[Built-in Events](../events/built-in-events.md#scheduled-tasks) describes each
payload. With telemetry started, each run, scheduled or
started by `RunNow`, is one root span named `schedule run`, with the entry or
task name in `arkoren.schedule.entry`, and feeds a count and a duration metric labeled with the name,
whether it was an entry or a task, and the outcome: `ok`, `skipped`, or
`failed`. Task payloads, error text, and job identifiers are never recorded.
When telemetry is off, a run costs nothing extra. The log lines of a run carry
the trace ID. [Telemetry](../observability/telemetry.md) explains how to
turn it on.

## Testing

Two tools cover the usual tests. The memory driver runs real timers, so inside
a `testing/synctest` bubble an entry that runs every hour fires after an
instant of real time, and `RunNow` works without any timer at all. The
`scheduletest` package offers a fake driver for code that defers tasks. It
records the entries and jobs and fires nothing by itself, so a test inspects
what your code deferred without executing a handler. Register the same reminder
handler as above, then assert on the submitted payload:

```go
import "arkoren.dev/core/service/schedule/scheduletest"

fake := scheduletest.New()
scheduler := schedule.New(fake)
err := scheduler.Handle(Remind, func(ctx context.Context, reminder Reminder) error {
	return sendReminder(ctx, reminder.UserID)
})
if err != nil {
	t.Fatal(err)
}

_, err = scheduler.After(t.Context(), Remind, Reminder{UserID: 42}, 10*time.Minute)
if err != nil {
	t.Fatal(err)
}

fake.AssertDeferred(t, Remind, func(reminder Reminder) bool {
	return reminder.UserID == 42
})
```

This proves that the job was deferred with the expected user ID; it does not
prove that mail was sent. To execute due jobs through the real scheduler path,
start `scheduler.Run` and then call `fake.Drain(ctx)`. `Drain` returns
`scheduletest.ErrNotRunning` until `Run` has handed its dispatch function to the
fake; in a `synctest` bubble, use `synctest.Wait` before draining. Handler panics,
timeouts, and failure events then behave as they do in production.
[Controlling Time](../testing/time.md)
shows how to drive the memory driver in a `synctest` bubble. A new driver
should pass the `scheduletest.Run` conformance suite; see
[Conformance Suites](../testing/conformance-suites.md).

For every function and option, see [the `schedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule).
