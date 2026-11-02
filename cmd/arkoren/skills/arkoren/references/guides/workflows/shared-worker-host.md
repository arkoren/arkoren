---
title: Worker Hosts
description: Share native workflow and scheduler resources with explicit startup, readiness, and cleanup ownership.
order: 60
summary:
  - Register workflow and scheduler work on one native host before startup.
  - Wire Temporal or Restate lifecycle hooks with explicit readiness and cleanup boundaries.
  - Preserve client ownership, request verification, and journal compatibility across deployments.
---

# Worker Hosts

When one application uses an engine for workflows and scheduled tasks, a **host**
owns their shared connection and execution endpoint. Pass one Temporal or Restate
host to both drivers, then wire its lifecycle once. Memory drivers have no shared
host; Hatchet is pending and unpublished.

This page owns lifecycle for both [workflow drivers](drivers.md) and
[scheduler drivers](../scheduled-tasks/drivers.md). Driver-specific registration
belongs on those pages. Examples assume an existing application composer and a
validated workflow registry.

## Registration Order

Construct the host, register or bind all work, then start the host. Construction
does no network I/O. A workflow `NewRunner` and a scheduler constructor add work
to the host; a workflow `NewDriver` only controls runs. Late registration returns
the vendor's `ErrStarted`.

Inside a composer, register cleanup at the acquiring owner, before later
construction can fail. Use `composer.Must` for fallible construction there;
startup callbacks complete before application runners begin. See
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md).

Builder callbacks run synchronously once, in order, on one fresh draft. Nil
entries panic; expanded nil or empty slices supply no edits. Publication detaches
owned settings and containers from the retained draft, not native SDK internals,
hooks, clients, or captured services. Finish configuring shared native objects
before publishing them.

## Temporal

The `arkoren.dev/driver/temporal` host shares a client, task queue, and worker.
Within your composer, create and register it before either driver:

```go
import "arkoren.dev/driver/temporal"

host := temporal.New(func(host *temporal.Builder) {
	host.HostPort("temporal:7233")
	host.TaskQueue("app")
})
composer.Cleanup(host.Stop)
```

Register workflow and schedule work on that same host. Here `registry` already
contains your workflows and steps:

```go
import (
	"arkoren.dev/driver/temporal/temporalschedule"
	"arkoren.dev/driver/temporal/temporalworkflow"
)

runner := composer.Must(temporalworkflow.NewRunner(host, registry))
schedules := composer.Must(temporalschedule.New(host))
```

The runner's Driver supplies the workflow client; `schedules` supplies the
scheduler driver. Neither starts a worker. Add host lifecycle and readiness hooks:

```go
composer.Startup(host.Start)
composer.Check("temporal", host.Ping)
```

Start dials, applies registrations, and starts the worker. Ping checks native API
reachability, not worker liveness or scheduler dispatch readiness. A wrong address
fails startup rather than waiting for the first client request. The scheduler's
own run lifecycle is separate and uses the already-started host.

### Connection Policy

Defaults are `HostPort("localhost:7233")`, namespace `default`, and task queue
`arkoren`. Configure TLS, credentials, interceptors, and other native settings
through `ClientOptions` and `WorkerOptions`. When dialing, host address and
namespace override matching native fields, including when using host defaults.
The host adapts `slog.Default()` when no logger is supplied; an explicit native
client logger takes precedence. SDK option records are value copies, not
recursive snapshots.

If you already own a native client, wrap it instead:

```go
host := temporal.NewFrom(client, func(host *temporal.Builder) {
	host.TaskQueue("app")
})
```

Stop leaves this borrowed client open. Only TaskQueue, WorkerOptions,
WorkerStopTimeout, and NoWorker apply; host logger and connection settings do
not. Configure the borrowed client's logger before wrapping it: the worker
inherits that logger. `host.Client()` is available immediately for NewFrom,
but is nil before Start for a dialing host. A nil borrowed client fails startup.

### Shutdown Boundaries

Serialize Start and Stop; call Stop after Start returns. An already-canceled
startup context acquires nothing. Native worker startup has no context parameter,
so cancellation during that call is observed only after it returns, followed by
resource cleanup. Registration callbacks and SDK hooks must return. Registration
panics propagate after owned-client cleanup.

`WorkerStopTimeout` defaults to ten seconds; zero restores the default and a
negative host value disables grace. A nonzero native
`WorkerOptions.WorkerStopTimeout` wins. Native grace can apply separately to
internal workers before activity contexts are canceled: it is not a total
shutdown deadline and cannot terminate arbitrary callbacks.

