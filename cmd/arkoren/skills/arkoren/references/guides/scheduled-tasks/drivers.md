---
title: Scheduler Drivers
nav: Drivers
description: Choose between the memory, Temporal, and Restate scheduler drivers, with pending Hatchet integration notes.
order: 40
---

# Scheduler Drivers

## Introduction

Choose memory when independent process-local timers and disposable deferred
tasks are enough. Every replica runs every entry, so memory suits development,
tests, and single-replica deployments, not shared scheduling.

Choose Temporal or Restate when replicas need coordinated ticks or
deferred tasks must survive worker restarts. Start with the engine your team
already operates, then check its time-zone, retry, and missed-tick behavior
below. Shared timing does not guarantee exactly-once handler effects.
See [Scheduled Tasks Overview](overview.md)
for using the scheduler, and [Choosing a Driver](../drivers/choosing-a-driver.md)
for every service's drivers side by side.

## Compatibility matrix

Hatchet is pending and unpublished. It is excluded from this matrix; references
to Hatchet below describe retained development context, not available support.

Compare scheduling exposed through Arkoren, not every engine feature. Shared
ticks coordinate the same entry across application replicas; they do not promise
exactly-once handler effects. Restart survival assumes the engine retains its
state, not that engine data loss is recoverable.

| Driver | Shared ticks across replicas | Deferred tasks survive worker restart | Non-UTC entries | Recover missed ticks | Shared workflow host |
| --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No | No | Yes | No | No |
| [Temporal](#temporal) | Yes | Yes | Partial — Named zones are supported; Local is rejected. | Partial — Limited by CatchupWindow, one minute by default. | Yes |
| [Restate](#restate) | Yes | Yes | Partial — Named zones are supported; Local is rejected. | Partial — Fires the pending tick once, not every missed tick. | Yes |

Memory runs every entry independently in every replica and drops the running
generation's unstarted work at shutdown. Temporal and Restate reject the
process-local `Local` zone; use an explicit named zone. Hatchet accepts only UTC
by default;
`AllowCronTZ(true)` emits an engine-dependent extension whose behavior you must
verify before relying on it.

Memory logs failed runs without retrying them. Temporal makes one attempt;
Restate treats ordinary handler failures as terminal, while missing dispatch or
unknown handler names can be retried. Hatchet marks failed or timed-out runs
failed without retrying them. Connection loss or worker reconnection can still
produce duplicate delivery, so make handlers idempotent. During an autumn DST
overlap, memory and Restate fire once and Temporal fires twice; the default
UTC-only Hatchet path has no DST overlap.

The two available engine drivers share a **host** per vendor: one object that manages
the native worker or service endpoint and that you start and stop with the
application.
[Shared Worker Host](../workflows/shared-worker-host.md) explains it once, and
the sections below show only what the scheduler adds. In short, build the host,
pass it to the driver's `New` before the host starts, and register `host.Start`
and `host.Stop` with the application.

Run installation commands from your application's module directory. The
examples import
`arkoren.dev/core/service/schedule`. Handle driver constructor errors before
building or running the scheduler. Add the [recurring entries](overview.md#defining-entries)
and register [task handlers](overview.md#deferring-tasks) before `Run`.

An engine driver treats the entry names you register as its own. Give each
application a distinct `Prefix` when several share one Temporal, Hatchet, or
Restate server. A driver deletes the schedules and crons that carry its prefix
and that your code no longer declares, so two applications with the default
prefix delete each other's entries.

## Memory

The memory driver keeps timers in the process using the `time` package. It is
part of `arkoren.dev/core`, so there is nothing to install. Choose it for local
development, tests, and a single replica whose deferred tasks can be lost on a
restart.

### Connecting

`New` never fails and starts nothing. The scheduler's `Run` starts the timers:

```go
import (
	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/memoryschedule"
)

scheduler := schedule.New(memoryschedule.New())
```

Register `scheduler.Run` as an [application runner](overview.md#creating-and-running-the-scheduler).
It starts the timers and blocks through shutdown and the drain described below;
the memory driver has no separate start or stop hooks.

### Configuration

The only construction setting is `memoryschedule.Builder.Logger`, for skipped ticks
and failed runs. It defaults to `slog.Default()`.

### Running recurring work and reminders

Add the [five-minute prune entry](overview.md#defining-entries) to this
`scheduler`; its function runs on matching calendar ticks. Register the
reminder handler from [Deferring tasks](overview.md#deferring-tasks), then call
`After` to schedule a one-off reminder. Its returned ID is accepted by `Cancel`.

### Behavior and limits

Every process that runs the scheduler runs every entry, so a service with three
replicas runs each entry three times. A task deferred before `Run` waits and
arms when `Run` starts; an overdue task runs as soon as possible. All deferred
work is process-local and is lost on process exit. Runner cancellation stops
timers and discards that generation's unstarted work. New deferrals accepted
after timer admission closes, including during draining, wait pending for a
later raw-driver `Run`; they are not armed by the draining generation.

One `Run` owns its generation through the entire drain. A concurrent raw-driver
`Run` returns `memoryschedule.ErrAlreadyRunning` even after the first context is
canceled, until its executions finish. After it returns, the raw driver can
run again; the `schedule.Scheduler` facade is single-use and returns
`schedule.ErrRunning` on any second `Run`. Timer callbacks capture their owning
Run's context and dispatcher, and stale recurring callbacks cannot rearm an old
entry in a later generation.

Admitted functions keep context values but not the runner's deadline or
cancellation. Shutdown waits for them without a driver drain timeout. Set
positive entry timeouts and bound I/O in task handlers: timeouts are cooperative,
and a function waiting only for its detached context can prevent `Run` from
returning forever. The application's shutdown budget bounds its runner wait,
not callback lifetime; cleanup can proceed while work remains active. No timeout
forcibly kills application code.

The raw memory driver's `Defer` copies payload bytes before acceptance, both for
pending jobs and armed timers. An already-canceled `Defer` returns its context
error and an empty ID without scheduling; an already-canceled `Cancel` returns
its context error without removing work. Later cancellation of an accepted
submission's context does not withdraw it: use `Cancel`, which cannot stop a
dispatch already admitted. These admission and copying rules are memory-specific,
not additional guarantees of every scheduler driver.

Because the driver uses plain timers, a test can run it in a `testing/synctest`
bubble and move time instantly; see [Controlling Time](../testing/time.md).

### API references

For every function and option, see [the `memoryschedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule/memoryschedule).

## Temporal

The Temporal driver turns each entry into a Temporal Schedule and each deferred
task into a delayed workflow. Temporal retains both in engine state and
coordinates schedule firings across replicas. Choose it when you already run
Temporal and want durable scheduling, or when you also use
[Temporal for workflows](../workflows/drivers.md#temporal).

### Installation

```sh
go get arkoren.dev/driver/temporal
```

### Connecting

With a [configured Temporal host](../workflows/shared-worker-host.md#temporal),
call `New` before `host.Start`: it registers a workflow and an activity on that
host. Add your entries to the scheduler before running it:

```go
import "arkoren.dev/driver/temporal/temporalschedule"

driver, err := temporalschedule.New(host, func(scheduler *temporalschedule.Builder) {
	scheduler.Prefix("app")
})

scheduler := schedule.New(driver)
```

Register `scheduler.Run` as an [application runner](overview.md#creating-and-running-the-scheduler).

`Run` creates or updates one Schedule per entry, deletes the stale ones, and
waits. It expects a connected host, which the linked `composer.Startup` wiring
provides before runners start. On a dialing host with no native client yet,
`Run` with a live context, `Defer`, and `Cancel` return
`temporalschedule.ErrNotStarted`. An already-canceled driver `Run` returns nil
without connecting. Deferring before local `Run` is supported once a native
client is available; a `temporal.NewFrom` host exposes its supplied client
immediately.

The worker can receive existing jobs between host startup and local `Run`.
Those activities wait for dispatch readiness within their activity timeout,
rather than failing the single-attempt delivery. Start `Run` promptly: that wait
uses the activity's execution budget. A client-only host with `NoWorker(true)`
can submit work but needs a matching worker elsewhere to execute it.

### Configuration

The host options (address, namespace, task queue, client and worker options)
are described in [Shared Worker Host](../workflows/shared-worker-host.md#temporal).
The driver's `Builder.Prefix` defaults to `arkoren` and prefixes workflow,
activity, and schedule IDs. `MaxRunTime` defaults to one hour and bounds a run
whose entry has no positive `Timeout`; zero or negative `MaxRunTime` restores
that default rather than removing the bound. `CatchupWindow` defaults to one
minute and sets how far back Temporal fires missed ticks; zero or negative
values restore that default too. Nil `Logger` selects `slog.Default()` for
driver records.

For a dialing host, a nil host `Logger` adapts `slog.Default()` before native
client construction; an explicit `ClientOptions.Logger` wins. Host address and
namespace settings, including their defaults, override the matching native
client-option fields. Configure SDK TLS and authentication on that native
option record; it is copied by value, not recursively cloned.

### Running recurring work and reminders

Add the [prune entry](overview.md#defining-entries), then run this `scheduler`.
Temporal owns its calendar and dispatches each run to a registered worker.
For one-off work, register the [reminder handler](overview.md#deferring-tasks)
before `Run`, then use `After`; its job ID identifies a delayed Temporal workflow.

### Behavior and limits

An entry's schedule has the ID `<prefix>-<name>`. Five-field specs are passed
through, and a six-field spec with seconds is adapted to Temporal's form. A
`WithoutOverlapping` entry maps to Temporal's skip policy. Each run makes a
single attempt, so a failed run is not retried. Temporal fires a tick that
falls in an autumn overlap twice and skips one in a spring gap (see
[Time Zones and DST](time-zones-and-dst.md)). Its practical resolution is about a
second, and firings can lag by a few seconds.

Every driver `Run` exit, including reconciliation failure, removes its local
dispatcher and cancels active dispatch contexts. It does not join application
callbacks or forcibly stop code that ignores cancellation. Later activities
wait for a new local driver `Run`, never reuse the stopped dispatcher; a
scheduler facade itself still runs only once.

The worker's early stop notification does not itself cancel an already-running
callback and bypass native grace. Active callbacks still observe cancellation
from their owning `Run` or native activity context. The host's
`WorkerStopTimeout` defaults to ten seconds; zero restores that default and a
negative host value disables grace. A nonzero native
`WorkerOptions.WorkerStopTimeout` takes precedence. This is native graceful-stop
policy, not a total shutdown deadline or forced callback termination.

Native worker startup has no context parameter. Cancellation during that call
is observed after it returns, then acquired resources are released; it cannot
forcibly interrupt SDK startup or registration callbacks. Serialize host
`Start` and `Stop`, and make native hooks return.

> [!WARNING]
> On startup `Run` deletes every Temporal Schedule whose ID begins with
> `<prefix>-` and that your code does not declare. Use a unique prefix per
> application on a shared namespace. Deferred tasks are workflows named
> `<prefix>-deferred-*` and are never pruned.

### Accessing Temporal operations

`host.Client()` exposes the native SDK client, and `temporal.NewFrom` borrows
an existing one; see [host ownership](../workflows/shared-worker-host.md#temporal).
Only `TaskQueue`, `WorkerOptions`, `WorkerStopTimeout`, and `NoWorker` apply to
`NewFrom`; host `Logger` and connection settings do not. Its worker inherits the
supplied client's logger, so configure that client before wrapping it. Host
`Stop` leaves borrowed clients open. For an owned client, it closes the client
only after worker cleanup finishes, possibly after `Stop`'s context has ended.
Native changes to schedules managed by this driver can be overwritten on the
next reconciliation. Keep independent schedules outside the driver's prefix.

### Running locally

Use the [Temporal Compose file and host example](../workflows/drivers.md#running-locally)
in your own application. It starts the development server on `127.0.0.1:17233`
and includes a cluster-health check. Construct this schedule driver before
starting that host, add your prune entry, and register `scheduler.Run`.
Check the prune function's effect after a matching tick, or use
`scheduler.RunNow(ctx, "prune-sessions")` to check its handler without waiting.
Run `docker compose down --volumes` to remove the disposable server and schedule history.

### API references

For every function and option, see [the `temporalschedule` API reference](https://pkg.go.dev/arkoren.dev/driver/temporal/temporalschedule).

## Hatchet

> [!NOTE]
> Pending and unpublished: `arkoren.dev/driver/hatchet` is not available to
> install. The following examples and operational notes are retained development
> context, not usable setup instructions or verified public API documentation.
> Choose memory, Temporal, or Restate above for an available driver.

The Hatchet driver turns each entry into a Hatchet cron trigger and each
deferred task into a scheduled run in the pending integration. These retained
notes target UTC entries; they are not a recommendation to install it.

### Installation

Installation is unavailable until the Hatchet module is published.

### Connecting

Use a [configured Hatchet host](../workflows/shared-worker-host.md#hatchet).
`New` registers three standalone tasks, so call it before `host.Start`, then
add your entries to the scheduler before running it:

```go
import "arkoren.dev/driver/hatchet/hatchetschedule"

driver, err := hatchetschedule.New(host, func(clock *hatchetschedule.Builder) {
	clock.Prefix("billing")
})

scheduler := schedule.New(driver)
```

Register `scheduler.Run` as an [application runner](overview.md#creating-and-running-the-scheduler),
after configuring the host's startup and cleanup hooks.

### Configuration

The host takes the API token (read from `HATCHET_CLIENT_TOKEN` when empty), the
gRPC address, TLS settings, a namespace, and worker slots; see
[Shared Worker Host](../workflows/shared-worker-host.md#hatchet). The driver has
four builder settings. Prefix (default `arkoren`) names the tasks and marks the
crons the driver owns. MaxRunTime (default one hour) is the execution
timeout of every run. AllowCronTZ permits non-UTC entries, described
next. Logger sets the logger.

### Running UTC work and reminders

Add the [prune entry](overview.md#defining-entries) using `schedule.UTC()`.
After this `scheduler` starts, Hatchet shows its cron trigger under the selected
prefix. Register the [reminder handler](overview.md#deferring-tasks) before
`Run`, then use `After`; the returned ID refers to a scheduled Hatchet run.

### Behavior and limits

Hatchet evaluates cron expressions in UTC. An entry in another zone makes `Run`
return `schedule.ErrUnsupported`, unless you set `Builder.AllowCronTZ(true)`, which
prefixes the expression with `CRON_TZ=<zone>`. That form is not part of
Hatchet's documented behavior, so check it against your engine first.

Each `Run` reconciles the crons of its prefix with the entries it was given: it
creates missing crons, replaces changed ones, and deletes any it does not
declare. During a rolling deploy the last process to start decides the final
set, so use a distinct prefix per application and avoid running two versions
of a schedule at once. A host configured with `hatchet.Builder.NoWorker(true)` runs no tasks, so it
cannot run entries.

The driver sets no retry count: Hatchet marks a failed or timed-out run failed
without retrying it. Delivery is still at least once; connection loss or worker
reconnection can cause the same run to arrive twice. Entries must therefore be
idempotent even though failures are not retried. `Cancel` is best effort: it
deletes the scheduled run but does not stop one that has already started.

Deferred task payloads travel in the versioned `payload_json_v1` field as an
opaque JSON string, then become JSON bytes again at scheduler dispatch. This
keeps large numeric IDs, including `int64` and `uint64` limits, out of Hatchet's
generic-map number conversions. Nested objects, arrays, null, and JSON strings
retain their values. Recurring entries still use a name-only input. Workers
also read pending jobs in the legacy raw `payload` format without treating
legacy JSON strings as encoded envelopes. Mixed formats or invalid new-format
payloads fail before the handler runs.

> [!WARNING]
> Upgrade all Hatchet workers before upgraded producers defer tasks. Old
> workers do not understand `payload_json_v1`. If workers also produce jobs,
> pause deferrals during the rollout and resume only after every old worker
> has stopped. Legacy pending jobs can still run, but numbers already rounded
> by Hatchet cannot be recovered. Check critical pending numeric arguments
> against authoritative records and cancel/recreate affected jobs from those
> records before execution; legacy decoding does not repair lost digits.

### Accessing Hatchet operations

`host.Client()` exposes the native Hatchet SDK client. Native changes to owned
cron triggers are replaced on the next reconciliation; keep independent work
outside this prefix. The workflow driver can share this host and worker.

### Running locally

Use the [Hatchet Compose file, token setup, and host example](../workflows/drivers.md#running-locally-1)
in your application. The instructions include Postgres, readiness verification,
dashboard credentials, and the plaintext local gRPC address. Construct this
driver before starting that host, add a UTC entry, and register `scheduler.Run`.
Confirm that the cron appears in the dashboard and check the handler's effect
after its tick. `RunNow` checks handler behavior without testing cron delivery.
Run `docker compose down --volumes` to remove the disposable engine, database, and crons.

The linked profile pins engine v0.111.0. It does not establish complete native
lifecycle validation: the pinned SDK's background startup, asynchronous panic,
and durable-shutdown race limits still apply to the shared host. See
[Hatchet host ownership and shutdown](../workflows/shared-worker-host.md#hatchet).

### API references

The [`hatchetschedule` API reference URL](https://pkg.go.dev/arkoren.dev/driver/hatchet/hatchetschedule)
is pending publication; it does not establish package availability.

## Restate

The Restate driver turns each entry into a virtual object that sends itself the
next tick before it runs the current one, so the chain survives a crash.
Deferred tasks are delayed sends. Choose it when you run a Restate server.

### Installation

```sh
go get arkoren.dev/driver/restate
```

### Connecting

First configure the host's reachable endpoint and request-identity keys, and
register its `Start`, `Serve`, and `Stop` lifecycle methods as shown in
[Shared Worker Host](../workflows/shared-worker-host.md#restate). The scheduler
driver binds services to that host, so construct it before `host.Start`. Add
your entries before running the scheduler:

> [!WARNING]
> When upgrading existing schedule state, let in-flight `Remove` invocations
> finish on the old deployment before replacing it. Removal now records
> individual state clears instead of `ClearAll`; forcing the new handler onto
> an incompatible recorded command history can break replay. If an older
> deployment already removed an entry, drain its pending ticks before
> reintroducing that name: the deleted generation history cannot be recovered.

```go
import "arkoren.dev/driver/restate/restateschedule"

driver, err := restateschedule.New(host, func(clock *restateschedule.Builder) {
	clock.Prefix("billing")
})

scheduler := schedule.New(driver)
```

Register `scheduler.Run` alongside `host.Serve` as
[application runners](overview.md#creating-and-running-the-scheduler), with the
host's startup and cleanup hooks installed before running the application.

`Run` publishes each entry through the ingress, and retries every second until
the server accepts, so it can start before the endpoint is registered.

### Configuration

The host options are described in [Shared Worker Host](../workflows/shared-worker-host.md#restate).
The driver's `Builder.Prefix` defaults to `arkoren`. Service names remove
separators and capitalize the next letter, so choose prefixes whose normalized
service names are distinct, not merely different strings. The index object's
key retains the exact prefix; ingress requests escape it as one path segment
without changing the persisted logical key.

`MaxRunTime` defaults to one hour and configures both abort and inactivity
timeouts on the native Tick and Run handlers. Zero restores one hour; a negative
value leaves the server's defaults, not an unlimited execution guarantee. Nil
`Logger` selects `slog.Default()`. The host owns its endpoint listener; its
shared HTTP client remains caller-owned and is not closed by host `Stop`.

### Running recurring work and reminders

Add the [prune entry](overview.md#defining-entries), then run this `scheduler`
alongside `host.Serve`. Once deployment registration succeeds, Restate stores
the entry's next tick. Register the [reminder handler](overview.md#deferring-tasks)
before `Run`, then use `After`; its job ID is a Restate invocation ID.

Deferred task payloads retain valid JSON values, including explicit `null`.
They can be accepted before local `Run` if the ingress already knows the
deployment; a delivery before dispatch readiness retries rather than consuming
the job. Unlike `Run`'s initial publication, `Defer` does not retry ingress
submission failures. Upgrade serving workers before relying on null-payload
preservation: older workers discard those bytes. Recurring entries still reach
dispatch with nil payload bytes, including legacy recurring messages carrying
`Payload:null`; declared entry names distinguish those from null-valued tasks.

### Behavior and limits

Runs are at least once. A changed spec (expression, zone, or overlap flag)
starts a new generation, and the ticks of the old one stop. The index removes
undeclared entries by clearing the active spec while retaining the generation
number. Reintroducing the same name advances that generation, so queued ticks
from before removal cannot revive the schedule. Keep that retained generation
when managing state manually. This protects against obsolete ticks, not duplicate
application effects or general exactly-once execution. Service names, state
keys, tick payloads, and idempotency-key formats remain unchanged by retention;
the rollout limits above still apply to state removed by older deployments.

A `WithoutOverlapping` entry runs inline in its tick; others run through the
runner. The `Local` zone is rejected with `schedule.ErrUnsupported`. The calendar follows the core rules,
so a spring-gap time is skipped and an autumn-overlap time runs once.

The Restate server must be able to reach the advertised URL of your endpoint,
and the deployment must be registered, by `restate.Builder.Register(true)` or by an
operator. Each rollout is a new deployment, so keep handlers compatible with
work that is already in flight. The linked local server profile uses Restate 1.7.12.

### Accessing Restate operations

The host's `Query` method accesses the Restate admin SQL API for inspection.
Restrict admin credentials and do not expose arbitrary queries to untrusted
callers. The workflow driver can bind services on the same host; keep its
deployment compatible with work already in flight.

### Running locally

Use the [Restate Compose file and host configuration](../workflows/drivers.md#running-locally-2)
in your application. It includes ingress and admin URLs, a health check, and
`host.docker.internal` so the server can call your Go process. Construct this
driver before `host.Start` and run both `host.Serve` and `scheduler.Run`.
The linked profile requests workflow preflight retention on fresh disposable
state; that optional workflow feature is not required for recurring schedules.
Its health check does not prove persisted feature activation; see
[workflow retention prerequisites](../workflows/drivers.md#retaining-canceled-runs-before-execution)
before depending on retained workflow cancellation.
The local host disables request-identity verification; use it only on an isolated
development machine. Check the prune handler's effect after a tick; `RunNow`
checks only the handler, not durable delivery. Run `docker compose down --volumes` to
remove the disposable server and its schedule state.

### API references

For every function and option, see [the `restateschedule` API reference](https://pkg.go.dev/arkoren.dev/driver/restate/restateschedule).
