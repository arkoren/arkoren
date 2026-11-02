---
title: Workflow Drivers
nav: Drivers
description: Choose and configure memory, Temporal, or Restate for workflow execution.
order: 20
summary:
  - Compare available drivers by persistence, child ownership, retries, and cancellation retention.
  - Wire memory, Temporal, or Restate to the shared workflow client and verify delivery locally.
  - Preserve native policy limits and retained histories during worker replacement.
---

# Workflow Drivers

Use memory for process-local work and tests. Choose Temporal or Restate when
runs must survive worker replacement, provided the engine retains their history.
An existing engine is a useful starting point, but check the operations and
retention your process needs before committing to it. Hatchet is pending and
unpublished; it is not an available integration.

Examples use the [onboarding definitions](overview.md#defining-work) and import
`arkoren.dev/core/service/workflow`. Handle registry and constructor errors
before using their results. A **runner** registers definitions for execution;
a **driver** starts and controls runs. Native drivers share a
[worker host](shared-worker-host.md) with scheduler drivers.

## Compatibility Matrix

These capabilities describe Arkoren's adapters, not the vendors' entire SDKs.
Restart survival assumes retained engine data, not protection against engine
data loss. Memory clients must share one driver in one process.

| Driver | Restart Survival | Owned Child Runs | Detached Runs | Start Delay | Client-Only Process |
| --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No | Yes | Yes | Yes | No |
| [Temporal](#temporal) | Yes | Yes | Yes | Yes | Yes |
| [Restate](#restate) | Yes | No | Yes | Yes | Yes |

`Child` owns cancellation only when it creates a run; joining an existing run
must never acquire that ownership. Memory and Temporal preserve this distinction
atomically. Restate returns `workflow.ErrUnsupported` before submitting child
work. `Spawn` is detached and is not a cancellation-equivalent fallback.

| Driver | Finished Retention Setting | Retained Pre-Execution Cancellation | Zero-Backoff Retries |
| --- | --- | --- | --- |
| [Memory](#memory) | Yes — One hour by default, in this process only. | Yes — While the process and retained record remain. | Yes |
| [Temporal](#temporal) | Partial — Namespace policy; workflow Retention is ignored. | Yes — Subject to native history retention. | No — Multiple attempts with zero initial backoff are rejected. |
| [Restate](#restate) | Yes — One day by default. | Partial — Requires positive retention and persisted preflight-retention activation. | Partial — Explicit zero is supported with native millisecond precision. |

Cancellation acceptance alone does not establish a retained outcome or permission
to reuse an identifier. See [Restate retention](#cancellation-retention) before
depending on duplicate protection for delayed or queued runs.

All three drivers support [client and worker observations](running-workflows.md#events-and-telemetry).
Their propagation boundaries differ:

| Driver | Client Calls | Step Attempts | Start-To-Attempt Carrier | Child/Spawn Carrier |
| --- | --- | --- | --- | --- |
| [Memory](#memory) | Yes | Yes | Yes — Copied start carrier. | Yes |
| [Temporal](#temporal) | Yes | Yes | Yes — Dedicated input envelope. | Yes |
| [Restate](#restate) | Yes | Yes | Yes — Invocation headers extracted in actual attempts. | Partial — Spawn only; Child is unsupported. |

## Memory

Memory is included in `arkoren.dev/core`. It runs workflows in local goroutines;
replicas share neither runs nor signals, and restart loses both. It uses the
system clock, so [synctest](testing.md#workflow-decisions) can advance waits and
backoff without real delays. It does not replay coordination code.

### Connecting Locally

Build a registry before constructing the driver:

```go
import "arkoren.dev/core/service/workflow/memoryworkflow"

registry, err := workflow.Handlers(Onboard, SendWelcome)
```

After handling `err`, wrap the driver in the ordinary facade:

```go
driver := memoryworkflow.New(registry)
client := workflow.NewClient(driver)
```

There is no startup hook. Register `driver.Stop` with application cleanup and
give cleanup a deadline. Stop cancels runs and joins workflow and step-attempt
goroutines until they return or its context ends. It cannot kill code that
ignores cancellation; a later Stop can continue joining it. After Stop, new
starts return `memoryworkflow.ErrStopped`. A nil registry executes nothing.

### Local Policy

The only driver setting is `memoryworkflow.Builder.Logger`; nil selects
`slog.Default()`. Step retry and timeout policies belong to the definitions,
not the driver. Finished runs remain for their `Retention`, one hour when zero;
expiry removes results and duplicate-start protection.

Use [Starting Runs](running-workflows.md#starting-runs) with this `client`.
With the confirmation-aware definition, signaling then reading `Result` returns
the supplied token. Signals buffer at most 1024 undelivered values per run and
signal name; another send returns `memoryworkflow.ErrQueueFull` without enqueuing
that value. Unknown or finished runs return `workflow.ErrNotFound`.

See the [`memoryworkflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow/memoryworkflow).

## Temporal

Temporal executes workflow functions as native workflows and steps as activities.
Choose it when you use Temporal's workers and tooling, or need parent-owned child
runs. From your application's module directory, install the driver:

```sh
go get arkoren.dev/driver/temporal
```

### Connecting Workers

Create a [Temporal host](shared-worker-host.md#temporal) and register your runner
before starting that host. Construction does no network I/O:

```go
import "arkoren.dev/driver/temporal/temporalworkflow"

runner, err := temporalworkflow.NewRunner(host, registry)
```

After checking the constructor error, use its client driver:

```go
client := workflow.NewClient(runner.Driver())
```

For a separate producer process, set the host's `NoWorker(true)` and use
`NewDriver`, supplying the registry to derive native execution timeouts and
reject unknown workflow names:

```go
driver, err := temporalworkflow.NewDriver(host, func(driver *temporalworkflow.DriverBuilder) {
	driver.Registry(registry)
})
```

Without that optional registry, the runner still enforces `MaxDuration`, but the
client sets no server execution-timeout backstop. A dialing host needs `Start`
before calls; a host made with `temporal.NewFrom` exposes the borrowed client
immediately without replacing worker startup or taking ownership of that client.

### Matching Processes

Client and worker must agree on namespace, task queue, codec, and prefix. Both
`DriverBuilder.Prefix` and `RunnerBuilder.Prefix` default to `arkoren`; both also
accept a `Logger`, defaulting to `slog.Default()` for adapter records. The
runner's required positional registry also supplies its driver registry; there
is no `RunnerBuilder.Registry` setting.

Workflow logging uses Temporal's replay-aware logger and native level filtering.
Bound and grouped `slog.LogValuer` attributes resolve before forwarding, preserving
their redaction behavior. Host connection, TLS, and native SDK options belong to
[Worker Hosts](shared-worker-host.md#temporal).

The workflow type is `<prefix>-wf-<name>`, its step activity is
`<prefix>-step-<name>`, and the facade run ID is the native workflow ID. IDs are
unique across workflow names in one namespace while history remains. Result,
status, cancel, and signal calls describe and verify the workflow type first;
a wrong workflow name returns `ErrNotFound` without issuing a command or returning
output. Grant describe permission as well as command permissions. These checks
do not replace application authorization.

Use the [run-control examples](running-workflows.md) after host startup. With
matching routing, a signaled onboarding run returns its confirmation token.
Metadata is stored in memo; tracing uses a separate input envelope and needs no
host propagator hook. Deploy carrier-aware workers before traced producers:
older workers cannot decode traced envelopes. Pause producers if worker-first
rollout is not possible. Legacy untraced histories keep their argument shape.

### Retry Limits

Positive step timeouts become activity start-to-close timeouts. `Timeout(0)`
removes that attempt limit; the workflow's `MaxDuration` supplies the native
schedule-to-close bound. A permanent failure becomes non-retryable.
The worker logs an error for each failed step attempt, including attempts that
will be retried; an attempt log does not by itself mean the run failed.

Positive backoff caps apply from the first retry. An uncapped declaration asks
for Go's largest duration rather than the native implicit cap, but server limits
can reduce it. The tested two-attempt policy on native server 1.29.1 retained
`876000h`; this is evidence for that server and policy, not a universal maximum.
Inspect the persisted policy if this limit matters.

Multiple attempts with zero initial backoff return `workflow.ErrUnsupported`
before activity encoding or scheduling, even if the handler would succeed first
time. The driver does not substitute a positive interval or retry the whole
workflow. Single attempts and explicitly positive backoff remain supported.

> [!WARNING]
> Older zero-backoff histories recorded an activity command that replacement
> workers now reject; replay can fail with `TMPRL1100`. Keep affected runs on
> their original workers until completion, or drain them before replacement.
> Mixed workers on an unversioned task queue are not safe routing. There is no
> automatic migration or added replay marker; replay retained histories before
> rollout.

### Run Lifetimes

Sleep uses a native timer; waits select over signals, timers, and cancellation.
An internal timer enforces `MaxDuration`, with the native execution timeout as a
backstop. Step, child-result, existing-child join, and Spawn-acknowledgment waits
end on the parent's cancellation or duration limit without waiting for their
native futures to finish.

Only an atomically created child is owned by the parent; it receives cancellation
or parent-close termination. Joining an existing ID observes its original run
and ignores new input, without canceling that run when the parent ends. This
ownership decision survives replay without another history command or marker.
Spawn is detached but waits for start acknowledgment within the parent's budget.
An acknowledgment error does not prove the child never started: reconcile that
same ID before replacing work.

Terminal errors preserve native chain order: an outer timeout remains
`ErrTimeout`, and an outer application error is not replaced by a timeout cause.
Application error kinds remain matchable; unrelated lookup and transport errors
remain errors. Workflow panics fail the run rather than retrying a workflow task.
`Retention` and `Description` are ignored; retention belongs to the namespace.
Poll `c.Done()` non-blockingly, never by a blocking native channel read.

### Native Access

`host.Client()` exposes `go.temporal.io/sdk/client.Client`, and `c.Raw()` exposes
`go.temporal.io/sdk/workflow.Context`. Native operations bypass facade observations
and do not relax the [replay rules](overview.md#replay-rules).

### Local Engine

For a disposable development engine, save this in your application's `compose.yaml`:

```yaml
services:
  temporal:
    image: temporalio/temporal:1.5.1
    command: ["server", "start-dev", "--ip", "0.0.0.0", "--headless"]
    ports:
      - "127.0.0.1:17233:7233"
```

Run `docker compose up -d`, then run
`docker compose exec temporal temporal operator cluster health --address 127.0.0.1:7233`
until it succeeds. Configure the host's `HostPort("127.0.0.1:17233")` and
`TaskQueue("onboarding")`, then register the runner and
[lifecycle hooks](shared-worker-host.md#temporal). Start a run and read its result
to verify delivery, not merely server health. `docker compose down --volumes`
removes the disposable engine and history. The CLI image tag does not establish
the native server's version or duration limits.

See the [`temporalworkflow` API reference](https://pkg.go.dev/arkoren.dev/driver/temporal/temporalworkflow).

## Restate

Restate calls an HTTP endpoint in your application. Workflow functions become
workflow services, and steps run inline as durable actions. Choose it for durable
execution through that serving model when you do not need parent-owned `Child`
runs. Install from your application's module directory:

```sh
go get arkoren.dev/driver/restate
```

### Connecting Workers

Configure a reachable endpoint and request-identity keys using the
[Restate host](shared-worker-host.md#restate). Before `host.Start`, bind the
workflow services and construct a separate client driver:

```go
import "arkoren.dev/driver/restate/restateworkflow"

runner, err := restateworkflow.NewRunner(host, registry)
```

After handling that error, create the facade:

```go
driver, err := restateworkflow.NewDriver(host)
client := workflow.NewClient(driver)
```

Binding does not listen, serve, or register a deployment. Register host `Start`
and `Stop` as startup/cleanup hooks and `Serve` as a runner, as described in
[Worker Hosts](shared-worker-host.md#restate). A producer-only process uses
`ClientOnly(true)` and `NewDriver`, which binds no services.

### Matching Processes

Use the same codec and prefix in every client and worker. Both role builders
expose `Prefix` (default `arkoren`) and `Logger` (nil selects `slog.Default()`).
Service naming removes prefix separators and uppercases the following letter;
the default workflow `onboard` becomes `ArkorenWf_onboard`. `runner.Services()`
returns the bound service names in workflow-name order.

After deployment registration, use [run control](running-workflows.md) to start
and signal the confirmation-aware onboarding workflow. Reading the token from
`Result` verifies endpoint delivery. Duplicate starts return `ErrAlreadyStarted`;
an undeployed workflow returns `ErrNotFound`.

Signals resolve durable promises, keeping the first value. For accepted runs
still queued or delayed, the adapter forces delivery only after proving pending
state. Lookup and output-probe failures remain errors rather than being treated
as missing runs. Cancel needs access to admin SQL introspection as well as ingress.

Trace headers use `arkoren-trace-*`, separate from `arkoren-meta-*` metadata and
native attempt headers. Upgrade old workers before expecting trace continuity.

### Retry Limits

The adapter explicitly supplies initial delay, multiplier, attempts, and maximum
interval. Zero initial delay stays zero; zero maximum requests Go's largest
duration instead of the SDK default cap. Native delays have millisecond precision
and the multiplier is `float32`.

Step execution is cooperative. Positive `Timeout` supplies a context deadline;
after the body returns, late success becomes `workflow.ErrTimeout`. No adapter
goroutine abandons the body to escape that deadline, so noncooperative code can
prevent return. Zero supplies no attempt deadline. The SDK logs an error for
each failed attempt it retries.

### Run Lifetimes

`MaxDuration` starts a durable timer when execution begins, not at submission.
Blocking runtime operations observe it; it does not forcibly interrupt running
Go code. Native handler inactivity and abort timeouts are fixed at 24 hours.
Cancel closes a local attempt's Done and requests native cancellation; a running
step or another process observes it at a durable-operation boundary.

`Child` returns `ErrUnsupported` before send, attach, or cancellation. A durable
receipt identifies an invocation but cannot atomically distinguish creating it
from joining another owner's run across replay. A lookup then send is not safe
ownership. Spawn remains supported for explicitly detached work only.

> [!WARNING]
> Drain runs that called the earlier `Child` implementation on their original
> deployment before replacing it. Rejection changes their journal sequence;
> forced registration does not migrate that history. Completed retained outputs
> remain readable. Spawn is not an equivalent cancellation fallback.

### Cancellation Retention

`Retention` keeps finished outputs and promises for the configured window; zero
uses Restate's one-day default. Retained cancellation of delayed or queued runs
additionally requires positive effective retention and persisted activation of
`preflight_invocation_termination_retention` on the relevant partitions.

With that profile, pre-execution cancellation keeps `ErrCanceled`, `Canceled`
status, and duplicate protection for the retention window. Without it, the legacy
profile discards the invocation: Result and Status return `ErrNotFound`, another
Cancel can return `ErrNotFound`, and Start can immediately reuse the key. That
native behavior is not permission to repeat business work without reconciliation.

The tested Restate 1.7.12 includes the opt-in feature introduced in 1.7.8. Operators
request it with
`RESTATE_EXPERIMENTAL_ENABLE_PREFLIGHT_INVOCATION_TERMINATION_RETENTION=true`
and must ensure persisted activation. Activation establishes a minimum server
version: plan the compatibility change before applying it to an existing cluster,
and do not downgrade below 1.7.8 after the feature barrier applies. It cannot
recover already discarded records. See the
[native release notes](https://github.com/restatedev/restate/blob/v1.7.12/release-notes/v1.7.8.md#opt-in-retention-for-terminated-pre-flight-invocations).

The driver neither enables nor verifies this profile. Health proves HTTP
readiness; `/version` reports requested flags, not partition-barrier activation.
Public admin SQL on 1.7.12 cannot expose the internal feature state needed to
prove activation. None is a retained-cancellation readiness gate, and there is
no local tombstone or silent fallback. VQueues are not required for the feature.

### Native Access

`c.Raw()` exposes `restate.WorkflowContext` from `github.com/restatedev/sdk-go`;
keep its calls [replay-safe](overview.md#replay-rules). `host.Query` accesses admin SQL:
restrict credentials and never expose arbitrary queries to untrusted callers.
Each rollout is a new deployment, so preserve compatibility with runs in flight.

### Local Engine

For a fresh disposable server, save this as your application's `compose.yaml`.
The flag requests preflight retention; it is not proof of persisted activation
or a safe upgrade procedure for an existing cluster.

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
until it succeeds. This checks HTTP health only. For an application on your
machine, configure the host to reach ingress/admin and let the server reach back:

> [!WARNING]
> The following endpoint skips request-identity verification. Anyone who can
> reach port 9080 can invoke handlers. Use it only on an isolated development
> machine; production requires identity keys and network access controls.

```go
import "arkoren.dev/driver/restate"

host := restate.New(func(host *restate.Builder) {
	host.Addr(":9080")
	host.IngressURL("http://localhost:18080")
	host.AdminURL("http://localhost:19070")
	host.AdvertisedURL("http://host.docker.internal:9080")
	host.Register(true)
	host.AllowInsecure(true)
})
```

Bind the runner, wire the host hooks, and read an onboarding result to confirm
registration and execution. `docker compose down --volumes` deletes the disposable
server and history.

See the [`restateworkflow` API reference](https://pkg.go.dev/arkoren.dev/driver/restate/restateworkflow).
