---
title: Workflow Drivers
nav: Drivers
description: Choose between the memory, Temporal, Hatchet, and Restate workflow drivers, and install, wire, and configure each, with a local server.
order: 50
---

# Workflow Drivers

## Introduction

Memory loses runs on exit; Temporal, Hatchet, and Restate survive worker restarts.
Prefer your existing engine if it supports your operations: Restate lacks `Child`,
and Hatchet lacks both `Child` and `Spawn`. Start with the [overview](overview.md)
for portable definitions or [Choosing a Driver](../drivers/choosing-a-driver.md)
for other services. Install modules from your application's directory.

Declare workflows and steps with each workflow's `MaxDuration` as in the overview
before wiring a driver.

A [host](shared-worker-host.md) manages shared native resources and their ownership.
**Runners** register
definitions before startup; **drivers** control runs, including from client-only
processes.

## Compatibility matrix

Compare Arkoren's workflow operations, not each engine's full SDK. Restart
survival requires retained engine history; it does not cover engine data loss.

| Driver | Runs survive worker restart | `Child` | `Spawn` | Start delay | Separate client-only process |
| --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No | Yes | Yes | Yes | No |
| [Temporal](#temporal) | Yes | Yes | Yes | Yes | Yes |
| [Hatchet](#hatchet) | Yes | No | No | Partial — Cannot exceed TimeoutMargin. | Yes |
| [Restate](#restate) | Yes | No | Yes | Yes | Yes |

`Child` acquires cancellation ownership only when it creates the run. Joining an
existing same-ID run ignores new input and ends only the wait on parent
cancellation or timeout; it never takes ownership from another caller. Memory and
Temporal preserve that decision atomically, including across Temporal replay.
Restate rejects `Child` before sending, attaching, or canceling; Hatchet rejects
both child operations. These rejections return `workflow.ErrUnsupported`.
`Spawn` is explicitly detached work, not a lifetime-equivalent fallback for `Child`.
Memory requires one shared process-local driver. Hatchet also rejects delays
above its timeout margin. Check each provider's retention and replay limits.

Cancellation acceptance is not the same as retained cancellation. In particular,
Restate's retained results and duplicate-start protection for delayed or queued
runs require positive retention and the
[operator-managed preflight-retention profile](#retaining-canceled-runs-before-execution).
Without that profile, a canceled pre-execution invocation can disappear.

Hatchet's rows describe operations exercised on the pinned v0.111.0 profile, not
race-free or caller-bounded lifecycle behavior. A fresh full native race suite
passed on that profile, but repeated concurrent conformance still exposes an SDK
shutdown race; startup cancellation and asynchronous SDK panics also remain
unresolved. Keep the
[native limits](#hatchet) when selecting and operating this driver.

Use the [shared client/worker observability wiring](overview.md#events-and-telemetry)
for every driver below.
For Temporal, deploy carrier-aware workers before traced producers: old workers
cannot decode traced input envelopes. Pause producers if worker-first rollout
cannot be guaranteed. Legacy untraced histories retain their argument shape.

| Driver | Client calls | Step attempts | Start-to-attempt carrier | `Child` / `Spawn` carrier |
| --- | --- | --- | --- | --- |
| [Memory](#memory) | Yes | Yes | Yes — Copied start carrier. | Yes |
| [Temporal](#temporal) | Yes | Yes | Yes — Input envelope. | Yes |
| [Hatchet](#hatchet) | Yes | Yes | Yes — Wire field. | No — Unsupported child operations. |
| [Restate](#restate) | Yes | Yes | Yes — Invocation headers, extracted in Run attempts. | Partial — Spawn only; Child is unsupported. |

Examples assume the [overview's definitions](overview.md#a-first-workflow).
Handle registry/constructor errors before using results or starting hosts; wire
lifecycle hooks with the [host guide](shared-worker-host.md).

## Memory

Memory ships in `arkoren.dev/core`. Runs cannot survive process exit; sleeps,
retries, timeouts, and delays use the system clock, controlled by `testing/synctest`.

### Connecting

Build from a registry and wrap the driver in the ordinary client:

```go
import (
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
)

registry, err := workflow.Handlers(Onboard, SendWelcome)
driver := memoryworkflow.New(registry)
client := workflow.NewClient(driver)
```

The memory driver has no startup hook. Register `driver.Stop` for application
cleanup with a bounded context; it cancels runs but cannot forcibly terminate
application code. See [Lifecycle and Ownership](../application/lifecycle-and-ownership.md).

### Configuration

`memoryworkflow.Builder.Logger` selects logging (nil: `slog.Default()`). There are
no connection, persistence, or pool settings; step policies belong to definitions.

### Starting and confirming onboarding

Use the [start/signal examples](overview.md#talking-to-a-run) with this `client`.
`Result` returns the confirmation token. Client and worker must share this driver.

### Behavior and limits

Runs live in process-local goroutines: restart loses them, and signals to other
replicas find no run. A nil registry runs nothing. `Stop` cancels runs; afterward,
`Start` returns `memoryworkflow.ErrStopped`. Cancellation and timeouts cancel
the run's context, not code that ignores it. Give `Stop` a deadline: its context
bounds the cleanup wait, not the lifetime of noncooperative application code.
Arrange for workflow and step bodies to honor cancellation, and separately join
any background work your application starts.

Signals buffer up to 1024 values per run and signal name, then return
`memoryworkflow.ErrQueueFull`. Unknown or finished runs return `workflow.ErrNotFound`.
Without replay, this driver cannot reveal nondeterministic functions.

### API references

See [the `memoryworkflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow/memoryworkflow).

## Temporal

Temporal maps workflows to workflows and steps to activities, using its retries,
timers, signals, and replay. Choose it for an existing Temporal engine or its tooling.

### Installation

```sh
go get arkoren.dev/driver/temporal
```

### Connecting

Register definitions on a [Temporal host](shared-worker-host.md#temporal) before
`host.Start`. Construction does no I/O; `runner.Driver()` supplies the driver:

```go
import "arkoren.dev/driver/temporal/temporalworkflow"

registry, err := workflow.Handlers(Onboard, SendWelcome)
runner, err := temporalworkflow.NewRunner(host, registry)
client := workflow.NewClient(runner.Driver())
```

A client-only process sets `temporal.Builder.NoWorker(true)` in the host callback
and supplies the registry in a driver callback:

```go
driver, err := temporalworkflow.NewDriver(host, func(driver *temporalworkflow.DriverBuilder) {
	driver.Registry(registry)
})
```
Client-only construction can precede or follow host startup. A dialing host
needs `Start` before operations; `NewFrom` exposes its supplied native client
immediately, without transferring ownership or replacing worker startup.

### Configuration

Worker and client `DriverBuilder.Prefix` and `RunnerBuilder.Prefix` must match
(default `arkoren`) to address the same registered types.

A client-only driver needs definitions via `DriverBuilder.Registry` to set
execution timeouts from `MaxDuration`; an
unknown workflow name then returns `workflow.ErrNotFound`. `NewRunner` takes
its registry positionally, and `RunnerBuilder` has no `Registry` setter.
Both builders expose `Logger`; nil selects `slog.Default()` for adapter records.
Workflow logging uses Temporal's replay-aware logger, resolves bound and grouped
`slog.LogValuer` attributes before forwarding, and retains native level filtering.
The host's settings (address, namespace, task queue, TLS) are in
[Shared Worker Host](shared-worker-host.md#temporal).
Metadata stays in memo; no host propagator hook is required. Observe the
[worker-first tracing rollout constraint](#compatibility-matrix) before enabling
traced producers.

### Starting and confirming onboarding

Use the [start/signal examples](overview.md#talking-to-a-run), matching namespace,
task queue, and driver prefix across processes. `Result` returns the token.

### Behavior and limits

A workflow type is `<prefix>-wf-<name>`, a step is an activity named
`<prefix>-step-<name>`, and the run identifier is the Temporal workflow ID. Start
rejects a duplicate identifier even after that run has ended while native history
is retained, so identifiers are unique per namespace within that window. The
failure is `workflow.ErrAlreadyStarted`. `Result`, `Status`, `Cancel`, and `Signal`
describe the native execution and verify its
workflow type first. A handle with the wrong workflow name returns
`workflow.ErrNotFound`, without returning output or issuing the command. Grant
native describe permission as well as the permissions for the requested operation.

`Delay` becomes a start delay. A positive step timeout becomes the activity's
start-to-close timeout. `StepBuilder.Timeout(0)` leaves that attempt timeout
unset; the activity's schedule-to-close bound is the workflow's `MaxDuration`,
not an invented one-minute limit. `workflow.Permanent` becomes a non-retryable
error. Positive retry backoff is capped from the first retry; an uncapped policy
requests Go's largest duration rather than Temporal's implicit retry cap. The
server can reduce that value: native server 1.29.1 persisted `876000h` (100 times
365 days) in the tested two-attempt policy. This is not a universal backend bound
or a guarantee that every Go duration is representable; check the persisted
policy on your server.

Exact multi-attempt retries with zero initial backoff are not representable by
Temporal's native activity retry policy. The affected `Step` call returns
`workflow.ErrUnsupported` before encoding or scheduling any activity, even if its
handler would succeed on the first attempt. There is no one-nanosecond approximation
or whole-workflow retry fallback. Single attempts and positive initial backoff
remain supported. To retain an intentionally positive delay, configure it
explicitly, including on derived steps; the old fallback was one second before
applying the cap.

> [!WARNING]
> Older zero-initial-backoff histories recorded an activity command that the new
> worker now rejects. Replaying them on that worker can fail with `TMPRL1100`.
> Keep affected executions on their original workers until completion, or drain
> them before replacement. An unversioned task queue shared by old and new workers
> is not safe routing. The driver adds no replay marker or automatic migration;
> replay representative retained histories before rollout.

A sleep is a Temporal timer, and `Wait` and `Select` wait
on signal channels, cancellation, and timers. `MaxDuration` is enforced by a
timer inside the workflow, with Temporal's execution timeout as a backstop.

Step-result, child-result, existing-child join, and Spawn acknowledgment waits
observe the parent's cancellation and `MaxDuration`, without waiting for the
native future itself to finish. This ends the parent's wait, not arbitrary
application code that ignores cancellation.

`Child` owns cancellation only after Temporal atomically creates the native child;
newly created children stop with the parent through cancellation or parent-close
termination. If that ID already exists, the runner's join activity observes its
existing result, ignores the new input, and acquires no ownership, even if another
parent created the run. Parent cancellation or timeout ends only that observation.
Replay preserves the native creation decision without a new history command or
version marker. `Spawn` uses detached ownership but waits for start acknowledgment
within the parent's budget. An acknowledgment-wait error does not prove the child
never started: reconcile the same ID before submitting replacement work.
External client `Start` still rejects duplicate IDs.

Result conversion preserves the terminal failure in native chain order. An outer
timeout maps to `workflow.ErrTimeout` even if it carries an earlier application
failure; an outer application failure is not replaced by a timeout cause.
Application error kinds remain matchable, and unrelated native lookup/transport
errors remain errors, including non-comparable Go error values.

`Retention` and `Description` are ignored.
Inside the workflow, poll `Done` with a non-blocking `select` and never wait on
it, because Temporal reports a blocked read as a deadlock. A panic in workflow
code fails the workflow and is not retried as a task.

### Using Temporal SDK capabilities

`host.Client()` exposes `client.Client` from `go.temporal.io/sdk/client`; the
host guide shows borrowing one with `temporal.NewFrom`. `c.Raw()` exposes
`go.temporal.io/sdk/workflow.Context`; follow the
[replay rules](overview.md#rules-for-workflow-functions).

### Running locally

With Docker installed, save this as your application's `compose.yaml`:

```yaml
services:
  temporal:
    image: temporalio/temporal:1.5.1
    command: ["server", "start-dev", "--ip", "0.0.0.0", "--headless"]
    ports:
      - "127.0.0.1:17233:7233"
```

Run `docker compose up -d`, then
`docker compose exec temporal temporal operator cluster health --address 127.0.0.1:7233`
until it succeeds. Create the host using the published module:

```go
import "arkoren.dev/driver/temporal"

host := temporal.New(func(worker *temporal.Builder) {
	worker.HostPort("127.0.0.1:17233")
	worker.TaskQueue("onboarding")
})
```

Wire the runner and host hooks, then start/signal onboarding; `Result` returns
the token. This is development-only: `docker compose down --volumes` deletes history.

### API references

See [the `temporalworkflow` API reference](https://pkg.go.dev/arkoren.dev/driver/temporal/temporalworkflow).

## Hatchet

Hatchet maps workflows to durable tasks and steps to standalone tasks. Durable
eviction needs engine v0.80.0+, but the repaired registered retry timing needs
v0.111.0+ with an adequate native retry-delay cap. The host's older-version gate
does not establish retry-timing compatibility. The local profile below pins
v0.111.0, which supports these overrides; the pinned SDK's startup and shutdown
limits still apply.

### Installation

```sh
go get arkoren.dev/driver/hatchet
```

### Connecting

With a [configured Hatchet host](shared-worker-host.md#hatchet), register the
definitions before `host.Start`, then wrap the runner's driver:

```go
import "arkoren.dev/driver/hatchet/hatchetworkflow"

registry, err := workflow.Handlers(Onboard, SendWelcome)
runner, err := hatchetworkflow.NewRunner(host, registry)
client := workflow.NewClient(runner.Driver())
```

A client-only process sets `hatchet.Builder.NoWorker(true)` in its host callback
and uses `hatchetworkflow.NewDriver(host)` without a registry.

### Configuration

Match `DriverBuilder.Prefix`, `RunnerBuilder.Prefix` (default `arkoren`), and the
effective host namespace across worker/client processes. Within one tenant,
different namespaces isolate executions with the same prefix, workflow name,
and logical ID. Namespaces route; they do not authorize.

Both builders expose the same timing settings. `ScheduleTimeout` (24 hours)
bounds the wait for a worker slot. Once admitted, a waiting run keeps its slot
for `EvictionTTL` (one minute) before leaving it. `TimeoutMargin` (one minute)
is added to `MaxDuration` for Hatchet's execution timeout and also limits the
start delay you can request.

`Lookback` (30 days) bounds searches for runs started elsewhere and sets
signal-marker idempotency; see retention requirements below. `PollInterval`
(five seconds, jittered) bounds result-poll gaps. `LookupGrace` (1.5 seconds)
budgets discovery retry waits while engine listings become visible; negative
disables waiting. Native request time is additional, so it is not an operation
deadline.
The host's options are in [Shared Worker Host](shared-worker-host.md#hatchet).
Hatchet's carrier uses a dedicated wire field, not identity metadata. Older
workers ignore it; upgrade them before expecting the
[shared trace behavior](overview.md#events-and-telemetry).

### Starting and confirming onboarding

Use the [start/signal examples](overview.md#talking-to-a-run), matching host
namespace and driver prefix. `Result` returns the token. Use new logical IDs for
unrelated work: duplicate-start protection expires.

### Behavior and limits

Hatchet scopes run identifiers by the effective host namespace, driver prefix,
workflow name, and logical ID. A duplicate start of the same tuple is
`workflow.ErrAlreadyStarted`, also after the run has finished, until the
MaxDuration plus Retention window expires. If the workflow omits `Retention`,
the driver uses `Lookback` for that part of the window.

`NewRunner` rejects registered `StepBuilder.Timeout(0)` with
`workflow.ErrUnsupported` before registration: Hatchet needs a finite native
execution timeout. It also rejects retry counts, factors, caps, and timeout
arithmetic outside supported native ranges instead of narrowing or overflowing
them. Step timeouts are cooperative; a late successful return after the local
deadline becomes `workflow.ErrTimeout`. Native execution timeouts round up to
whole seconds, with one second of reporting grace for steps; neither timeout
forcibly stops application code.

Registered `Initial`, `Factor`, and `Max` produce explicit native retry delays.
The SDK truncates them to milliseconds, and a supporting engine can clamp them
to its configured maximum. Engine v0.111.0+ is required to honor these overrides;
older engines use factor/max backoff that cannot preserve arbitrary initial
delays. An explicit zero asks for no deliberately positive retry delay, not zero
scheduling latency or unlimited attempts. The SDK's precision and the engine's
configured cap still apply on supporting engines.

A `Delay` is a durable sleep at run start; exceeding the timeout margin returns
`workflow.ErrUnsupported`. Options given to a step for a single call can only
shorten the attempts and the timeout registered for it. Nonzero per-call backoff
is ignored; a derived zero initial delay overrides the registered positive delay.
Before deploying that override, follow the [affected-history rollout](#upgrading-legacy-runs).
Hatchet delivers the newest of several events that arrived before a
wait, so the first value of a signal is stored in a small marker run, and
waiters read it from there to keep the "first value wins" rule of the contract.
Signal values are scoped to the verified physical Hatchet execution, not the
reusable application ID. Reusing an application ID after workflow TTL expiry
cannot deliver an earlier execution's signal.

Marker idempotency lasts `Lookback` (30 days) from the first signal. Keep it and
engine event/marker retention through queueing, execution, and recovery: expiry
can lose signals or first-value guarantees. Separately, logical duplicate-start
protection expires `MaxDuration + Retention` after submission. Size both windows
for your required lifetime; see [retention details](hatchet-recovery.md#retaining-signals-through-recovery).

Signals accepted while queued remain eligible while their history is retained.
`MaxDuration` begins at the first worker invocation, with `Delay` added to the
deadline; `ScheduleTimeout` separately bounds worker admission.

`Child` and `Spawn` return `workflow.ErrUnsupported` without submitting work.
Workflows calling them fail unless they handle that error. `Step` and external
client `Start` remain supported. Native submission or ordinary `Start` inside a
parent is not a safe workaround: it can repeat completed child work on replay.

Discovery and signals verify the exact workflow and physical execution.
Unproven ownership returns `workflow.ErrNotFound`; verification can fail after
work starts. The driver caches only verified executions, not submission receipts.
Each logical lookup rediscovers and verifies the current execution; a recently
verified execution enables retries within its remaining `LookupGrace`, while an
unknown ID gets one discovery pass. The final retry uses the remaining grace
instead of dropping it when a full backoff would not fit. This does not guarantee
visibility or extend the wait window; native request time remains additional.
Reconcile trusted engine data before treating a verification error as proof that
no work started. See the
[recovery guide](hatchet-recovery.md#keeping-execution-identity-stable) for execution
keys, marker ownership, and wire fields. Native startup and asynchronous SDK
panic limits remain qualified by the pinned SDK; do not assume a fixed startup
time or that every worker failure returns from `host.Start`. See the
[host lifecycle limits](shared-worker-host.md#hatchet).

Attempt telemetry observes the handler's return, not subsequent native result
acceptance. A handler that returns nil after its deadline produces one successful
handler-attempt observation, while the adapter reports native
`workflow.ErrTimeout`. The adapter emits no second attempt observation for that
decision. Client `Result` observes the timeout separately; the successful handler
observation is not durable engine completion.

### Upgrading legacy runs

Legacy `<prefix>:<name>:<id>` runs are inaccessible to new clients, and new
workers reject them before application code, including during replay.

> [!WARNING]
> Pause producers and signal senders, drain old runs with the old
> deployment, then replace workers and clients together before resuming. Do not
> mix old and new formats in a rolling deployment. Manage remaining old runs
> through Hatchet only after verifying their exact workflow and physical ID.
> Reconcile their effects before starting those logical IDs again: new keys do
> not collide with old idempotency records and can repeat business work.

Drain v2 runs lacking a signal version or using `signal_version: 1` too. Workers
accept only version 2; older v2 runs remain queryable/cancelable, not signalable
or replayable. Follow the [upgrade procedure](hatchet-recovery.md#replacing-an-older-deployment),
keeping namespace and prefix stable while runs must remain accessible.

> [!WARNING]
> Child/Spawn calls now return `workflow.ErrUnsupported`, including on replay of
> `signal_version: 2` workflows. Pause producers, drain affected workflows on the
> old deployment, then replace clients/workers together. Before retrying or
> replacing runs, reconcile existing child effects by verified physical ID:
> containment does not undo work. Never mix workers that submit children with
> workers that reject them.

Derived zero-delay overrides add a private `retry_without_delay` step-input field;
old workers ignore it. Upgrade every worker before relying on that field. Drain
existing workflows containing affected derived calls on their original deployment
before replacing it: replay can otherwise change a recorded native child input.
Unaffected calls retain their earlier input shape; this adds no durable node or
automatic history migration.

### Using Hatchet SDK capabilities

`host.Client()` exposes `*hatchet.Client` from
`github.com/hatchet-dev/hatchet/sdks/go`; `c.Raw()` exposes its `DurableContext`.
Follow the [replay rules](overview.md#rules-for-workflow-functions); native
submission does not supply portable Child/Spawn trigger-and-join behavior.

### Running locally

With Docker installed, the following `compose.yaml` supplies an isolated
development engine, never production. It pins engine v0.111.0, the first release
with explicit retry-delay overrides, to a multi-architecture image digest. This
does not update the Go SDK or prove a sufficient engine retry-delay cap for your
policy. The startup and shutdown limits above still apply despite a passing full
native suite. Validate your deployed profile before depending on those guarantees.

```yaml
services:
  postgres:
    image: postgres:15.19
    environment:
      POSTGRES_USER: hatchet
      POSTGRES_PASSWORD: hatchet
      POSTGRES_DB: hatchet
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U hatchet -d hatchet"]
      interval: 2s
      timeout: 5s
      retries: 20
  hatchet:
    image: ghcr.io/hatchet-dev/hatchet/hatchet-lite:v0.111.0@sha256:0776a420bc6af14cc0ca4e5ddb0f6ad6e64375ccbeb2accdc1a88c9877969085
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      DATABASE_URL: postgresql://hatchet:hatchet@postgres:5432/hatchet?sslmode=disable
      SERVER_AUTH_COOKIE_DOMAIN: localhost
      SERVER_AUTH_COOKIE_INSECURE: "t"
      SERVER_AUTH_SET_EMAIL_VERIFIED: "t"
      SERVER_GRPC_BIND_ADDRESS: 0.0.0.0
      SERVER_GRPC_INSECURE: "t"
      SERVER_GRPC_BROADCAST_ADDRESS: localhost:17077
      SERVER_INTERNAL_CLIENT_INTERNAL_GRPC_BROADCAST_ADDRESS: localhost:7077
      SERVER_GRPC_PORT: "7077"
      SERVER_URL: http://localhost:18888
      SERVER_DEFAULT_ENGINE_VERSION: V1
    ports:
      - "127.0.0.1:17077:7077"
      - "127.0.0.1:18888:8888"
```

Run `docker compose up -d`, then
`curl --fail http://localhost:18888/api/ready` until it succeeds. Open
`http://localhost:18888`, sign in with the local Hatchet Lite administrator
account (`admin@example.com`, initial password `Admin123!!`), and create an API
token for its tenant. Set `HATCHET_CLIENT_TOKEN` to that token. Never use these
local credentials or plaintext transport in production.

```go
import "arkoren.dev/driver/hatchet"

host := hatchet.New(func(worker *hatchet.Builder) {
	worker.HostPort("127.0.0.1:17077")
	worker.TLSStrategy("none")
})
```

Wire the runner/hooks and start/signal onboarding; `Result` returns the token.
`docker compose down --volumes` deletes the engine, database, tokens, and history.

### API references

See [the `hatchetworkflow` API reference](https://pkg.go.dev/arkoren.dev/driver/hatchet/hatchetworkflow).

## Restate

Restate serves workflows as workflow services and steps inline as durable
actions. Choose it for durable runs on Restate without a separate worker fleet.

### Installation

```sh
go get arkoren.dev/driver/restate
```

### Connecting

First configure a reachable endpoint and request-identity keys with the
[Restate host guide](shared-worker-host.md#restate). Before `host.Start`, bind one
service per workflow through the runner, then create the client driver:

> [!WARNING]
> Upgrading changes the journal sequence of workflows that called `Child`:
> those calls now return `workflow.ErrUnsupported`. Drain affected runs on their
> original deployment before replacement; do not force-migrate their histories.
> The driver adds no replay marker or automatic migration. Completed retained
> outputs remain readable, but `Spawn` is not an equivalent cancellation fallback.

```go
import "arkoren.dev/driver/restate/restateworkflow"

registry, err := workflow.Handlers(Onboard, SendWelcome)
_, err = restateworkflow.NewRunner(host, registry)
driver, err := restateworkflow.NewDriver(host)
client := workflow.NewClient(driver)
```

Register `host.Start`/`host.Stop` with `composer.Startup`/`composer.Cleanup` and `host.Serve`
with `composer.Run` inside a composer, as in
the host guide. Binding neither starts the listener nor registers the deployment.

A client-only process sets `restate.Builder.ClientOnly(true)` in its host callback
and uses `NewDriver`, which binds nothing. See [Shared Worker Host](shared-worker-host.md#restate)
for identity keys and other host options.

### Configuration

Match `Prefix` (default `arkoren`) and codec across processes so the driver
addresses the runner's bound services.
`DriverBuilder` and `RunnerBuilder` each expose `Prefix` and `Logger`. When
forming service names, the driver removes dots and dashes from the prefix and
uppercases the following letter.
Restate's `arkoren-trace-*` invocation headers are separate from `arkoren-meta-*`
metadata and native attempt headers. Older workers ignore them; upgrade workers
before expecting the [shared trace behavior](overview.md#events-and-telemetry).

### Starting and confirming onboarding

After endpoint registration, use the [start/signal examples](overview.md#talking-to-a-run)
with matching prefix/codec across processes. `Result` returning the token confirms
execution by that endpoint.

### Behavior and limits

Duplicate starts return `workflow.ErrAlreadyStarted`; undeployed workflows
return `workflow.ErrNotFound`. Result errors preserve sentinel and application
error kinds. Signals resolve durable promises with first-value-wins behavior.
`Cancel` closes a local run's `Done`, then requests native cancellation at a
durable-operation boundary. Retention before execution is conditional; configure
the [preflight-retention profile](#retaining-canceled-runs-before-execution) before
relying on canceled results or duplicate-start protection for delayed or queued
runs. Do not use cancellation acceptance or a missing lookup alone as permission
to reuse a logical ID.

The adapter explicitly supplies the initial retry interval, multiplier, maximum
attempts, and maximum interval. Zero initial backoff remains an explicit zero;
zero maximum uses Go's largest duration instead of the SDK's default cap. Native
retry timing has millisecond precision and the multiplier uses `float32`.
Step attempts run cooperatively, without an adapter goroutine that abandons the
body. After the body returns, its context is checked: late success after an
attempt deadline is `workflow.ErrTimeout`, not a successful result. A body that
ignores cancellation can still prevent return.

`MaxDuration` is a durable timer that begins when the run first executes, and it
is enforced at blocking runtime operations; it does not forcibly interrupt a
running Go step. The handler's own inactivity timeout is a fixed 24 hours.
`Child` returns `workflow.ErrUnsupported` before any send, attach, or cancellation:
the durable receipt identifies an invocation but cannot atomically distinguish
creation from joining across replay. A lookup followed by a send cannot establish
cancellation ownership. `Spawn` remains supported only for detached work; it is
not a fallback when the parent must own child cancellation.

`Retention` becomes the Restate workflow retention, one day by default. The Restate
SDK logs an error line for each failed step attempt that will be retried.

The Restate server must reach the advertised URL, and the deployment must be
registered, by `restate.Builder.Register(true)` or by an operator. Each rollout is a new
deployment, so keep workflows compatible with runs in flight. The Cancel call
reads Restate's introspection tables through the admin API, which must therefore
be reachable. Secondary result and pending-state probes preserve lookup failures
instead of treating unavailable APIs as `workflow.ErrNotFound`. Signal delivery
is forced only after proving that the invocation is pending. The local server
profile pins Restate 1.7.12.

### Retaining canceled runs before execution

Portable retained cancellation of delayed or queued workflows requires positive
effective workflow `Retention` and the persisted server feature
`preflight_invocation_termination_retention`. With that profile, a canceled
pre-execution run retains `workflow.ErrCanceled`, `Canceled` status, and duplicate
protection for its retention window. Without the feature, the legacy server
profile discards the invocation: `Result` and `Status` return
`workflow.ErrNotFound`, a repeated `Cancel` can return `ErrNotFound`, and `Start`
can immediately reuse the key. This native behavior is not permission to repeat
business work without reconciliation.

Restate 1.7.12 includes the opt-in feature introduced in 1.7.8. Operators request
it with
`RESTATE_EXPERIMENTAL_ENABLE_PREFLIGHT_INVOCATION_TERMINATION_RETENTION=true`
and must ensure its persisted activation on the relevant partitions. Enabling it
establishes a persistent minimum server version; plan the compatibility change
before applying it to an existing cluster. After the feature barrier applies, do
not downgrade below Restate 1.7.8. The feature cannot restore records already
discarded. See the [native feature's release notes](https://github.com/restatedev/restate/blob/v1.7.12/release-notes/v1.7.8.md#opt-in-retention-for-terminated-pre-flight-invocations).

The driver neither enables nor verifies this operator-managed profile. `/health`
checks HTTP readiness; `/version` reports requested flags, not per-partition
barrier activation. Public admin SQL on pinned 1.7.12 cannot expose the internal
feature state needed to prove that activation. None is a retained-cancellation
readiness gate, and no local tombstone or silent fallback supplies the guarantee.
VQueues are not required for preflight retention itself.

### Using Restate SDK capabilities

`c.Raw()` exposes `restate.WorkflowContext` from `github.com/restatedev/sdk-go`;
follow the [replay rules](overview.md#rules-for-workflow-functions).
`runner.Services()` lists bound services. `host.Query` accesses admin SQL:
restrict its credentials and never expose arbitrary queries to untrusted callers.

### Running locally

With Docker installed, save this as your application's `compose.yaml`. This
profile requests preflight retention on a fresh disposable server. Do not reuse
it to change an existing cluster without the compatibility planning above; the
environment flag alone does not prove persisted activation.

```yaml
services:
  restate:
    image: docker.restate.dev/restatedev/restate:1.7.12
    environment:
      RESTATE_EXPERIMENTAL_ENABLE_PREFLIGHT_INVOCATION_TERMINATION_RETENTION: "true"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    ports:
      - "127.0.0.1:18080:8080"
      - "127.0.0.1:19070:9070"
```

Run `docker compose up -d`, then `curl --fail http://localhost:19070/health`
until it succeeds. This confirms HTTP readiness, not retained-cancellation
readiness. For a Go process running on your machine, configure the
ingress, admin API, and reverse connection explicitly:

> [!WARNING]
> This local configuration disables request-identity verification and listens
> on port 9080. Anyone who can reach that port can invoke handlers. Use it only
> on an isolated development machine; production needs identity keys and network
> access controls.

```go
import "arkoren.dev/driver/restate"

host := restate.New(func(worker *restate.Builder) {
	worker.Addr(":9080")
	worker.IngressURL("http://localhost:18080")
	worker.AdminURL("http://localhost:19070")
	worker.AdvertisedURL("http://host.docker.internal:9080")
	worker.Register(true)
	worker.AllowInsecure(true)
})
```

Wire `host.Start`/`Serve`/`Stop` as in Connecting and start/signal onboarding.
`Result` returning the token confirms registration and execution.
`docker compose down --volumes` deletes the disposable server and history.

### API references

See [the `restateworkflow` API reference](https://pkg.go.dev/arkoren.dev/driver/restate/restateworkflow).
