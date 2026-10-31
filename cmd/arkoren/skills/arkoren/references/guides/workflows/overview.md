---
title: Workflows Overview
nav: Overview
description: Define workflows from steps, signals, sleeps, and child workflows, register them with a driver, start and signal runs, and know the rules.
order: 10
---

# Workflows Overview

## Introduction

Some processes take longer than a request and must not be lost halfway. A user
signs up, you send a welcome email, then wait up to three days for them to
confirm and mark the onboarding as expired if they do not. A payment is authorized, an order is
packed, a refund may follow weeks later. Written as ordinary code in a request
handler or a queue consumer, such a process breaks when the server restarts, and
you end up building a state machine in a database to survive it.

A **workflow** is that process written as one ordinary Go function. The function
runs **steps**, which are the parts that do real work and may fail, waits for
**signals** from the outside, sleeps, and starts other workflows. A **driver**
makes it durable: an engine such as Temporal, Hatchet, or Restate records each
result, so a run that is interrupted resumes where it was, and a sleep of three
days does not hold a goroutine for three days.

A workflow differs from its neighbors in how long it lives and how much it
remembers. A [queue](../queues/overview.md) job is one unit of work with a retry
policy. A [scheduled task](../scheduled-tasks/overview.md) starts work at a time.
A workflow connects many units of work into one run with a name, an identifier,
a status, and a result that you can ask for. The [memory driver](drivers.md#memory)
runs workflows inside the process for development and tests, and the
[Temporal](drivers.md#temporal), [Hatchet](drivers.md#hatchet), and
[Restate](drivers.md#restate) drivers make them durable. Your definitions do not
change between them, but supported operations and timing limits differ; check
the [driver comparison](drivers.md#compatibility-matrix) before choosing an engine.

## A first workflow

Keep coordination separate from side effects from the start. A durable engine
can **replay** the workflow function from the beginning, supplying recorded step
results to rebuild its state. Put mail, database, and network calls in steps,
not in the workflow function. Even a step can repeat if its effect succeeds
before its result is recorded, so its external effects must be idempotent.

The onboarding process above, written with Arkoren, has three definitions: a
step that sends the email, a signal that carries the confirmation token, and the
workflow that ties them together. Use this payload type for the examples:

```go
import "arkoren.dev/core/service/workflow"

type User struct {
	ID    int    `json:"id"`
	Email string `json:"email"`
}
```

Supply your application's `sendWelcome` function with the signature
`func(context.Context, User) (string, error)`. It sends mail through your mail
service, honors cancellation, and tolerates repeated calls. The examples keep
that application-specific implementation separate from workflow coordination.

Focused snippets on these pages keep error results visible but omit routine
propagation checks. Handle those errors in your application; complete workflow
functions and examples about failure retain their checks.

```go
var SendWelcome = workflow.NewStep("send-welcome", sendWelcome)

var EmailConfirmed = workflow.NewSignal[string]("email-confirmed")
```

With that step and signal defined, declare the workflow's control flow:

```go
func onboard(c *workflow.Context, user User) (string, error) {
	if _, err := c.Step(SendWelcome, user); err != nil {
		return "", err
	}

	token, err := c.Wait(EmailConfirmed, workflow.Timeout(72*time.Hour))
	if errors.Is(err, workflow.ErrTimeout) {
		return "expired", nil
	}

	return token, err
}
```

Publish that function with a maximum run duration:

```go
var Onboard = workflow.New("onboard", onboard, func(definition *workflow.Builder) {
	definition.MaxDuration(96 * time.Hour)
})
```

After the welcome step succeeds, this workflow waits up to 72 hours for the
confirmation token and returns it, or returns `"expired"` if the wait times out.
The 96-hour `MaxDuration` bounds the whole run and is required for registration;
it is separate from the signal's wait timeout.

Definitions are package-level values, immutable once built. Construction
callbacks configure one fresh draft, settings return that same draft, and
publication copies the finished settings. Retained drafts cannot change
definitions. Callbacks run directly once, synchronously in order; nil entries
and callback panics propagate. Expanded nil or empty callback slices omit edits.

The first mistake (a bad name, a nil function, a negative duration) is kept on
the value and returned when you register it. Among domain validation failures,
only a bad name passed to `NewSignal` panics; declare signals at startup so a
bad declaration fails there. Names are one to 64 lowercase letters, digits, or
inner hyphens, and workflows and steps share one namespace.

## Steps

A step is a named function with a typed input and output: the place where a
workflow touches the world. Send the email, charge the card, call the API. The
driver records what a step returned, so a replay reads a recorded result
instead of rerunning that completed step. This is not exactly-once delivery:
if the email was sent but the result was not recorded before a crash, the step
can run again. Make external effects idempotent.

A step runs once by default and has a one-minute timeout. `Retry` sets the
maximum number of executions and `Backoff` the delay before each retry: an
initial delay, a multiplying factor, and a cap. `Timeout` bounds one execution.
If temporary mail failures should retry, replace the baseline step declaration:

```go
var SendWelcome = workflow.NewStep("send-welcome", sendWelcome, func(step *workflow.StepBuilder) {
	step.Retry(5)
	step.Backoff(time.Second, 2, time.Minute)
})
```

For an existing payment step named `Charge`, derive a single-attempt variant
when the caller must not retry that operation:

```go
attempt := Charge.Derive(func(step *workflow.StepBuilder) {
	step.Retry(1)
})
receipt, err := c.Step(attempt, order)
```

Derive keeps the name, typed function, inherited policy, and first error; it
does not reset defaults. It leaves the base step unchanged. Workflows have no
Derive method. Registration snapshots each function and policy independently
of its published pointer.

A step is the right unit to make idempotent, because a retry can run it again
after it partly succeeded. What a failing step returns, and how to stop retries,
is covered in [Workflow Errors](error-handling.md).

## Signals and waiting

A signal is a typed message that code outside the workflow sends to a run:
for example, a webhook confirms an email or an admin approves a request.
`workflow.NewSignal[T]` declares the name and the value's type. Inside the
workflow, `Wait` blocks until the signal arrives, with an optional timeout that
returns `workflow.ErrTimeout`. For an approval process, first declare a string
signal, then wait for it inside the workflow function:

```go
var Approved = workflow.NewSignal[string]("approved")
```

```go
approver, err := c.Wait(Approved, workflow.Timeout(24*time.Hour))
```

A signal sent before the workflow reaches `Wait` is not lost, and the first
value of a signal is remembered for the rest of the run, so a second `Wait` on
the same signal returns the same value at once. These signals are unrelated to
the payload-less signals of the [event bus](../events/overview.md).

If an approval deadline should trigger escalation rather than fail the run,
use `Select` to wait for either the signal or a timer. With `Approved` declared
as a string signal, replace the preceding `Wait` with this alternative and
record which case completed:

```go
var (
	approver string
	escalate bool
)

err := c.Select(
	workflow.On(Approved, func(by string) {
		approver = by
	}),
	workflow.After(24*time.Hour, func() {
		escalate = true
	}),
)
```

If the workflow only needs to pause, use `Sleep`. `Sleep` and `Select` are
durable with an engine driver: the run is not tied to a process while it waits.

## Child workflows

A process may need another workflow's result before continuing, or it may hand
off work that should continue independently. `Child` starts a workflow and waits
for its result. Only creating the run acquires **cancellation ownership**: canceling
the parent or reaching its `MaxDuration` also stops that newly created child.
Joining an existing run ignores the new input and never acquires ownership,
even if another parent created it. Cancellation or timeout ends only the joining
parent's wait, not the existing run.

`Spawn` returns the child's identifier without waiting for its result, and the
child outlives the parent. Choose it only for explicitly detached work, not as a
fallback for parent-owned cancellation. Memory and Temporal support `Child`;
Restate and Hatchet return `workflow.ErrUnsupported` before submitting child work
because they cannot preserve the required atomic, replay-stable ownership
decision. Hatchet also rejects `Spawn`. Check [driver-specific limits](drivers.md)
before using these calls. Given workflow definitions `ChargeOrder` and `SendSurvey`:

```go
receipt, err := c.Child(ChargeOrder, "charge-"+order.ID, order)

id, err := c.Spawn(SendSurvey, "survey-"+order.ID, order)
```

Give a child an identifier that is derived from your data, as above. When a run
with that identifier already exists, `Child` joins it and ignores the new input,
and `Spawn` treats the duplicate as success, which keeps a replayed parent from
starting the same child twice while the engine retains that run. An empty
identifier, one with control characters, or the parent's own identifier for
the same workflow name is `workflow.ErrInvalidID`. How much of this works
depends on the driver; a driver that cannot do it returns `workflow.ErrUnsupported`, and
[Workflow Drivers](drivers.md) lists the differences.

## Rules for workflow functions

An engine may run your workflow function again from the start to rebuild its
state, feeding it the recorded results of earlier steps. The function must
therefore be **deterministic**: given the same recorded results it makes the
same calls in the same order. In practice:

- Do not read the clock, random numbers, the network, files, or global state in
  the function. Use `c.Now()` for the time and `c.UUID()` for a new identifier,
  which the driver records, and move any I/O into a step.
- Do not start goroutines or use channels of your own in the function. Use
  `Select` to wait on several things.
- Inputs and outputs must round-trip through the codec, which is
  `encoding/json/v2` by default. A `time.Duration` field does not marshal, so use
  an integer count of seconds in payload types.
- Keep the function short and let steps do the work. A change to a workflow that
  has runs in flight can break their replay, so add new behavior behind a new
  workflow name or a version in the input when runs of the old shape are still
  alive.

`MaxDuration` is required: it bounds the whole run, and `Handlers` returns
`workflow.ErrMissingMaxDuration` without it. A run that exceeds it fails with
`workflow.ErrTimeout`. Pick a value above the longest legitimate run, because a
workflow that waits three days needs a maximum of more than three. `Retention`
sets how long the engine keeps a finished run, where the engine has such a
setting.

## Registering and starting runs

Two roles use a workflow. A **worker** executes it, so it needs the definitions.
A **client** starts runs and talks to them, so it needs only the names. In a
small service one process is both. `workflow.Handlers` validates the definitions
and returns a registry, the driver is built from the registry, and
`workflow.NewClient` wraps the driver:

```go
import "arkoren.dev/core/service/workflow/memoryworkflow"

registry, err := workflow.Handlers(Onboard, SendWelcome)
driver := memoryworkflow.New(registry)
client := workflow.NewClient(driver)
```

Check the registry error before using it. The memory driver needs no start hook;
register `driver.Stop` with your application's cleanup and give it a deadline.
It cancels runs and bounds the cleanup wait, but cannot stop code that ignores
cancellation. See [Lifecycle and Ownership](../application/lifecycle-and-ownership.md).

Every worker process must register the same definitions under the same names.
Names, JSON payloads, and an explicit trace carrier cross the driver; Go contexts
do not. The engine drivers use a
[shared worker host](shared-worker-host.md) and build the driver from the host
and the registry; their wiring is in [Workflow Drivers](drivers.md).

After starting a run, send its confirmation signal and call `Result`
as shown below: it returns the token you sent. If no signal arrives, the example
returns `"expired"` after 72 hours. Use [synctest](testing.md#testing-a-workflows-logic)
to verify that timeout branch without waiting three days.

A run is started with an identifier you choose, which is how you refer to it
afterwards. Choose one that comes from your data, such as `"user-42"`, so a
duplicate request cannot start a second run:

```go
run, err := client.Start(ctx, Onboard, "user-42", user)
if errors.Is(err, workflow.ErrAlreadyStarted) {
	// run is the existing run
}
```

A duplicate `Start` returns the existing run together with an error that
matches `workflow.ErrAlreadyStarted`, so you can treat it as "already
happening". `Delay(d)` delays the beginning of the run, and `Metadata` attaches
string pairs where the driver supports them.

## Inspecting registered definitions

To review worker retry policy before starting runs, use the registry from your
registration setup. `Steps` and `Workflows` return eager `collection.Slice`
snapshots, sorted by name:

```go
retried := registry.Steps().Filter(func(_ int, handler workflow.StepHandler) bool {
	return handler.Options.Attempts > 1
})
byDuration := registry.Workflows().GroupBy(func(_ int, handler workflow.WorkflowHandler) time.Duration {
	return handler.Options.MaxDuration
})
```

`retried` preserves name order, as does each group in `byDuration`. These are
definition snapshots, not live queries or workflow execution histories. Editing
records cannot replace registered handlers or their policy. Handler functions,
their captures, and services remain shared: the collection wrapper adds
operations, not ownership or replay isolation.

## Talking to a run

The returned `*workflow.Run[Out]` is a typed handle. `Result` waits for the run to end and
returns its output; the context you pass bounds only your wait, not the run.
`Status` returns `Running`, `Completed`, `Failed`, or `Canceled`. `Cancel` requests
cancellation; it does not forcibly terminate application code. Native completion
and pre-execution retention have [provider-specific limits](drivers.md), so do
not infer retained results or permission to reuse an ID from cancel acceptance.
A signal goes through the client with the workflow, the identifier, the
signal, and the value:

```go
err := client.Signal(ctx, Onboard, "user-42", EmailConfirmed, token)

out, err := client.Run(Onboard, "user-42").Result(ctx)
```

`client.Run` makes a handle from an identifier without any network call, which
is how a different process, or a later request, picks a run up again. A signal
to a run that does not exist or has finished is `workflow.ErrNotFound`.

## Events and telemetry

With your existing application `app` and `workflow.ClientBuilder.Events`, the
client publishes an event each time one of its
calls succeeds: `workflow.run.started` from `Start`, `workflow.run.signaled`
from `Signal`, and `workflow.run.canceled` from `Run.Cancel`:

```go
client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
	client.Events(app.Events())
})
```

The payloads carry the workflow name, the run identifier, and the signal name,
never the input, output, or signal value. A failed call publishes nothing, so a
duplicate `Start` that returns `ErrAlreadyStarted` is silent, while canceling
a finished run succeeds and publishes. The events describe what your process
asked for, not what happens inside the run; [Built-in Events](../events/built-in-events.md#workflows)
describes them.

To distinguish slow client calls from slow work, configure
[telemetry](../observability/telemetry.md) independently in each client and worker
process. Given a started telemetry service named `observations`, replace the
earlier registry construction with worker-side registration:

```go
registry, err := workflow.NewRegistry([]workflow.Definition{Onboard, SendWelcome},
	func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(observations)
	})
```

Pass this registry to your memory driver or native runner. Then configure the
client in the process that starts and controls runs:

```go
client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
	client.Telemetry(observations)
	client.Events(app.Events())
})
```

Both settings default to the current process's `telemetry.Default()` on each
call or attempt; explicit services remain shared. A client setting does not
configure a worker, including a worker in the same process. Export telemetry
from every pod to a common backend to view distributed traces. A local event bus
or memory capture does not collect another pod's observations.

Client spans are `workflow start`, `workflow signal`, `workflow cancel`,
`workflow status`, and `workflow result`. Each call records
`arkoren.workflow.operation.count` and `arkoren.workflow.operation.duration`
(seconds), including validation failures. A duplicate start has the expected
`already_started` outcome, keeps its error and handle, and emits no started
event. **Result duration measures the caller's wait**, including decoding, not
the durable run's execution time. Canceling or timing out that wait does not
cancel the run. Polling never emits completed or failed events; there is no
portable terminal engine subscription.

Workers record `workflow step_attempt` only when `StepHandler.Handle` actually
runs. Every retry is another attempt; replayed coordination, recorded results,
suspension, signals, and sleeps do not count as attempts. The step's ordinary
`context.Context` carries the attempt span, so dependency calls made with it can
create child spans. Do not add telemetry side effects to workflow functions:
they can replay. Use engine history for durable run timing and state.

Attempt observations describe the handler's return, not the engine's terminal
decision. In particular, a Hatchet handler that returns nil after its deadline
produces one successful handler-attempt observation, while the adapter rejects
that late result as native `workflow.ErrTimeout`. The adapter does not emit a
second attempt observation for that decision; see its [timing limits](drivers.md#hatchet).
Do not treat an attempt's success as proof that the workflow completed successfully.

The client injects the start span into a separate carrier. Drivers persist and
forward it to actual attempts and supported child/spawn runs; retries and new
child attempts share the original start parent, not a fabricated orchestration
span. Joining an existing run keeps that run's original carrier. Signals observe
submission only and do not replace the start parent. See the
[propagation matrix](drivers.md#compatibility-matrix) for driver boundaries.
Trace carriers are stored with engine inputs or headers: restrict submission to
trusted callers and keep sensitive baggage out of your propagator.
Native SDK client calls bypass these observations and local client events. If
custom native scheduling bypasses Arkoren's runtime, it must explicitly carry
propagation.

If an option, codec, JSON extension hook, or driver panics during a client call,
the exact panic value still propagates. A started observation closes with the
bounded `workflow.panic` error class, never success or raw panic data. This does
not change step handlers' existing recovery policy.

Automatic telemetry never records payloads, signal values, outputs, metadata,
run identifiers, or raw error text. Metric labels contain only the fixed
operation, outcome, and classified error type. Use `c.Logger()` and the engine's
history for application-controlled progress reporting, with your own redaction
policy. `c.Raw()` exposes the native orchestration context when needed, but does
not make side effects replay-safe.

For every function and option, see [the `workflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow).
