---
title: Shared Worker Host
description: The host the Temporal, Hatchet, and Restate drivers share, how to build and start it, and how to run a process that only starts work.
order: 40
---

# Shared Worker Host

## Introduction

If one service uses an engine for both workflows and scheduled tasks, it should
not need a separate connection and worker for each driver. A **host** manages those
shared resources, including their ownership rules. The Temporal, Hatchet, and
Restate modules each provide a host that their workflow and scheduler drivers
use together.

The host is why the engine drivers read differently from the
[memory driver](drivers.md#memory). You build the host first and pass it to the
drivers, and the drivers register what they need with it. Then the application
starts the host and stops it at shutdown. Temporal and Hatchet start a worker;
Restate instead needs a serving endpoint, as described below.
This page describes that arrangement once, for [workflow drivers](drivers.md)
and [scheduler drivers](../scheduled-tasks/drivers.md) alike. The memory drivers
have no host.

## The order that works

Build the host and register work before starting it. Construction performs no
I/O; drivers **register** workflows and activities with Temporal and Hatchet,
or **bind** services to Restate. Once the host starts, registration is closed
and later registration fails with that vendor's `ErrStarted`.

For Temporal and Hatchet, `Start` connects, applies registrations, and starts
the worker. Restate splits startup: `Start` opens the listener, then `Serve`
serves requests and optionally registers the deployment. Construct anything
that registers or binds work (`New` for a scheduler, `NewRunner` for workflows)
before `host.Start`. A client-only workflow `NewDriver` registers no work.

These snippets use your existing `*application.Application` named `app` and
the `Onboard` workflow and `SendWelcome` step from the
[workflow overview](overview.md#a-first-workflow). Check constructor and
registry errors before registering lifecycle hooks.

```go
import (
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/temporal"
	"arkoren.dev/driver/temporal/temporalschedule"
	"arkoren.dev/driver/temporal/temporalworkflow"
)

host := temporal.New(func(host *temporal.Builder) {
	host.HostPort("temporal:7233")
	host.TaskQueue("app")
})

registry, err := workflow.Handlers(Onboard, SendWelcome)

runner, err := temporalworkflow.NewRunner(host, registry)

driver, err := temporalschedule.New(host)

compose := func(composer *application.Composer) {
	composer.Cleanup(host.Stop)
	composer.Startup(host.Start)
	composer.Check("temporal", host.Ping)
}
```

Three composer registrations complete the wiring. `composer.Startup(host.Start)` connects and
starts the worker; a wrong address or a stopped server fails startup instead of
the first request, and start hooks finish before any runner begins, so the
workflow client and the scheduler's `Run` find a running host. `composer.Cleanup(host.Stop)`
stops the worker and releases its owned client; a borrowed Temporal client stays
open. `Check` registers `host.Ping` as a readiness check for native API
reachability, not proof of worker liveness or scheduler dispatch readiness. See
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md) for the hooks
and [Health and Readiness](../application/health-and-readiness.md) for checks.

A host is also a place to register once and share. When a service uses Temporal
for both workflows and scheduled tasks, pass the same host to both drivers, as
above. They share one connection and one task queue, and one worker serves all of
it. Keep the vendors apart, though: a Temporal host and a Hatchet host are
different objects from different modules.

## Temporal

The Temporal host belongs to `arkoren.dev/driver/temporal`, in package
`temporal`. `temporal.New` returns a host that dials the server in `Start` and
closes the client in `Stop`. Builder settings include the server address
(`HostPort`, default `localhost:7233`), the namespace (`Namespace`,
default `default`), the task queue the worker polls (`TaskQueue`, default
`arkoren`), a logger, and the SDK's own client and worker options for TLS,
credentials, and interceptors. Nil host `Logger` adapts `slog.Default()` when
dialing; an explicit native `ClientOptions.Logger` wins. Host address and
namespace, including defaults, override the matching native client fields.

`Builder.WorkerStopTimeout` supplies native graceful-stop policy: ten seconds by
default, zero restores that default, and a negative host value disables grace.
A nonzero native `WorkerOptions.WorkerStopTimeout` takes precedence. The SDK can
apply grace separately to internal workers, then cancel activity contexts; this
is not a total shutdown deadline or forced termination of application callbacks.

Serialize `Start` and `Stop`. An already-canceled startup context acquires
nothing. Native worker startup has no context parameter: cancellation during
that call is observed only after it returns, then acquired worker resources and
any owned client are released. Registration callbacks and SDK hooks must return;
the host cannot forcibly interrupt them. Registration panics propagate after
owned-client cleanup.

`Stop` initiates worker cleanup even with an expired context and waits only until
its context ends. Cleanup continues afterward; the host closes an owned client
only after worker cleanup returns. A context error therefore does not prove
cleanup finished. Repeated calls wait on the same cleanup with their own contexts.

If your program already has a Temporal client, wrap it with `temporal.NewFrom`:

```go
host := temporal.NewFrom(client, func(host *temporal.Builder) {
	host.TaskQueue("app")
})
```

The host then uses your client, and `Stop` leaves it open, since you own it.
Only `TaskQueue`, `WorkerOptions`, `WorkerStopTimeout`, and `NoWorker` apply.
Host `Logger` and connection settings do not. The worker inherits your client's
logger; configure that logger before creating the native client, not on the
wrapper. `host.Client()` returns the SDK client, nil before `Start` unless you
used `NewFrom`.

SDK option records are copied by value, not recursively. Host address and
namespace override native client fields only when the host dials. `NewFrom`
retains the caller's client configuration and ownership.

## Hatchet

The Hatchet host belongs to `arkoren.dev/driver/hatchet`, in package `hatchet`.
It needs an API token, either with `Builder.Token` or in the `HATCHET_CLIENT_TOKEN`
environment variable, and the gRPC address of the engine with `Builder.HostPort`,
as `host:port`. `Builder.TLSStrategy("none")` connects without TLS, which a local
engine needs; an empty value or `"tls"` uses the SDK defaults, which read
`HATCHET_CLIENT_TLS_*` variables. `Builder.Namespace` prefixes workflow and event
names; set it on the host and not in the SDK's client options, because the
drivers must know it to match their waits. `Builder.WorkerName` (default `arkoren`),
`Builder.Slots` (default 100), and `Builder.DurableSlots` size the worker. Native
ClientOptions follow derived settings, but the host Logger replaces the SDK logger.

`Start` creates the client, runs the registrations, and starts the worker. When
durable tasks are registered, it checks the engine version. A recognized version
older than 0.80.0 fails with `hatchet.ErrEngineUnsupported`. If the version cannot
be parsed as semantic versioning, the check logs a warning and lets startup
continue; this does not confirm engine compatibility. `host.Client()` returns
the SDK client, nil until `Start` succeeds.

The outer ClientOptions slice copies on input and publication; native functions
and their captures remain shared.

The v0.80.0 durable-eviction check is not a retry-timing check. Registered retry
delay overrides require v0.111.0+ and adequate native caps; see
[workflow timing limits](drivers.md#hatchet). The local engine profile pins
v0.111.0, which supports those overrides; it does not fix the SDK lifecycle limits
below. A passing full native race suite does not establish race-free shutdown:
repeated concurrent conformance still reports the SDK race.

Some native startup RPCs use background contexts, and `RegisterTimeout` budgets
registration-log observation, not the whole startup. Cancellation during that
observation waits for the registration handoff or its budget before synchronous
cleanup. The SDK can panic in an internal goroutine outside the host's recovery
boundary. These startup and asynchronous-panic limits remain open; use a process
supervisor rather than assuming every failure returns as a startup error.

Do not infer caller-bounded shutdown either. The host cancels and joins admitted
application callbacks, which must cooperate, but the SDK does not expose all
intake/reporting goroutines for joining. Its ordinary `Client.Close` does not
close gRPC connections; embedded-mode release is different. The pinned SDK also
has a durable-eviction shutdown race between reading and clearing wait state;
the adapter cannot lock that private state. Keep calling `Stop`, but do not treat
native close or functional recovery as proof of complete cleanup or race-free
shutdown. These SDK limits require a separate dependency correction, not a claim
of caller-bounded lifecycle behavior.

## Restate

The Restate host belongs to `arkoren.dev/driver/restate`, in package `restate`,
and differs from the other two in one way: Restate calls *your* process. The
host serves an HTTP endpoint that the Restate server invokes, so it has a third
lifecycle method, `Serve`, and drivers **bind** their services to it instead of
registering them.

```go
import (
	"os"

	"arkoren.dev/driver/restate"
)

host := restate.New(func(host *restate.Builder) {
	host.Addr(":9080")
	host.IngressURL("http://restate:8080")
	host.AdminURL("http://restate:9070")
	host.AdvertisedURL("http://app:9080")
	host.Register(true)
	host.IdentityKeys(os.Getenv("RESTATE_IDENTITY_KEY"))
})

compose := func(composer *application.Composer) {
	composer.Cleanup(host.Stop)
	composer.Startup(host.Start)
	composer.Run(host.Serve)
	composer.Check("restate", host.Ping)
}
```

`Start` builds the endpoint and opens the listener without serving. `Serve`
serves requests, registers the deployment with the admin API when
`Builder.Register(true)` is set, and blocks until the application stops, so it is a
runner. Register cleanup immediately after construction: `Stop` also closes a
listener opened by `Start` when `Serve` never runs, including startup rollback.
Canceling `Serve` or a deployment-registration failure does not replace `Stop`.

`Stop` waits for active HTTP requests until its context expires, then closes
remaining connections and returns the shutdown error. Connection closure cancels
request contexts; it cannot stop application code that ignores cancellation.
The shared `HTTPClient` remains caller-owned and is not closed by the host.
`Ping` checks only the admin API's health, not ingress reachability, endpoint
registration, successful workflow delivery, or persisted activation of the
[workflow preflight-retention profile](drivers.md#retaining-canceled-runs-before-execution).

The ingress and admin URLs are where your process reaches the server. The
advertised URL is where the *server* reaches your process, and it must be
reachable from there; the default is `http://localhost` plus the listen address,
which only works when both run on one machine. The endpoint speaks HTTP/2
without TLS and HTTP/1.1.

> [!WARNING]
> The endpoint verifies that each request was signed by the Restate server.
> Set `Builder.IdentityKeys` to the server's request-identity public keys
> (`publickeyv1_...`). A host that would listen without keys fails `Start` with
> `restate.ErrNoIdentityKeys`. `Builder.AllowInsecure(true)` skips the check, and
> belongs to local development only: anyone who can reach the listener could
> invoke your handlers.

Registration needs care in production. `Builder.Register(true)` is convenient, and
it registers a new deployment on every rollout. `Builder.RegisterForce` overwrites a
deployment at the same URL, and should only be used when no work of the old one
is in flight; `Builder.RegisterBreaking` accepts a deployment that removes services.
If your operators register deployments themselves, leave `Builder.Register` off. An
admin API protected by a key takes `Builder.AdminAuthKey`, and a protected ingress
`Builder.AuthKey`.

Before upgrading workflows that called `Child`, drain affected runs on their
original deployment. New workers reject `Child` before submission; the changed
journal sequence is not made replay-safe by forced registration. There is no
automatic history migration. See [Restate workflow rollout and ownership limits](drivers.md#restate).

When this host also serves schedules, preserve their retained generations and
finish in-flight `Remove` invocations on the old deployment before replacing an
incompatible journal. If an older deployment already removed an entry, drain its
pending ticks before reusing that name. See the
[Restate schedule rollout constraints](../scheduled-tasks/drivers.md#restate);
forced registration does not make incompatible histories replay-safe.

## Processes that only start work

A web process often needs to start workflows or defer tasks but should not run
any. Each host can be built as a **client only**: it connects, and starts no
worker and opens no listener.

```go
host := temporal.New(func(host *temporal.Builder) {
	host.TaskQueue("app")
	host.NoWorker(true)
})
driver, err := temporalworkflow.NewDriver(host, func(driver *temporalworkflow.DriverBuilder) {
	driver.Registry(registry)
})
```

Temporal and Hatchet `Builder.NoWorker(true)` skip the worker, and Restate
`Builder.ClientOnly(true)` skips the listener. NoWorker defaults false; setting
false later restores it, but never forces a worker without registrations. A host with nothing
registered behaves the same way. In these processes use the workflow
`NewDriver`, which only needs a client, and not the `NewRunner`, which needs a
worker and returns an error on such a host. The scheduler driver registers work
on the host, so run the scheduler in a worker process. Keep the prefix and the
task queue identical in both kinds of process, so a run started by the web
process reaches the worker. Hatchet HasWorker reports permission, not evidence
of a running worker. Temporal WorkerDisabled reports the stored NoWorker flag.

Construction callbacks run directly once, synchronously in order on one fresh
draft. Nil entries panic; expanded nil/empty slices omit edits. Methods return
the same draft. Publication detaches owned roots and containers, including
Hatchet ClientOptions and Restate IdentityKeys; native SDK records, client
handles, hooks and their captures remain shared. Retaining a draft cannot
reconfigure the host. Workflow DriverBuilder and RunnerBuilder are separate;
NewRunner's required registry remains positional and takes precedence. A missing
host or registry is a construction error, not an optional builder setting. Only
Temporal DriverBuilder exposes Registry; its RunnerBuilder has no Registry
setter, and NewRunner's required registry supplies both runner and driver.

For every function and builder method, see the API references for the hosts' drivers:
[`temporalworkflow`](https://pkg.go.dev/arkoren.dev/driver/temporal/temporalworkflow),
[`hatchetworkflow`](https://pkg.go.dev/arkoren.dev/driver/hatchet/hatchetworkflow), and
[`restateworkflow`](https://pkg.go.dev/arkoren.dev/driver/restate/restateworkflow).
