---
title: Signals and Waiting
description: Wait for typed external messages or durable timers without blocking on application channels.
order: 40
summary:
  - Declare shared typed signals and reuse their first accepted value within a run.
  - Handle a confirmation deadline without hiding whole-run cancellation or timeout.
  - Select between signals and timers or pause through replay-safe runtime operations.
---

# Signals and Waiting

An onboarding run can send mail immediately, then wait days for the user to
confirm. A **signal** carries that external confirmation into the run. Durable
drivers retain accepted signals and waits in engine state; the memory driver
keeps them only while its process lives.

These examples use `arkoren.dev/core/service/workflow` and the overview's
[`User` and `SendWelcome`](overview.md#defining-work). Client-side sending is
covered by [Signaling Runs](running-workflows.md#signaling-runs).

## Declaring Signals

Declare a shared name and payload type for senders and waiters:

```go
var EmailConfirmed = workflow.NewSignal[string]("email-confirmed")
```

The signal carries a string token. A sender and waiter using the same name with
incompatible types is an application error. Names follow the workflow identifier
rule: 1–64 lowercase letters, digits, or inner hyphens. An invalid signal name
panics at declaration, so declare signals during startup rather than from input.

A signal resolves once per run. The first value is remembered: later Wait or
Select calls for that name return it immediately, even if more values were sent.
Signals accepted before the workflow reaches its wait are retained too. This is
not a message stream; use separate runs or another messaging capability when
every event needs processing. Workflow signals are also distinct from the
payload-less signals on the [event bus](../events/overview.md).

## Awaiting Confirmation

Replace the overview's short workflow body with this function when confirmation
belongs to the process. The function uses `errors` and `time` from the standard
library:

```go
func onboard(c *workflow.Context, user User) (string, error) {
	if _, err := c.Step(SendWelcome, user); err != nil {
		return "", err
	}

	token, err := c.Wait(EmailConfirmed, workflow.Timeout(72*time.Hour))
	if errors.Is(err, workflow.ErrTimeout) && c.Err() == nil {
		return "expired", nil
	}

	return token, err
}
```

Publish that function under the same definition shape, retaining a maximum that
exceeds the confirmation wait:

```go
var Onboard = workflow.New("onboard", onboard, func(definition *workflow.Builder) {
	definition.MaxDuration(96 * time.Hour)
})
```

After the step succeeds, a confirmation returns its token. If only the 72-hour
wait expires, the result is `"expired"`. The `c.Err() == nil` check prevents a
whole-run timeout from being mistaken for that ordinary expiry branch: both
use `workflow.ErrTimeout`. Cancellation or a run limit still ends the operation.

No Timeout, zero, or a negative Timeout disables only the per-wait limit. The
run's `MaxDuration` and cancellation remain in force. A remembered signal returns
without scheduling another wait timer.

Keep engine history and signal state for the full interval during which the run
must recover. Finished-run retention controls later inspection and duplicate
protection, not permanent delivery. See [Drivers](drivers.md) for provider
retention and queue limits; the memory driver cannot survive process loss.

## Racing Alternatives

If the deadline should trigger another action rather than return a timeout,
select between the confirmation and a timer inside your workflow function:

```go
var (
	token   string
	expired bool
)

err := c.Select(
	workflow.On(EmailConfirmed, func(value string) {
		token = value
	}),
	workflow.After(72*time.Hour, func() {
		expired = true
	}),
)
```

On success, exactly the chosen callback runs. Use `expired` to choose your next
step, or `token` to continue confirmation. Handle `err` before using either;
cancellation, decoding failures, or the whole-run timeout can prevent selection.
Callbacks run synchronously on the coordination path and must follow the same
[replay rules](overview.md#replay-rules) as the workflow body. Set local state in
them; perform external effects in subsequent steps.

An already remembered signal wins immediately, in case order if several are
remembered. Otherwise the runtime chooses the signal or timer that fires; do
not infer a portable ordering for simultaneous arrivals. A nonpositive After
delay is immediately eligible, not guaranteed to beat every available signal.
Select without cases returns `workflow.ErrNoCases`; On and After panic for nil
callbacks.

## Pausing Work

When no external message is needed, use a durable sleep instead of `time.Sleep`:

```go
err := c.Sleep(24 * time.Hour)
```

An engine can suspend the run during that pause. Sleep returns early when the run
is canceled or times out. Memory uses process-local timers instead.

Do not block on your own channel or on `<-c.Done()` inside coordination code.
Temporal cannot schedule a native Go channel block and reports a deadlock.
Use Sleep, Wait, or Select to block, and inspect `c.Err()` when you need the run's
cancellation reason. [Error Handling](error-handling.md) explains how that reason
differs from a caller abandoning a result wait.

For declarations and options, see the
[`workflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow).
