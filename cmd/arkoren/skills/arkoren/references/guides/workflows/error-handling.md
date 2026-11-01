---
title: Workflow Errors
nav: Error Handling
description: How steps retry and when to stop, which errors end a run, how panics and timeouts surface, and why messages must not hold secrets.
order: 20
---

# Workflow Errors

> [!NOTE]
> Hatchet references below are retained development context for a pending,
> unpublished integration, not available driver support. See the
> [workflow comparison](drivers.md#compatibility-matrix) for available drivers.

## Introduction

When a payment request fails, another attempt might succeed; when a card is
declined permanently, it will not. Make that retry decision inside the **step**,
where the failure occurs. The driver retries the step according to its policy.

The **workflow function** makes a different decision: what to do after the step
gives up. It can compensate for earlier work or return an error that fails the
run. Nothing retries that returned workflow failure. Engine replay rebuilds the
coordinator's state; it is not a retry policy for failed runs.

The error a run ends with reaches every caller of `Run.Result`, in every
process, as a value you can match with `errors.Is`. Understanding what survives
that trip, and what is stored along the way, is the second half of this page.

The snippets import `"arkoren.dev/core/service/workflow"` and use the
definitions and configured client from [the overview](overview.md).
`Order`, `Receipt`, the payment gateway, and the reserve/release steps below
belong to your application; they illustrate the compensation boundary.

## Retrying steps

A step runs once unless you ask for more with `Retry`, which sets the maximum
number of executions, not the number of retries. `Backoff` spaces them: the
first retry waits the initial delay, each later one multiplies it by the factor,
and the cap limits it. With `step.Retry(5)` and
`step.Backoff(time.Second, 2, time.Minute)` in a `*workflow.StepBuilder` callback, the
waits are one, two, four, and eight seconds. `Timeout` bounds each execution, one
minute by default, and a step that exceeds it fails that attempt with
`workflow.ErrTimeout` and is retried like any other failure.

The driver waits between attempts. With a durable engine, an hour-long backoff
does not require a live application goroutine; the memory driver keeps the run
in process. A step function must still honor its context so a timeout can stop
its work.

Native representation can limit a valid declaration. In particular, Hatchet
rejects unlimited registered step timeouts, and its retry-delay overrides need
engine v0.111.0+ with a sufficient native cap. Those delays have millisecond
precision. Temporal returns `workflow.ErrUnsupported` for multi-attempt policies
with zero initial backoff before scheduling any activity; it does not substitute
a positive delay. Single attempts and positive backoff remain supported. Check the
[driver limits](drivers.md) before relying on either policy.

> [!WARNING]
> Temporal's refusal changes the command sequence of older histories that used
> zero initial backoff. Keep affected runs on their original workers until they
> finish, or drain them before replacement. Mixing workers on one unversioned
> task queue is not safe routing, and the driver adds no replay marker or automatic
> migration. See [the Temporal rollout guidance](drivers.md#behavior-and-limits-1).

## Stopping retries

Some failures will never succeed: the card is stolen, the address does not
exist, the input is invalid. Retrying them wastes time and sometimes money.
Wrap such an error with `workflow.Permanent` and the step fails at once, with
attempts left over:

```go
func charge(ctx context.Context, order Order) (Receipt, error) {
	receipt, err := gateway.Charge(ctx, order)
	if errors.Is(err, gateway.ErrDeclined) {
		return Receipt{}, workflow.Permanent(err)
	}

	return receipt, err
}
```

`errors.Is(err, workflow.ErrPermanent)` recognizes a permanent error. The
workflow decides what to do next: return the error, or compensate and return a
different outcome. A panic inside a step is recovered and becomes a permanent
error, since a panic is a bug that a retry will repeat.

## Handling a failed step

A checkout may reserve stock successfully and then fail to charge the customer.
Ending the workflow is not enough: the reserved stock still needs to be
released. After a step exhausts its attempts, `c.Step` returns its error wrapped
with the step's name, so the workflow can decide whether to undo earlier work
before ending:

```go
func checkout(c *workflow.Context, order Order) (Receipt, error) {
	if _, err := c.Step(Reserve, order); err != nil {
		return Receipt{}, err
	}

	receipt, err := c.Step(Charge, order)
	if err != nil {
		if _, failure := c.Step(Release, order); failure != nil {
			return Receipt{}, errors.Join(err, failure)
		}

		return Receipt{}, err
	}

	return receipt, nil
}
```

Publish the checkout function with a one-hour limit:

```go
var Checkout = workflow.New("checkout", checkout, func(definition *workflow.Builder) {
	definition.MaxDuration(time.Hour)
})
```

The release is itself a step, so it is retried and recorded like the others.
The error a step returns reaches the workflow in the form it has on the wire, so
match it by kind, with errors made by `workflow.NewError`, and not with
`errors.As` into a type that only the step knows.

## Errors a run can end with

When a run fails, `Run.Result` returns an error you match with `errors.Is`.
Client operations can also fail without ending a run: a duplicate `Start`, for
example, reports that the identifier is already in use. Keep that distinction
when handling the shared sentinel errors:

| Error | Meaning |
| --- | --- |
| `workflow.ErrTimeout` | The run exceeded `MaxDuration`, a step attempt timed out, or a `Wait` timeout expired. A workflow can handle a step or wait error instead of failing the run. |
| `workflow.ErrCanceled` | The run, step, or wait was canceled. |
| `workflow.ErrNotFound` | The run or workflow does not exist; also returned by `Status`, `Signal`, and `Cancel`. |
| `workflow.ErrAlreadyStarted` | `Start` used an identifier that was already used; the existing run is returned, not failed. |
| `workflow.ErrUnsupported` | The driver cannot do what was asked, such as `Spawn` or a long `Delay`. |

These sentinels are `*workflow.Error` values with a stable **kind**. Matching
compares kinds, so it still works after an error crosses from a worker to your
process.

To give your own failures a kind that callers can match, create them with
`workflow.NewError`:

```go
var ErrCardDeclined = workflow.NewError("card-declined", "card declined")

func chargeStep(ctx context.Context, order Order) (Receipt, error) {
	return Receipt{}, workflow.Permanent(ErrCardDeclined)
}

// in the process that started the run
if _, err := run.Result(ctx); errors.Is(err, ErrCardDeclined) {
	// ask the customer for another card
}
```

The kind, message, and permanent marker cross the wire. The chain of wrapped errors
behind a failure, with its own types and values, does not, so a caller can match
a kind but never `errors.As` into a type defined in the worker. Kinds beginning
with `workflow.` are reserved.

The wait for a result is separate from the run. `Run.Result(ctx)` returns the
context's error when `ctx` ends first, and the run goes on; ask again later
with the same identifier.

## Waiting and timeouts

`Wait` with `workflow.Timeout` returns `ErrTimeout` when nothing arrives, and a
workflow usually handles that as a normal branch rather than a failure, as in
the [onboarding example](overview.md#a-first-workflow) that returns `"expired"`. Because the run's own
`MaxDuration` also reports `ErrTimeout`, check `c.Err()` if you need to tell the
two apart: it is nil while the run is alive and holds the reason once it has
been canceled or has timed out. `c.Done()` closes at that moment. With the
Temporal driver, poll `Done` with a non-blocking `select` and never block on it;
Temporal reports a deadlock otherwise.

## Panics

A panic in the workflow function is recovered and fails the run with an error
that begins `panic:`; the stack goes to the run's logger, not into the error.
Panics that an engine uses for its own control flow, such as the Restate SDK's
suspend and abort, are passed through untouched so replay keeps working. In every case, treat a panic as a bug
to fix, not as a retry mechanism.

This recovery covers application workflow and step bodies, not every native SDK
goroutine. Hatchet can panic asynchronously outside the host's recovery boundary;
its [startup and shutdown limits](shared-worker-host.md#hatchet) remain qualified
by the pinned SDK, and native lifecycle validation is not complete.

## Keeping secrets out of errors

> [!WARNING]
> The text of an error that ends a run is stored in the engine's history and
> returned to every caller of `Run.Result`, in any process. Do not put secrets or
> personal data in the messages of errors that a workflow or step returns.

An error like `fmt.Errorf("charge %s: %w", cardNumber, err)` writes the card
number into your engine's history and its backups. When the underlying error may
carry values, return `workflow.NewError` with a generic message and log the
detail separately:

```go
if err != nil {
	logger.ErrorContext(ctx, "charge failed", "order", order.ID, "error", err)

	return Receipt{}, workflow.NewError("charge-failed", "charge failed")
}
```

This hides the provider's message without misclassifying every failure as a
declined card. Mark the error permanent only when you know a retry cannot
succeed. Restrict access to the separate logs and redact sensitive provider
details there too.

## Mistakes caught at registration

Definitions keep their first error, and `workflow.Handlers` returns it before
anything runs: `ErrInvalidName`, `ErrNilFunc` for a nil function or a typed-nil
definition, `ErrInvalidOptions` for a negative duration or a `MaxDuration` of
zero or less, `ErrMissingMaxDuration`, and `ErrDuplicateName`. `c.Step` also
fails a step whose own error is set. An empty identifier, or one with control
characters, is `ErrInvalidID` from `Start`, `Signal`, `Run`, `Child`, and
`Spawn`. Handle the error from `Handlers` at startup: a worker with a broken
registry cannot run anything.

For every function and option, see [the `workflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow).
