---
title: Workflow Steps
description: Run external effects with explicit attempt policies and choose the right child-workflow lifetime.
order: 30
summary:
  - Put external effects in typed steps with idempotency and explicit attempt policies.
  - Derive a local step policy without changing its registered definition.
  - Distinguish owned child creation, non-owning joins, and detached work.
---

# Workflow Steps

A workflow decides what happens next; a **step** performs work outside the
engine, such as sending a welcome message. The driver records the step's result
and can supply that result during replay instead of repeating completed work.
Use steps for every external effect in a
[workflow function](overview.md#replay-rules).

## Defining Steps

An effect can succeed before its result reaches engine history. Retrying or
recovering the step can therefore repeat that effect. Use a stable business key
with the external service, or another application-owned idempotency mechanism;
recording the step is not an exactly-once guarantee.

For the overview's `User`, supply your application's cancelable, idempotent
`sendWelcome(context.Context, User) (string, error)` function. Declare it with
`arkoren.dev/core/service/workflow`. Policy examples also use the standard
`time` package:

```go
var SendWelcome = workflow.NewStep("send-welcome", sendWelcome)
```

Register it alongside the workflow with `workflow.Handlers(Onboard, SendWelcome)`.
Inside that workflow, call the published definition:

```go
messageID, err := c.Step(SendWelcome, user)
```

The result is a string, matching the step's output type. A failure is wrapped
with the step name. A declaration with a non-nil `Err()` fails without running;
registration catches these errors before workers start.

## Retry Policy

A step runs once by default, with a one-minute timeout for that attempt. Enable
more attempts when failures are transient and repeating the effect is safe:

```go
var SendWelcome = workflow.NewStep("send-welcome", sendWelcome, func(step *workflow.StepBuilder) {
	step.Retry(5)
	step.Backoff(time.Second, 2, time.Minute)
	step.Timeout(30 * time.Second)
})
```

This replaces the baseline declaration rather than registering another step with
the same name. It allows five executions total, with retry delays of one, two,
four, and eight seconds. Timeout bounds each attempt, not all five together;
the body must honor its `context.Context` so cancellation can stop its work.

Without an explicit Backoff, enabled retries start at one second, multiply by
two, and cap at 30 seconds. `Retry` values below one become one. A zero backoff
maximum removes the cap; zero initial delay requests no deliberate positive
delay, not an engine default. Factors below one or nonfinite factors become one.
Negative initial delay, cap, or timeout is a declaration error. `Timeout(0)`
removes the per-attempt limit, not the workflow's duration limit.

Native policies are not interchangeable. Temporal rejects multi-attempt calls
with zero initial backoff before scheduling work; Restate retry delays have
millisecond precision. Check [driver limits](drivers.md) before relying on these
settings, and do not bypass a rejection with an application retry loop in the
workflow function.

Return `workflow.Permanent(err)` when another attempt cannot succeed. After
attempts stop, the workflow decides whether to return the error or compensate
for earlier work. [Error Handling](error-handling.md) covers that decision and
stable error kinds.

## Local Overrides

When one call needs a different policy, derive a variant rather than mutating
the registered step. For example, a caller can deliberately allow only one attempt:

```go
singleAttempt := SendWelcome.Derive(func(step *workflow.StepBuilder) {
	step.Retry(1)
})
messageID, err := c.Step(singleAttempt, user)
```

Derive keeps the name, typed body, inherited policy, and first declaration
error, then applies edits. It does not reapply defaults or change `SendWelcome`.
The registered handler still supplies the work; the derived definition supplies
this call's policy. Workflows have no Derive method.

## Child Ownership

When a larger process needs another workflow's result, use `Child` only on a
[compatible driver](drivers.md#compatibility-matrix). Memory and Temporal support
it; Restate rejects it before submission.

Assume your application has a registered `FulfillOrder` workflow with an `Order`
input and a typed receipt output. Inside a parent workflow, derive its ID from
the order's stable string ID:

```go
receipt, err := c.Child(FulfillOrder, "fulfillment-"+order.ID, order)
```

If the call creates the run, it acquires **cancellation ownership**: canceling
the parent or reaching its `MaxDuration` also stops that child. If the ID already
exists, Child joins the existing run and ignores the new input. Joining never
acquires ownership, even when another parent created the run; ending the parent
ends only its wait, leaving the existing child with its original owner.

Creation versus joining must be atomic and stable across replay. A lookup
followed by a separate start cannot establish ownership. Do not replace an
unsupported Child with an ordinary client Start inside coordination code: that
is external I/O and does not preserve this contract.

## Detached Work

If work must continue independently, use Spawn instead. Given a registered
`SendSurvey` workflow accepting the same order:

```go
id, err := c.Spawn(SendSurvey, "survey-"+order.ID, order)
```

Spawn returns the ID without waiting for the workflow's result. The run outlives
its parent, so this is not a substitute for owned Child cancellation. An existing
ID is treated as success while its record remains retained; use
[a client handle](running-workflows.md#reading-results) to inspect the result later.

Both calls reject empty IDs, IDs containing ASCII control characters, and the
parent's own ID for the same workflow name with `workflow.ErrInvalidID`. Stable
business IDs avoid a replay starting a different child, but protection expires
with the run's retained history. On Temporal, Spawn waits for start acknowledgment
within the parent's budget: an error from that wait does not prove no child
started. Reconcile the same ID before replacing work.

For declarations and options, see the
[`workflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow).
