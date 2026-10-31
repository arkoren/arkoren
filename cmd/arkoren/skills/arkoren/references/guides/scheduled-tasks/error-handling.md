---
title: Scheduled Task Errors
nav: Error Handling
description: What happens when an entry or task fails, panics, times out, or chooses not to run, how failures are reported, and what drivers do next.
order: 30
---

# Scheduled Task Errors

## Introduction

Scheduled work runs without anyone watching, so what it does when it fails is
part of its design. Separate two decisions: the scheduler classifies the
outcome, and the driver decides what happens next. The scheduler calls your
function with the entry's timeout, recovers panics, and records the outcome in
logs and configured telemetry. When events are configured, failures and skips
also publish events. The driver receives nil for success or a skip, and an
error for failure; its [retry behavior](#what-the-driver-does-next) varies.

A run ends in one of three **outcomes**. `ok` means the function returned
nil. `skipped` means it returned `schedule.ErrSkipped`. `failed` means it
returned another error or panicked. Timeout cancellation is cooperative: a
function that returns nil after its deadline still counts as successful.

## Skipping on purpose

An entry sometimes decides that there is nothing to do, or that another
instance is already doing it. Returning `schedule.ErrSkipped` says exactly
that. With an existing `*cache.Cache` named `store`, use `TryLock` to skip
rather than wait for another instance. Here, `rebuildIndex` is your
application's indexing function:

```go
import "arkoren.dev/core/service/schedule"

func reindex(ctx context.Context) error {
	lease, held, err := store.TryLock(ctx, "reindex", time.Minute)
	if err != nil {
		return err
	}
	if !held {
		return schedule.ErrSkipped
	}
	defer lease.Unlock(ctx)

	return rebuildIndex(ctx)
}
```

The lease expires after one minute even if rebuilding continues. Renew it for
longer work, and use a database lock or fencing when concurrent execution would
break correctness; a cache lease only reduces duplicate work. See
[Cache Locks](../cache/locks.md) for renewal and release behavior.

The scheduler logs the run as skipped, labels the span and metrics `skipped`,
and hands the driver a nil error, so no engine sees a failure and no
`schedule.run.failed` event fires. With `Builder.Events`, the scheduler publishes
`schedule.run.skipped` instead, with the name and duration, so you can count
how often a run steps aside. Wrapping it with `fmt.Errorf("%w", ...)`
works too, because the check uses `errors.Is`.

## Failures, panics, and timeouts

Any other error is a failure. The scheduler wraps it as `run <name>: ...`,
logs it at error level with the duration, marks the span as an error with the
error's Go type as `error.type`, and publishes `schedule.run.failed` when the
scheduler was built with `Builder.Events`. A panic does not crash the process: it is
recovered and becomes an error that carries the panic value and the stack, with
`schedule.panic` as its `error.type`. A task whose payload cannot be decoded
into its type fails the same way before your handler is called.

An entry's `Timeout` cancels the context passed to your function when the
duration passes. The scheduler cannot stop code that ignores its context, so
the function has to check `ctx` or pass it to calls that do. When the function
returns the context's error, the run counts as failed, with the context error
as its type. Returning nil instead reports success even after the deadline:

```go
entry := schedule.NewEntry("export", export, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Hour(2))
	entry.Timeout(30 * time.Minute)
})
```

## Reacting to failures

If a background run fails, an event listener can notify the on-call service
without putting alert delivery in every entry. The `schedule.run.failed` payload has the
entry or task name, whether it was a task, how long it ran, and the error. The
event fires from the scheduler, so it is the same for every driver, including a
run started with `RunNow`. Construct the scheduler with
`scheduler.Events(app.Events())` in its `schedule.Builder` callback
before relying on this listener. Here, `notifyOnCall` is your application's
alerting function:

```go
_, err := app.Events().Listen(schedule.RunFailed,
	func(ctx context.Context, e schedule.RunFailedEvent) error {
		slog.ErrorContext(ctx, "scheduled run failed",
			"name", e.Name, "task", e.Task, "duration", e.Duration)

		return notifyOnCall(ctx, e.Name)
	})
```

A listener that fails is logged and does not change the run. The error text of
a failure is not recorded in telemetry, so it can contain details without
leaking them into a metrics backend; the event and the log do carry it, so
treat them with the same care you give any log. [Built-in Events](../events/built-in-events.md#scheduled-tasks)
lists the event next to the others.

## What the driver does next

After the scheduler returns, the driver decides whether the failure matters to
the engine, and the answers differ:

- The **memory** driver logs `schedule dispatch failed` and moves on. The next
  tick runs normally and nothing is retried.
- **Temporal** makes one attempt for each run. The activity fails, Temporal
  records the failure in the schedule's history, and the next firing is
  independent.
- **Hatchet** marks failed or timed-out runs failed without retrying them. Delivery
  is still at least once: connection loss or worker reconnection can deliver the
  same run again. Write entries so that running one twice is harmless.
- **Restate** treats an error from your function as terminal: it is logged and
  the run is not retried. One exception is a run for a name this deployment does
  not know, which is retried because another deployment of the application may
  know it. A missing dispatch function, before the driver's `Run` has installed
  it, is also retryable.

Because retries differ, the safest entry is **idempotent**: it checks what is
already done, works in batches that can be repeated, and uses a lock or a
unique key when it must not run twice at once. `WithoutOverlapping` coordinates
driver-fired runs of one entry; it does not prevent a repeat after a failure
or an overlapping manual `RunNow`. Memory's coordination is process-local;
see [overlapping runs](overview.md#overlapping-runs).

## Mistakes caught at startup

Most mistakes in a schedule are found before anything runs, and are returned
rather than panicking:

| Error | Meaning |
| --- | --- |
| `schedule.ErrInvalidName` | an entry or task name is not 1 to 64 lowercase letters, digits, or inner hyphens |
| `schedule.ErrInvalidEntry` | a missing or bad spec, a nil function or handler, a negative timeout, or a zero time passed to `At` |
| `schedule.ErrDuplicateName` | the name is already used by an entry or a task |
| `schedule.ErrUnknownName` | `After` or `At` for a task with no handler, or `RunNow` for an unknown entry |
| `schedule.ErrRunning` | `Add` or `Handle` after `Run` started, or a second `Run` |
| `schedule.ErrUnsupported` | the driver cannot do it, such as Hatchet with a non-UTC entry or a driver that cannot defer |

Entries keep their first error and report it from `Err`, so `Add` is where a
bad entry surfaces. `When` is required; no callbacks or empty callbacks do not
infer timing. Construction callbacks still run for invalid declarations, and
the completed declaration is validated after all callbacks. A nil execution
function remains `ErrInvalidEntry`. A supplied nil construction callback or
a callback panic propagates instead of becoming a domain error; expanded nil
or empty slices supply no callbacks. This differs from the runtime panic
recovery described above. Handle the `Add` return value.

Engine startup failures follow the driver too. Temporal and Hatchet return
reconciliation errors from `Run`, which stops the runner and, through it, the
application. Restate instead retries initial publication every second until
the server accepts it or the context is canceled. An unreachable Restate
server can therefore leave startup publication waiting rather than failing
immediately.

For every function and option, see [the `schedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule).
