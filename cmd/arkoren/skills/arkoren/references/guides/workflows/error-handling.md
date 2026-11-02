---
title: Error Handling
description: Classify step failures, compensate for earlier effects, and recognize errors across worker boundaries.
order: 70
summary:
  - Stop futile retries and explicitly compensate for earlier external effects.
  - Match stable error kinds across processes and distinguish attempt, wait, and run timeouts.
  - Keep persisted failure messages safe and validate definitions before worker startup.
---

# Error Handling

A temporary gateway failure can justify another step attempt; a permanently
declined payment cannot. The step reports that distinction. After attempts end,
the workflow chooses whether to compensate, return a different result, or fail.
Returning a workflow failure does not retry the whole run. Engine replay rebuilds
coordination state; it is not a retry policy for failed workflows.

Examples use `arkoren.dev/core/service/workflow`, standard `context` and `errors`,
and application-owned `Order`, `Receipt`, and gateway services. Configure attempt
counts and delays on [Workflow Steps](steps.md#retry-policy); native limits belong
on [Drivers](drivers.md).

## Stopping Retries

Use a stable application error for a condition callers must recognize. For
example, classify a gateway's permanent decline inside the step body:

```go
var ErrCardDeclined = workflow.NewError("billing.card-declined", "card declined")

func charge(ctx context.Context, order Order) (Receipt, error) {
	receipt, err := gateway.Charge(ctx, order)
	if errors.Is(err, gateway.ErrDeclined) {
		return Receipt{}, workflow.Permanent(ErrCardDeclined)
	}

	return receipt, err
}
```

The gateway and its ErrDeclined are your application's provider adapter.
Permanent stops further attempts even when the configured budget has attempts
left. Other failures remain eligible for the step's retry policy. A single
attempt is the default; a timed-out attempt can retry like another failure.
`errors.Is(err, workflow.ErrPermanent)` recognizes the permanent marker, and
`workflow.Permanent(nil)` remains nil.

## Compensating Work

A successful reservation is not undone when a later charge fails. Given
registered Reserve, Charge, and Release steps accepting an Order, explicitly
attempt release before returning the charge failure:

```go
func checkout(c *workflow.Context, order Order) (Receipt, error) {
	if _, err := c.Step(Reserve, order); err != nil {
		return Receipt{}, err
	}

	receipt, err := c.Step(Charge, order)
	if err != nil {
		if _, releaseErr := c.Step(Release, order); releaseErr != nil {
			return Receipt{}, errors.Join(err, releaseErr)
		}

		return Receipt{}, err
	}

	return receipt, nil
}
```

Publish this function with `workflow.New` and a suitable positive MaxDuration,
then register the workflow and all three steps. Release is itself recorded and
retried according to its policy. Compensation is another fallible effect, not a
database rollback or a guarantee that cancellation will leave time to undo work.
If the run's context has ended, later runtime operations can fail immediately.
Provide application-owned reconciliation for effects that remain unresolved.

The step failure reaching coordination code has already crossed the driver's
error representation. Match its stable kind rather than relying on a Go type
that exists only inside the step. Joining errors preserves local reporting;
do not expect every arbitrary Go cause to survive a wire round-trip.

## Matching Failures

Callers obtain a run's terminal failure from Result and use `errors.Is`:

```go
_, err := run.Result(ctx)
if errors.Is(err, ErrCardDeclined) {
	// Ask the customer for another card.
}
```

`workflow.Error` values have stable kinds. Kind, message, and permanent marker
survive encoding; arbitrary wrapped types and values do not. A worker-specific
`errors.As` target therefore cannot recover its original type in another
process. Nonempty equal kinds match; kinds beginning `workflow.` are reserved.

Distinguish errors from client commands from errors that end a run:

| Error | Meaning |
| --- | --- |
| `workflow.ErrAlreadyStarted` | Start found an existing run and also returned its handle; the existing run did not fail. |
| `workflow.ErrNotFound` | A workflow/run is unknown or forgotten; signaling a finished run also returns this error. |
| `workflow.ErrTimeout` | A step attempt, signal wait, or whole-run duration limit expired. |
| `workflow.ErrCanceled` | A run or its blocking operation was canceled. |
| `workflow.ErrUnsupported` | The driver cannot perform the operation or policy; retrying the same request does not add support. |

Provider lookup/transport errors remain errors rather than proof that work never
started. Reconcile uncertain outcomes by the same ID as described in
[Starting Runs](running-workflows.md#starting-runs).

## Timeout Boundaries

Three clocks answer different questions. A step Timeout limits one attempt.
`workflow.Timeout` limits one signal wait. MaxDuration limits the workflow run.
Each can produce `workflow.ErrTimeout`, but the workflow can handle an individual
step or wait failure without returning a failed run.

When branching on an ordinary wait expiry, also check that the run itself is
still alive:

```go
token, err := c.Wait(EmailConfirmed, workflow.Timeout(72*time.Hour))
if errors.Is(err, workflow.ErrTimeout) && c.Err() == nil {
	return "expired", nil
}
```

`c.Err()` is nil for a live run and reports `ErrCanceled` or `ErrTimeout` once
the run ends. Done closes when that state is observed. Never block on the native
Go channel inside a workflow function; Temporal reports a deadlock. Use durable
Wait, Sleep, or Select for blocking.

Separately, `run.Result(ctx)` returns a context error if the caller stops waiting
first. That does not cancel the run; read its result again with a fresh context.
Cancellation is cooperative, and native completion/retention boundaries remain
[provider-specific](drivers.md#compatibility-matrix).

## Panic Boundaries

A panic in a step body becomes a permanent failure rather than repeating the
same bug through retries. A workflow-body panic fails the run with a message
beginning `panic:`. Its stack goes to the logger, not the returned error. Fix the
bug; do not use panic as a retry mechanism.

Engine control-flow panics, including Restate suspension and abort, pass through
the adapter so native replay keeps working. Recovery of application bodies does
not promise recovery of every native SDK goroutine. Constructor callbacks and
client-call panics propagate; client telemetry records the bounded panic class
without changing the panic value.

## Safe Messages

> [!WARNING]
> Errors returned by steps or workflows can enter engine history, backups, and
> Result responses in other processes. Do not include secrets, personal data,
> or raw sensitive provider messages. Return a stable safe message instead.

For an unexpected provider failure, separate classification from private
diagnostics:

```go
if err != nil {
	logger.ErrorContext(ctx, "charge failed", "order", order.ID, "error", err)
	return Receipt{}, workflow.NewError("billing.charge-failed", "charge failed")
}
```

The application logger needs its own access controls and redaction; even the
order identifier or provider error can be sensitive. This generic error does not
misclassify an outage as a declined card. Mark it permanent only if you know
another attempt cannot succeed. Panic messages can also be persisted, so never
panic with sensitive data.

## Registration Errors

Validate definitions at startup with `workflow.Handlers` before using the registry.
Definitions keep their first declaration error. Registration reports
`ErrInvalidName`, `ErrNilFunc` (including typed-nil definitions),
`ErrInvalidOptions`, `ErrMissingMaxDuration`, or `ErrDuplicateName` as appropriate.
Negative definition durations are invalid, and MaxDuration must be positive.
An invalid signal name instead panics when NewSignal is called.

Runtime ID validation rejects empty IDs and ASCII control characters; Child and
Spawn also reject starting the current run under its own workflow name. These
fail with `ErrInvalidID`, not a backend submission error. `c.Step` rejects a
definition whose own Err is already set without executing it.

For declarations and options, see the
[`workflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow).
