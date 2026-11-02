---
title: Error Handling
description: Classify scheduled run outcomes, observe failures, and separate cooperative cancellation from driver recovery.
order: 60
summary:
  - Distinguish successful, deliberately skipped, and failed handler outcomes.
  - Apply cooperative timeouts and observe failures through logs and configured events.
  - Recognize driver recovery differences and registration errors before execution.
---

# Error Handling

Scheduled work has no request caller waiting to recover a failure. The
scheduler records what your handler returned; the driver decides whether the
engine retries or moves on. Keep those responsibilities separate when designing
alerts and repeatable business operations.

## Run Outcomes

Every executed entry or task produces one scheduler outcome:

| Outcome | Handler Result | Driver Receives |
| --- | --- | --- |
| `ok` | Returns nil. | Nil. |
| `skipped` | Returns an error matching `schedule.ErrSkipped`. | Nil. |
| `failed` | Returns another error or panics. | An error. |

An overlap tick suppressed by a driver never calls the handler, so it is not
the same as a handler returning `ErrSkipped`. Likewise, reaching a deadline
does not independently classify the run: a handler returning nil after
cancellation still reports `ok`.

## Skipping Work

If a stock refresh has nothing new to process, return `ErrSkipped` rather than
an ordinary failure. With application functions `stockChanged` and `refreshStock`,
import `context` and `arkoren.dev/core/service/schedule`. The first function has
type `func(context.Context) (bool, error)`; the second has type
`func(context.Context) error`:

```go
func refreshIfNeeded(ctx context.Context) error {
	changed, err := stockChanged(ctx)
	if err != nil {
		return err
	}
	if !changed {
		return schedule.ErrSkipped
	}

	return refreshStock(ctx)
}
```

The scheduler logs a skip, labels telemetry `skipped`, and returns nil to the
driver. With an event bus configured, it emits `schedule.run.skipped`, not
`schedule.run.failed`. Wrapped skips work because classification uses
`errors.Is`.

You can also skip when application coordination denies a lock, but a skip does
not itself establish exclusive execution. Choose appropriate
[locking and fencing](../cache/locks.md) if concurrent effects would violate
correctness.

## Timeout Boundaries

To bound a recurring refresh, configure a positive `Timeout` during entry
construction. Import `time`:

```go
refresh := schedule.NewEntry("refresh-stock", refreshIfNeeded, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Every(5 * time.Minute))
	entry.Timeout(4 * time.Minute)
})
```

This cancels the handler context after four minutes. Pass it into database and
network calls, or explicitly check it during loops. If the function returns
`ctx.Err()`, the run is failed; returning nil instead reports success even if
the deadline passed. The default zero timeout imposes no facade bound, and
negative values are declaration errors.

No timeout forcibly terminates Go code. Driver-native activity or handler
timeouts can record an engine timeout while application code continues.
Memory detaches admitted callbacks from runner cancellation and drains without
a driver timeout; an application's shutdown wait budget is not a callback
lifetime bound. Bound task handlers themselves as shown in
[Deferred Tasks](deferred-tasks.md#registering-handlers), and keep their
dependencies alive until execution ends. See [Drivers](drivers.md) for native
timeout and shutdown policies.

## Failure Events

For an ordinary error, the scheduler logs the failure and duration, marks the
run span as failed, and returns an error wrapped as `run <name>: ...`. Execution
panics are recovered into errors carrying the panic value and stack, with
`schedule.panic` as telemetry's error type. Invalid task JSON fails during
decoding before the handler runs.

To notify an operator without embedding alert delivery in every handler,
configure events when constructing the scheduler. With an existing application
`app` and selected `driver`:

```go
scheduler := schedule.New(driver, func(clock *schedule.Builder) {
	clock.Events(app.Events())
})
```

Before execution, register a typed listener. Here, `notifyOnCall` is your
application's `func(context.Context, string) error` notification function:

```go
_, err := app.Events().Listen(schedule.RunFailed, func(ctx context.Context, failure schedule.RunFailedEvent) error {
	return notifyOnCall(ctx, failure.Name)
})
```

The event contains the name, entry/task kind, duration, and original error.
It also fires for `RunNow` failures. Listener errors and panics are logged by
the event bus and do not change the run result; remaining listeners are skipped
after that failure. Alert delivery is not a transaction with execution.

Automatic telemetry excludes error text, payloads, and job IDs, but failure logs
and events carry the error. Do not put secrets in errors. See
[Built-in Events](../events/built-in-events.md#scheduled-tasks) for payload details
and [scheduler telemetry](overview.md#events-and-telemetry) for instrumentation.

## Driver Recovery

A failed callback does not, by itself, end `Scheduler.Run`. The driver receives
the dispatch error and applies its execution policy:

- Memory logs the failure without retrying it; the next calendar tick is independent.
- Temporal configures one activity attempt per firing; a failed run does not
  prevent the next schedule firing.
- Restate makes ordinary application errors terminal. Missing dispatch and
  any error matching `schedule.ErrUnknownName` remain retryable, including an
  application error wrapping that sentinel.

Do not add submission retries as a substitute for understanding those policies.
The facade does not automatically retry `At` or `After`. A repeated submission
creates another job, and an uncertain transport result can require reconciliation.

Make effects idempotent even when failed handlers are not retried: repeated
delivery and interrupted execution can still repeat work. Overlap policy is
not deduplication and does not cover manual `RunNow`; see
[Overlap Policy](calendar-schedules.md#overlap-policy).

Runner startup failures are separate. Temporal returns reconciliation errors
from `Run`, allowing the application runner supervisor to stop the application.
Restate instead retries initial publication every second until ingress accepts
it or the context is canceled. An unreachable server can therefore leave
publication waiting rather than immediately failing the runner.

## Registration Errors

Handle returned registration and submission errors using `errors.Is` rather
than matching their text:

| Error | Meaning |
| --- | --- |
| `schedule.ErrInvalidName` | An entry or task name violates the portable naming rule. |
| `schedule.ErrInvalidEntry` | A required spec or handler is missing, timing is invalid, a timeout is negative, or `At` received zero time. |
| `schedule.ErrDuplicateName` | An entry or task already uses that name. |
| `schedule.ErrUnknownName` | `At` or `After` has no registered handler, or `RunNow` cannot find an entry. Application errors can also wrap this identity. |
| `schedule.ErrRunning` | Registration followed `Run`, or the facade was run a second time. |
| `schedule.ErrUnsupported` | The driver cannot perform the operation, such as an engine entry using the Local zone. |

`Add` validates its entire batch before changing registration. Entry definitions
retain their first declaration error; later callbacks cannot repair it.
`When` is required even if construction callbacks are omitted or empty.

Construction callbacks run once in order, including for invalid declarations.
A nil execution function is a returned declaration error, but a supplied nil
construction callback or callback panic propagates. Runtime handler panic
recovery does not apply during construction.

For every function and option, see [the `schedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule).
