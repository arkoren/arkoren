---
title: Running Workflows
description: Start, inspect, signal, and cancel identified runs through the workflow client.
order: 50
summary:
  - Start identified runs and reconcile duplicates or uncertain submission outcomes.
  - Read results, authorize signals, and request cancellation without confusing acceptance with completion.
  - Inspect registered policy and configure client events separately from worker attempt telemetry.
---

# Running Workflows

Use a workflow client to start a process and return to it from later requests or
another service. These examples assume a [configured driver](drivers.md), an
operation context `ctx`, and the confirmation-aware
[`Onboard` definition](signals-and-waiting.md#awaiting-confirmation).

The facade is safe for concurrent use and does no I/O during construction.
The driver owner remains responsible for startup and cleanup:

```go
import "arkoren.dev/core/service/workflow"

client := workflow.NewClient(driver)
```

## Starting Runs

Choose a stable business identifier so duplicate requests refer to the same
process. For an already validated signup represented by `User`:

```go
run, err := client.Start(ctx, Onboard, "user-42", user)
if err != nil && !errors.Is(err, workflow.ErrAlreadyStarted) {
	return err
}
```

This fragment uses the standard `errors` package. Success returns a typed handle,
not the workflow's result. A duplicate returns that existing handle together
with `ErrAlreadyStarted`; it does not replace the original input or restart the
run. Protection lasts only while the provider retains the execution record.
Temporal IDs are unique across workflow names in a namespace, so include enough
business context to avoid collisions.

An empty ID or one containing ASCII control characters fails with
`workflow.ErrInvalidID` before submission. Invalid workflow declarations and
payload encoding can also fail before driver I/O. A transport failure after
submission can leave the outcome uncertain: the engine may have accepted work.
Reconcile the same logical ID rather than starting a replacement with a new ID.

Starting a run is not atomic with committing your application database. If the
handoff must survive a crash between those operations, persist the intent through
an application-owned outbox or equivalent durable mechanism. A successful Start
also does not make every external effect exactly once; steps still need
[idempotency](steps.md#defining-steps).

For a deferred start, add a delay without sleeping in the caller:

```go
run, err := client.Start(ctx, Onboard, "user-42", user,
	workflow.Delay(time.Minute),
	workflow.Metadata(map[string]string{"source": "signup"}))
```

The driver schedules the delay. Metadata copies the outer map and is visible
through provider operational interfaces where supported; keep secrets and
personal data out of it. Native timing, metadata, and retention limits are on
[Drivers](drivers.md).

## Reading Results

A later request can recreate a typed handle without a network call:

```go
run := client.Run(Onboard, "user-42")
status, err := run.Status(ctx)
```

The handle itself does not establish existence. Status queries the driver and
returns `workflow.Running`, `Completed`, `Failed`, or `Canceled`; unknown or
expired records return `ErrNotFound`. Running includes a delayed run that has
not executed yet.

To obtain the typed output, wait for the run to finish:

```go
token, err := run.Result(ctx)
```

Result returns the confirmation token, `"expired"` for the workflow's ordinary
confirmation deadline, or the run's failure. The context limits only this wait:
if `ctx` ends, Result returns its context error and the run continues. Ask again
with a fresh context and the same ID. [Error Handling](error-handling.md) explains
which error identities survive the worker boundary.

## Signaling Runs

After verifying a confirmation request and authorizing it for the target user,
send the token through the shared signal definition:

```go
err := client.Signal(ctx, Onboard, "user-42", EmailConfirmed, token)
```

This accepts delivery to the identified run; it does not wait for application
processing or completion. Unknown or finished runs return `ErrNotFound`. A signal
accepted before the workflow reaches Wait is retained, and the first value for
that name resolves the run's signal. See
[Signals and Waiting](signals-and-waiting.md#declaring-signals) for repeated waits
and retention boundaries.

The workflow name and ID are routing information, not authorization. Keep
untrusted callers away from raw engine submission, and resolve which run they
may signal in application code rather than trusting an arbitrary request ID.

## Canceling Runs

To request that the process stop, cancel the run rather than the context of a
Result call:

```go
err := run.Cancel(ctx)
```

Canceling a known finished run succeeds without replacing its result. An unknown
run returns `ErrNotFound`. A successful call observes cancellation acceptance,
not forced termination of application code or proof that all cleanup completed.
Bodies must cooperate with cancellation, and external effects already performed
are not undone.

Inspect the resulting status or result when the terminal outcome matters.
[Restate pre-execution cancellation](drivers.md#cancellation-retention) can lose
the record without its required retention profile. Neither cancel acceptance
nor a missing lookup authorizes reusing a business ID without reconciliation.
Worker shutdown is a separate [host lifecycle](shared-worker-host.md) concern.

## Inspecting Definitions

To inspect registered worker policy rather than live runs, query the registry
you constructed at startup:

```go
retried := registry.Steps().Filter(func(_ int, handler workflow.StepHandler) bool {
	return handler.Options.Attempts > 1
})
```

Steps and Workflows return eager `collection.Slice` snapshots sorted by name;
filtering preserves that order. You can group workflows by their run budget:

```go
byDuration := registry.Workflows().GroupBy(func(_ int, handler workflow.WorkflowHandler) time.Duration {
	return handler.Options.MaxDuration
})
```

Each group preserves name order. These records are not execution histories or
live queries. Editing a returned record cannot replace a registered handler or
policy. Functions, their captures, and services remain shared; collection
operations do not create ownership or replay isolation.

## Events and Telemetry

Client events describe successful local calls, not engine transitions. Given
your application `app`, enable them when constructing its client:

```go
client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
	client.Events(app.Events())
})
```

Start emits `workflow.run.started`, Signal emits `workflow.run.signaled`, and
Cancel emits `workflow.run.canceled`. Payloads contain the workflow name and run
ID, plus the signal name where relevant; they omit input, output, and signal
values. Failed calls emit nothing. A duplicate Start is silent; a successful
Cancel of a finished run still emits. There is no portable terminal engine
subscription, and polling emits no completed or failed event. See
[Built-in Events](../events/built-in-events.md#workflows).

Configure [telemetry](../observability/telemetry.md) independently in clients and
workers. Given a started service `observations`, replace ordinary registry
construction with worker registration:

```go
registry, err := workflow.NewRegistry([]workflow.Definition{Onboard, SendWelcome},
	func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(observations)
	})
```

Pass the validated registry to the driver or native runner. Configure the client
separately, even if both roles share a process:

```go
client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
	client.Telemetry(observations)
	client.Events(app.Events())
})
```

Nil telemetry follows the current process's `telemetry.Default()` on every call
or actual attempt; explicit services remain shared. Export every process to a
common backend: a local event bus or memory capture does not collect observations
from other pods.

Client spans are `workflow start`, `workflow signal`, `workflow cancel`,
`workflow status`, and `workflow result`. Each call records
`arkoren.workflow.operation.count` and `arkoren.workflow.operation.duration` in
seconds, including validation failures. Duplicate starts retain their error and
handle but use the expected `already_started` telemetry outcome. Result duration
measures the caller's wait and decoding, not durable execution time.

Workers observe `workflow step_attempt` only when a step handler actually runs.
Every retry is another attempt; replay, recorded results, suspension, signals,
and sleeps are not attempts. Dependency calls using the step's ordinary context
can become child spans. An attempt records the handler's return, not the engine's
terminal decision; a successful attempt is not proof of successful run completion.
Use engine history for durable run timing and state, and do not add telemetry
side effects to replayable coordination code.

The start span travels in a separate persisted carrier, not in metadata or a Go
context. Actual step attempts and supported child/spawn runs receive it; joining
an existing run preserves its original parent. Signals observe submission and
do not replace that parent. Retries and new child attempts retain the original
start parent rather than a fabricated orchestration span. Check
[driver propagation and rollout](drivers.md#compatibility-matrix) before enabling
traced producers. Carriers enter engine inputs or headers: allow only trusted
submitters and keep sensitive baggage out of your propagator.

Automatic telemetry omits payloads, values, outputs, metadata, run IDs, and raw
error messages. Metric labels use only operation, outcome, and classified error
type. Client-call panics still propagate unchanged; a started observation closes
with `workflow.panic`, not success or the raw panic value. Application logging
through `c.Logger()` needs its own redaction policy.

Native SDK operations bypass these observations and local client events. Custom
native scheduling outside the adapter must explicitly carry propagation.

For declarations and options, see the
[`workflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow).
