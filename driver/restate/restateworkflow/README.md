# Restate Workflow

`arkoren.dev/driver/restate/restateworkflow` runs the Arkoren `workflow` facade on
[Restate](https://restate.dev). Each facade workflow becomes one Restate
workflow service; steps run inline as durable `restate.Run` actions.

Install with `go get arkoren.dev/driver/restate`. For a standalone Compose file,
local host URLs, and a health check, see
[running Restate locally](../../../cmd/arkoren/skills/arkoren/references/guides/workflows/drivers.md#running-locally-2).

Given the [onboarding workflow and step](../../../core/service/workflow/README.md)
and existing `app` and `ctx` values, configure the host's authenticated endpoint:

Set `RESTATE_INGRESS_URL` and `RESTATE_ADMIN_URL` to your server endpoints,
`RESTATE_ENDPOINT_URL` to the application URL reachable by Restate, and
`RESTATE_IDENTITY_KEY` to the server's request-identity public key.

```go
import (
	"os"

	"arkoren.dev/core/application"
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/restate"
	"arkoren.dev/driver/restate/restateworkflow"
)

err := app.Run(ctx, func(compose *application.Composer) {
	logger := compose.App().Logger()
	observations := compose.App().Telemetry()

	registry := compose.Must(workflow.NewRegistry([]workflow.Definition{Onboard, SendWelcome}, func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(observations)
	}))
	// IdentityKeys are the Restate server's request-identity public keys
	// ("publickeyv1_..."). AllowInsecure(true) is for local development only.
	// Omitted keys require explicit insecure serving; malformed keys fail validation.
	host := restate.New(func(host *restate.Builder) {
		host.Addr(":9080")
		host.IngressURL(os.Getenv("RESTATE_INGRESS_URL"))
		host.AdminURL(os.Getenv("RESTATE_ADMIN_URL"))
		host.AdvertisedURL(os.Getenv("RESTATE_ENDPOINT_URL"))
		host.Register(true)
		host.IdentityKeys(os.Getenv("RESTATE_IDENTITY_KEY"))
		host.Logger(logger)
	})
	compose.Cleanup(host.Stop)
	compose.Must(restateworkflow.NewRunner(host, registry))
	driver := compose.Must(restateworkflow.NewDriver(host))
	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Telemetry(observations)
	})
	// Pass client to your application services here.
	_ = client
	compose.Startup(host.Start)
	compose.Run(host.Serve)
})
```

Composition completes before startup; serving begins only after all startup
callbacks succeed. A process that only starts runs can set
`Builder.ClientOnly(true)` on its host;
nothing listens and nothing is registered.

After the application starts and the deployment is registered, use
`client.Start(ctx, Onboard, "user-42", user)` to submit your `User` payload.

Call `NewRunner` before `host.Start`; `NewDriver` binds nothing and can be
constructed independently. The service for workflow `onboard` is
`ArkorenWf_onboard` (prefix `arkoren`, see `Prefix`) with the
handlers `Run`, `Signal`, and `Cancel`.

Contributor integration setup: `docker compose up -d --wait` in `driver/restate` starts the `restate` service of its `compose.yaml` (ingress on `127.0.0.1:18080`, admin on `127.0.0.1:19070`).
See the [package API](https://pkg.go.dev/arkoren.dev/driver/restate/restateworkflow)
for MaxDuration, cancellation, and signal semantics.

## Retries, cancellation, and shutdown

Step retry limits include the first attempt. An initial backoff of zero requests
an immediate retry; a maximum of zero removes the cap up to Go's largest duration.
Restate expresses retry delays in milliseconds and the multiplier as `float32`.
These policies are explicit rather than inherited from the SDK's 50-millisecond
initial delay and two-second maximum. Existing completed Run results remain
journaled and are not executed again.

A positive step timeout supplies a context deadline. Handlers must use that
context for blocking work and return when canceled. A handler that returns success
after its deadline is reported as `workflow.ErrTimeout`. The driver cannot kill
arbitrary handler code, undo its side effects, or make an uncooperative attempt
stop. `MaxDuration` is a durable timer observed at workflow runtime boundaries;
its completion is not proof that a native step has stopped running.

`Child` returns `workflow.ErrUnsupported` before sending or attaching to a run.
The [durable SDK receipt](https://pkg.go.dev/github.com/restatedev/sdk-go@v1.1.0#Invocation)
identifies the invocation but does not distinguish a newly created child from an
existing run. The pinned [native protocol](https://github.com/restatedev/restate/blob/v1.7.12/service-protocol/dev/restate/service/protocol.proto)
likewise reports only an invocation ID, not a creation acknowledgement.
Canceling that ID could stop another
owner's run; never canceling it would leave newly created children detached.
A status lookup followed by a send cannot make that ownership decision atomic.
An external ingress receipt recorded in a `Run` action also leaves a crash window
between server acceptance and journaling the receipt. The driver uses neither
workaround. `Spawn` remains supported for explicitly detached work; it is not a
replacement when your application requires parent-owned child cancellation.

This rejection changes the journal sequence of existing `Child` histories.
Drain affected runs on their original deployment before upgrading, and do not
force-migrate them to new workers. Completed retained outputs stay readable.
The driver adds no replay marker or automatic history migration. Conformance
asserts explicit rejection, no new child submission, and an untouched independent
target; it does not claim supported child cancellation or joining.

`Cancel` requires the admin API. The legacy server profile drops an invocation
canceled before its handler begins: Result and Status return
`workflow.ErrNotFound`, and Start can reuse the key immediately. Portable retained
cancellation requires positive effective workflow retention and the persisted
`preflight_invocation_termination_retention` server feature. Restate 1.7.12 includes
this opt-in feature, introduced in 1.7.8. It establishes a persistent minimum
server version; operators must plan that change and cannot recover records
already discarded. See the [native feature's release notes](https://github.com/restatedev/restate/blob/v1.7.12/release-notes/v1.7.8.md#opt-in-retention-for-terminated-pre-flight-invocations).

The driver neither enables nor verifies this profile. Admin `/health` confirms
only HTTP readiness, and `/version` reports requested experimental flags, not
per-partition barrier application. In pinned 1.7.12, the public admin SQL context
does not expose the internal `partition_state.enabled_features` table. Do not
present those checks as a verified retained-cancellation readiness gate. No
process-local tombstone or silent fallback supplies the missing guarantee.

For delayed runs, Status and Signal also use admin introspection when ingress
has no output yet. An unavailable admin/result API remains an error, not a
false report that the run is absent.

Always register host cleanup, including when a later startup callback can fail
before serving begins. `host.Stop` closes a listener that never reached Serve.
It waits for active HTTP requests until its context expires, then closes remaining
connections. That cancels request contexts but cannot forcibly terminate code
that ignores them. A supplied HTTP client and its transport remain caller-owned.

Client telemetry and worker-registry telemetry are independent, as shown above.
Only actual step attempts extract the durable start carrier; orchestration replay
does not create workflow-execution spans. Trace headers remain separate from
application metadata and signals do not replace the start parent.

Construction callbacks run synchronously once, in order, on one fresh draft.
Each setting returns that same draft. Omitted callbacks and expanded nil or empty
slices leave defaults unchanged; a supplied nil callback panics. Publication
detaches owned settings, not clients, services, SDK internals, or function captures.
Use `DriverBuilder` with `NewDriver` and `RunnerBuilder` with `NewRunner`.

## Contributor retention profiles

The integration fixture keeps both native flags off by default. Enable them only
when creating a fresh disposable cluster: they apply persistent partition
features. Never use these commands to reconfigure an existing cluster.

From the repository root, use the integration target's unique project and teardown:

```sh
env -u COMPOSE_PROJECT_NAME \
  ARKOREN_RESTATE_PREFLIGHT_RETENTION=true \
  ARKOREN_RESTATE_VQUEUES=true \
  GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  make integration MODULE=driver/restate
```

The target removes its containers, network, and volumes after success or failure.
Both profiles use the pinned `docker.restate.dev/restatedev/restate:1.7.12` image.
Set `ARKOREN_RESTATE_PREFLIGHT_RETENTION=false` in a separate fresh project to
compare legacy behavior. The environment mapping is
`RESTATE_EXPERIMENTAL_ENABLE_PREFLIGHT_INVOCATION_TERMINATION_RETENTION`;
the corresponding native setting is
`common.experimental-enable-preflight-invocation-termination-retention`.

New cancellation fixtures explicitly retain workflow results for five minutes.
The delayed case uses Arkoren Driver methods for Result, Status, duplicate Start,
and repeated Cancel. The queued case holds a real scoped concurrency slot and
observes `inboxed` through native introspection before cancellation. It uses the
bound Runner through public SDK/admin calls because the Driver has no scope
setting. Both cases require zero application executions. They assert the chosen
profile explicitly; a retained-result failure never falls back to the legacy
assertions.

VQueues are needed only for the deterministic queued fixture, not as a production
requirement for preflight retention. With `ARKOREN_RESTATE_VQUEUES=false`, that
fixture explicitly skips; the delayed case and remaining integration suite still
exercise the minimal retained-cancellation profile. None of these behavioral
checks is a read-only, cluster-wide proof of feature-barrier application.
