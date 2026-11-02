---
title: Drivers
description: Choose process-local timers or an engine-backed scheduler and configure its delivery, ownership, and shutdown rules.
order: 20
summary:
  - Compare memory, Temporal, and Restate timing, persistence, and missed-tick behavior.
  - Construct schedulers with appropriate ownership prefixes and execution policies.
  - Understand worker lifecycle, native access, and local delivery checks.
---

# Drivers

Choose memory for independent process-local timers and disposable deferred
work. Choose Temporal or Restate when calendar delivery must be coordinated
across workers or deferred jobs must survive worker restarts. Prefer an engine
your team already operates, then check its calendar and failure behavior below.

## Compatibility Matrix

This compares behavior exposed through Arkoren, not the engines' full feature
sets. Restart survival depends on retained engine state. Shared calendar timing
does not guarantee exclusive application effects or exactly-once execution.

| Driver | Shared Calendar Timing | Deferred Tasks | Worker Restart Survival | Non-UTC Entries | Missed Ticks | Workflow Host |
| --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No | Yes | No | Yes | No | No |
| [Temporal](#temporal) | Yes | Yes | Yes | Partial — Explicit named zones work; Local is rejected. | Partial — Recovery is limited by CatchupWindow, one minute by default. | Yes |
| [Restate](#restate) | Yes | Yes | Yes | Partial — Explicit named zones work; Local is rejected. | Partial — The pending tick runs once, not once per missed calendar match. | Yes |

Memory runs every entry in every replica. Memory and Restate use the core
calendar; Temporal evaluates its own calendar and runs an autumn repeated time
twice. See [Time Zones](time-zones-and-dst.md) before using local wall-clock times.
Memory does not retry failed handlers; Temporal configures one attempt; Restate
treats ordinary handler errors as terminal but retries missing dispatch and
errors matching `schedule.ErrUnknownName`. Design effects to tolerate repetition.

Engine drivers attach to the vendor's [Worker Hosts](../workflows/shared-worker-host.md).
Construct the schedule driver before starting the host, register matching
entries and handlers on every worker, and run the scheduler using
[the ordinary runner setup](overview.md#running-schedulers). Host connection,
credentials, endpoint security, and cleanup belong in that shared guide.

Run installation commands in your application's module directory. Constructor
snippets use `schedule` from `arkoren.dev/core/service/schedule`; timing examples
also import `time`. Handle constructor errors before using the returned driver.

`scheduler.Driver()` exposes the raw driver, and `DriverAs[T]()` finds a concrete
type through wrappers exposing `Unwrap() schedule.Driver`. Raw operations bypass
the facade's registration and observations; use ordinary scheduler methods for
application work.

## Memory

The bundled `memoryschedule` driver uses Go timers and needs no separate
installation. Use it for tests, development, or a single replica when losing
deferred jobs is acceptable.

### Creating Timers

Construction starts nothing and cannot fail:

```go
import "arkoren.dev/core/service/schedule/memoryschedule"

driver := memoryschedule.New()
scheduler := schedule.New(driver)
```

`scheduler.Run` starts timers; the driver has no separate `Start` or `Stop`.
Add a [calendar entry](calendar-schedules.md#defining-entries), or register a
[deferred handler](deferred-tasks.md#registering-handlers) and call `After`.
Deferrals accepted before `Run` remain pending; overdue jobs run at its next
opportunity. There is no missed-calendar-tick recovery.

The only optional driver setting is `Builder.Logger`, which defaults to
`slog.Default()` and receives driver failure and overlap-skip records.

### Shutdown Boundaries

Runner cancellation stops timer admission and discards unstarted work belonging
to that run. Process exit loses all jobs. Already admitted callbacks retain
context values but not the runner's cancellation or deadline; `Run` waits for
them without a drain timeout.

Set positive entry timeouts and bound task I/O. A callback waiting only for
its detached context can prevent the driver from returning forever. Application
shutdown budgets limit runner waiting, not callback lifetime, so cleanup can
start while callbacks still hold dependencies. No timeout kills Go code.

Deferrals accepted after admission closes, including during draining, remain
pending for a later raw-driver run. The raw driver rejects concurrent `Run`
calls through the drain with `memoryschedule.ErrAlreadyRunning`, and can run
again after it returns. The scheduler facade itself remains single-use.

The driver copies submitted payload bytes. An already-canceled submission
returns the context error and no ID; an already-canceled cancellation leaves
pending work unchanged. Canceling a submission's context after acceptance does
not withdraw its job. `Cancel` removes pending work but cannot stop an admitted
dispatch.

For timer tests, use [Controlling Time](../testing/time.md). `RunNow` checks a
handler without testing timer delivery.

For every function and option, see [the `memoryschedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule/memoryschedule).

## Temporal

Temporal stores recurring entries as Schedules and deferred tasks as delayed
workflows. It retains them across worker restarts while its engine state is
retained. Install the module:

```sh
go get arkoren.dev/driver/temporal
```

### Connecting Workers

With a [configured Temporal host](../workflows/shared-worker-host.md#temporal),
construct the driver before `host.Start`. It registers a workflow and activity
without making network calls.

> [!WARNING]
> `Run` deletes Temporal Schedules beginning with `<prefix>-` that are absent
> from your declarations. Give each application a unique prefix on a shared
> namespace. Workers sharing that prefix must declare the same schedule set;
> conflicting deployments can overwrite or remove each other's schedules.

```go
import "arkoren.dev/driver/temporal/temporalschedule"

driver, err := temporalschedule.New(host, func(clock *temporalschedule.Builder) {
	clock.Prefix("accounts")
})
scheduler := schedule.New(driver)
```

The prefix defaults to `arkoren` and names workflows, activities, and schedules.
`Run` creates or updates `<prefix>-<entry-name>` schedules, prunes undeclared
ones, then waits. Deferred workflows named `<prefix>-deferred-*` are not pruned.
Register [calendar entries](calendar-schedules.md) and
[task handlers](deferred-tasks.md) before running this scheduler.

A dialing host must connect first: live-context `Run`, `Defer`, and `Cancel`
otherwise return `temporalschedule.ErrNotStarted`. An already-canceled driver
`Run` returns nil without connecting. A host wrapping an existing native client
can submit deferrals before local `Run`. A client-only host needs matching
workers elsewhere.

Existing activities can arrive between host startup and scheduler startup.
They wait for dispatch readiness within their activity timeout, so start
`Run` promptly: readiness waiting consumes that execution budget.

### Timing Policy

If a brief outage must recover more than the default minute of missed ticks,
construct the driver with an explicit catch-up window. This is an alternative
construction using the same host, not a second registration on it:

```go
driver, err := temporalschedule.New(host, func(clock *temporalschedule.Builder) {
	clock.Prefix("accounts")
	clock.CatchupWindow(10 * time.Minute)
	clock.MaxRunTime(20 * time.Minute)
})
```

`MaxRunTime` defaults to one hour and sets the activity bound when an entry has
no positive timeout; deferred tasks use that fallback too. A positive entry
timeout overrides it. Nonpositive `MaxRunTime` and `CatchupWindow` restore their
defaults. Driver `Logger` defaults to `slog.Default()`. Native timing bounds do
not forcibly terminate application callbacks.

Each run has one activity attempt. `WithoutOverlapping` maps to Temporal's
skip policy; otherwise all overlaps are allowed. Temporal has approximately
second-level schedule resolution, and actual firing can lag. Its own calendar
skips spring-gap times and fires autumn-overlap times twice.

### Shutdown Boundaries

Every driver `Run` exit removes its dispatcher and cancels active dispatch
contexts, including on reconciliation failure. It does not join callbacks or
stop code that ignores cancellation. Later activities wait for a new raw-driver
run instead of reusing the stopped dispatcher; a facade cannot be restarted.

The host's worker-stop grace is native policy, not a hard callback deadline.
Its early stop notification does not immediately cancel active callbacks.
See [host shutdown](../workflows/shared-worker-host.md#shutdown-boundaries) for
grace, native option precedence, and client ownership.

### Native Access

Use the shared host's `Client()` for SDK operations outside the facade:

```go
native := host.Client()
```

A dialing host exposes it only after connection; `temporal.NewFrom` borrows a
supplied client and exposes it immediately. Configure native TLS, authentication,
and logger policy through [the host](../workflows/shared-worker-host.md#connection-policy).
Keep independent native schedules outside the driver's prefix: reconciliation
can overwrite native edits to managed schedules.

### Local Engine

Use the [Temporal local engine](../workflows/drivers.md#local-engine) in your
application. Construct this driver before starting that host and register
`scheduler.Run`. Check a handler's effect after a calendar tick to verify
engine delivery; `RunNow` verifies only the handler. The linked profile includes
health verification and disposable-engine cleanup.

For every function and option, see [the `temporalschedule` API reference](https://pkg.go.dev/arkoren.dev/driver/temporal/temporalschedule).

## Restate

Restate stores each recurring entry in a virtual object whose tick schedules
the next tick before dispatching the current work. Deferred tasks use delayed
sends. Choose it when you operate a Restate server with retained state:

```sh
go get arkoren.dev/driver/restate
```

### Connecting Workers

Configure the [Restate host](../workflows/shared-worker-host.md#restate), including
a reachable endpoint, request-identity verification, and deployment registration.
Construct this driver before `host.Start` because it binds native services.

> [!WARNING]
> Use a distinct ownership prefix per application. Startup reconciliation removes
> undeclared entries from that prefix's index. Service names drop separators and
> capitalize the following letter, so prefixes must also have distinct normalized
> names; different strings alone do not ensure isolation.

```go
import "arkoren.dev/driver/restate/restateschedule"

driver, err := restateschedule.New(host, func(clock *restateschedule.Builder) {
	clock.Prefix("accounts")
})
scheduler := schedule.New(driver)
```

The default prefix `arkoren` creates `ArkorenSchedule`, `ArkorenScheduleIndex`,
and `ArkorenRunner`. The index key preserves the exact prefix; ingress requests
escape it as one path segment. Run `scheduler.Run` alongside `host.Serve` using
the shared host's lifecycle setup. `Run` retries initial ingress publication
every second until accepted or canceled, including while the deployment is not
yet registered. It does not fail immediately on an unreachable ingress.

Register [calendar entries](calendar-schedules.md) and
[task handlers](deferred-tasks.md) before `Run`. Deferrals can be accepted before
local `Run` if ingress knows the deployment; delivery retries when dispatch is
not ready. Unlike initial publication, `Defer` does not retry submission errors.
The returned job ID is a Restate invocation ID accepted by `Cancel`.

### Timing Policy

For a different native handler budget, select `MaxRunTime` during construction:

```go
driver, err := restateschedule.New(host, func(clock *restateschedule.Builder) {
	clock.Prefix("accounts")
	clock.MaxRunTime(20 * time.Minute)
})
```

This alternative construction sets abort and inactivity timeouts on native Tick
and Run handlers. Zero restores the default hour; negative values leave server
defaults, not an unlimited-execution guarantee. Driver `Logger` defaults to
`slog.Default()`. Entry timeouts remain cooperative application-context bounds.

`WithoutOverlapping` runs inline in the entry's serialized tick invocation;
ordinary entries dispatch through the runner service. A delayed pending tick
runs once and computes its next match after the current time, rather than
replaying every missed calendar match. Ordinary application failures are terminal;
missing dispatch and errors matching `schedule.ErrUnknownName` are retryable.
Runs can repeat, so keep application effects idempotent.

### Deployment Safety

Changing an expression, zone, or overlap flag starts a new schedule generation.
Removing an entry clears its active spec but retains its generation. Adding the
same name later advances the generation so old queued ticks cannot revive it.
Preserve that retained generation if you manage native state manually; it does
not prevent duplicate application effects.

> [!WARNING]
> Before upgrading existing schedule state, let in-flight `Remove` invocations
> finish on the old deployment. Removal records individual state clears rather
> than `ClearAll`; replaying an incompatible recorded history can fail. If an
> older deployment already removed a name, drain its pending ticks before
> reintroducing it because its deleted generation history cannot be recovered.

Task JSON values, including explicit `null`, are preserved. Upgrade serving
workers before relying on null-payload preservation; older workers discard
those bytes. Recurring messages still dispatch with nil payload bytes,
including legacy recurring messages carrying `Payload:null`.

Each rollout creates a deployment. Keep handlers compatible with in-flight
work and use the host's [deployment and shutdown policy](../workflows/shared-worker-host.md#deployment-policy).
`Cancel` requests native invocation cancellation; it cannot forcibly stop
application code already executing. Keep captured dependencies alive until
callbacks finish.

### Native Access

The host's `Query` method exposes the admin SQL API for inspection. Keep admin
credentials restricted and do not expose arbitrary queries to untrusted callers.
For an existing host and operation context:

```go
rows, err := host.Query(ctx, "SELECT * FROM sys_invocation LIMIT 10")
```

This reads native invocation records, not the facade's registered definitions.
The workflow driver can bind to the same host; its separate
[cancellation retention](../workflows/drivers.md#cancellation-retention) policy
is not a prerequisite for recurring schedules.

### Local Engine

Use the [Restate local engine](../workflows/drivers.md#local-engine-1), construct
this driver before host startup, and run both `host.Serve` and `scheduler.Run`.
Check the handler's effect after a tick to test durable delivery. The linked
isolated development profile disables request-identity verification; do not use
that setting on an exposed endpoint. Follow its cleanup instructions to remove
the disposable server and schedule state.

For every function and option, see [the `restateschedule` API reference](https://pkg.go.dev/arkoren.dev/driver/restate/restateschedule).

## Hatchet

Hatchet integration is pending and unpublished. It is not an installable
scheduler option and is excluded from the available-driver matrix. Use memory,
Temporal, or Restate instead.

For the portable contract, see [the `schedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule).