Stop starts worker cleanup even with an expired context, then waits only until
its context ends. Cleanup continues afterward. The owned client closes only
after worker cleanup returns; a context error does not prove cleanup finished.
Repeated Stop calls wait on the same cleanup using their own contexts. Borrowed
clients remain yours to close.

See the [`temporal` host API](https://pkg.go.dev/arkoren.dev/driver/temporal).

## Restate

Restate reaches an endpoint in your application rather than polling a worker.
The host therefore has three lifecycle operations: Start opens a listener, Serve
handles requests, and Stop releases it.

Configure ingress/admin URLs that your process can reach, and an advertised URL
the Restate server can reach. Before constructing the host, obtain the server's
request-identity public keys (`publickeyv1_...`):

```go
import "arkoren.dev/driver/restate"

host := restate.New(func(host *restate.Builder) {
	host.Addr(":9080")
	host.IngressURL("http://restate:8080")
	host.AdminURL("http://restate:9070")
	host.AdvertisedURL("http://app:9080")
	host.IdentityKeys(identityKeys...)
	host.Register(true)
})
composer.Cleanup(host.Stop)
```

Here `identityKeys` is your configured `[]string` of server public keys. A serving
host without keys fails Start with `restate.ErrNoIdentityKeys`.
`AllowInsecure(true)` skips verification and permits anyone who can reach the
listener to invoke handlers; use it only for
[isolated local development](drivers.md#local-engine-1), never as a production
startup repair. The endpoint supports HTTP/1.1 and unencrypted HTTP/2; establish
production network access controls and transport protection separately.

Bind workflow and scheduler services to this host before startup, using their
constructors from the respective Drivers pages. Then add all three hooks:

```go
composer.Startup(host.Start)
composer.Run(host.Serve)
composer.Check("restate", host.Ping)
```

Start builds the endpoint and opens the listener without serving. Serve begins
request handling, optionally registers the deployment, and blocks until the
application stops. Stop also owns an opened listener if Serve never runs, such
as during startup rollback. Canceling Serve or a registration failure does not
replace cleanup.

Ping checks the admin API's health, not ingress reachability, registration,
successful workflow delivery, or persisted activation of the
[preflight-retention profile](drivers.md#cancellation-retention). Verify an actual
run when delivery readiness matters. The default advertised URL is
`http://localhost` plus the listen address; that only works when the server can
reach the application there.

### Deployment Policy

`Register(true)` asks Serve to register a deployment; otherwise operators must
register it. Each rollout creates a new deployment. `RegisterForce` overwrites
a deployment at the same URL and is appropriate only when none of the old work
is in flight. `RegisterBreaking` permits registration that removes services.
Neither option makes incompatible journals replay-safe. Use `AdminAuthKey` for
a protected admin API and `AuthKey` for protected ingress.

Drain workflow runs that used the earlier Child implementation on their original
deployment before upgrading to its rejection; there is no automatic history
migration. See [workflow rollout limits](drivers.md#run-lifetimes-1).

When the same host serves schedules, preserve retained generations and finish
in-flight Remove invocations on the old deployment before replacing an
incompatible journal. If an older deployment removed an entry, drain its pending
ticks before reusing its name. The
[Restate scheduler guidance](../scheduled-tasks/drivers.md#restate) owns those
schedule-specific constraints.

### Shutdown Boundaries

Stop waits for active HTTP requests until its context expires. It then closes
remaining connections and returns the shutdown error. Closing connections cancels
request contexts, not arbitrary Go code that ignores cancellation. The shared
HTTP client remains caller-owned and is not closed by endpoint cleanup.

`host.Query` runs admin SQL; restrict its credentials and never accept arbitrary
queries from untrusted callers. See the
[`restate` host API](https://pkg.go.dev/arkoren.dev/driver/restate).

## Client-Only Hosts

A web process can start work without serving it. For Temporal, set
`temporal.Builder.NoWorker(true)` and construct `temporalworkflow.NewDriver`.
For Restate, set `restate.Builder.ClientOnly(true)` and construct
`restateworkflow.NewDriver`. These host settings skip the worker or listener;
native connection and authentication still belong to the host.

Use the workflow driver's [client-only setup](drivers.md), not NewRunner.
Temporal rejects a runner on a NoWorker host. Scheduler drivers register work,
so construct those in the serving process instead. Match codec, prefix, and
native routing across producers and workers.

A host with no registered work also behaves client-only. NoWorker defaults false;
setting it false later restores permission but never forces a worker without
registrations. Temporal's WorkerDisabled reports the stored flag, not liveness.

Continue with [Running Workflows](running-workflows.md) once the host is started,
or return to [Scheduled Tasks](../scheduled-tasks/overview.md) for scheduler use.
