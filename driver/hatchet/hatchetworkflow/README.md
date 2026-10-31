# Hatchet workflow driver

`arkoren.dev/driver/hatchet/hatchetworkflow` runs `arkoren.dev/core/service/workflow` definitions on
[Hatchet](https://hatchet.run) durable tasks. Hatchet owns retries, timers, signals, and replay; the facade owns
what runs.

Install with `go get arkoren.dev/driver/hatchet`. The examples import
`"arkoren.dev/core/service/workflow"`, `"arkoren.dev/driver/hatchet"`, and
`"arkoren.dev/driver/hatchet/hatchetworkflow"`. For the application-owned
Compose file, API token setup, and readiness check, see
[running Hatchet locally](../../../cmd/arkoren/skills/arkoren/references/guides/workflows/drivers.md#running-locally-1).

Needs Hatchet engine v0.80.0 or newer (durable eviction). Check with `Host.Start`, which fails with
`hatchet.ErrEngineUnsupported` on older engines. Exact registered retry delays
require v0.111.0+ and a sufficient engine retry-delay cap; see the limits below.

Given the [onboarding workflow and step](../../../core/service/workflow/README.md),
your Hatchet `token`, and an existing `app`, register a worker before
startup. The address below matches the local Compose server:

```go
err := app.Run(ctx, func(compose *application.Composer) {
	host := hatchet.New(func(host *hatchet.Builder) {
		host.Token(token)
		host.HostPort("localhost:17077")
		host.TLSStrategy("none") // trusted local Compose server only
	})
	compose.Cleanup(host.Stop)
	registry := compose.Must(workflow.Handlers(Onboard, SendWelcome))
	runner := compose.Must(hatchetworkflow.NewRunner(host, registry)) // before host.Start
	client := workflow.NewClient(runner.Driver())
	// Pass client to your application services here.
	_ = client
	compose.Startup(host.Start)
})
```

Import `application` from `arkoren.dev/core/application`. All registrations
finish before startup; cleanup also runs if composition or startup fails.

A web process that only starts and queries runs:

```go
err := app.Run(ctx, func(compose *application.Composer) {
	host := hatchet.New(func(host *hatchet.Builder) {
		host.Token(token)
		host.HostPort("localhost:17077")
		host.TLSStrategy("none") // trusted local Compose server only
		host.NoWorker(true)
	})
	compose.Cleanup(host.Stop)
	driver := compose.Must(hatchetworkflow.NewDriver(host))
	client := workflow.NewClient(driver)
	// Pass client to your application services here.
	_ = client
	compose.Startup(host.Start)
})
```

A host without registrations also runs as a client only. Use the same `Prefix` in the worker and the client process.

## Construction settings

Construction callbacks run synchronously once, in order, on one fresh draft.
Each setting returns that same draft. Omitted callbacks and expanded nil or empty
slices leave defaults unchanged; a supplied nil callback panics. Publication
detaches owned settings, not clients, services, SDK internals, or function captures.
Use `DriverBuilder` with `NewDriver` and `RunnerBuilder` with `NewRunner`.

| Builder method | Default | Notes |
|---|---|---|
| `Prefix` | `arkoren` | Names of workflows, steps, events, idempotency keys. |
| `Lookback` | 30 days | Run/marker search window and marker idempotency TTL; cover queueing, execution, and recovery. |
| `TimeoutMargin` | 1 minute | Added to MaxDuration for the Hatchet execution timeout (a backstop). |
| `ScheduleTimeout` | 24 hours | How long a run may wait for a worker slot. |
| `EvictionTTL` | 1 minute | Durable wait length before the run leaves its slot. |
| `PollInterval` | 5 seconds | Cap of the status poll delay in `Result`. |
| `LookupGrace` | 1.5 seconds | Wait for a run that is not listed yet. Negative turns it off. |
| `Logger` | `slog.Default()` | |

Host builder settings added for this driver: `DurableSlots(n)` and `NoWorker(true)`.

`NoWorker` defaults to false; a later false restores worker permission without
forcing a worker when no registrations exist. `HasWorker` reports permission,
not a running worker. Native client options follow derived settings, then the
host logger replaces the native logger. The host copies the outer option slice,
not SDK functions or their captures.

## Behavior

- `Start`: a versioned idempotency key independently encodes the effective namespace, prefix, name, and logical ID
  (TTL = MaxDuration + Retention; zero Retention uses Lookback).
  A collision is `ErrAlreadyStarted`, also after the run ended. `Delay` is a durable sleep at the start of the run (above `TimeoutMargin`: `ErrUnsupported`).
- Each logical lookup rediscovers the current physical execution under the exact
  namespaced workflow and versioned key. Workflow discovery is paginated; stored
  input identity is checked before retaining a consistency-retry hint. The cache
  cannot indefinitely select an old execution after logical-ID reuse.
  Discovery uses the remaining `LookupGrace` for its final poll rather than
  returning early when a full backoff will not fit. This does not extend the
  configured wait window or guarantee native visibility; native request time is
  additional. Verification can still fail after the engine accepted the
  submission. Reconcile the existing execution
  before treating such an error as proof that no work started.
- `Result` pins that resolved execution and polls with a growing delay. Every
  timer, including the first and its jitter, is capped by `PollInterval`; request
  time is additional. Native-call cancellation preserves the caller's context
  error identity and does not cancel the execution. Failures carry the facade error envelope,
  so `errors.Is(err, workflow.ErrTimeout)` and custom kinds work. Hatchet timeouts without an envelope map to
  `ErrTimeout`; canceled runs map to `ErrCanceled`.
- `Signal` checks that the run is queued or running (`ErrNotFound` otherwise), stores the first value in a marker run
  (`<prefix>-signal`, idempotent per physical execution and signal), then pushes `<prefix>.sig.<name>` with the verified physical scope and marker ID.
  Hatchet delivers the newest of several pending events; the waiter reads the first value from the marker.
- `Child` and `Spawn` return `workflow.ErrUnsupported` before submission. This is fail-closed containment of replay duplication,
  not restored native child support. `Step` and external client `Start` remain supported.
- Per-call step options shorten registered attempts/timeout. A completed zero
  initial backoff is honored even when registration has a positive default;
  other per-call backoff changes remain ignored by this mapping.
- Set namespaces with `hatchet.Builder.Namespace` (or `HATCHET_CLIENT_NAMESPACE`), not `ClientOptions`.
- `MaxDuration` starts at the memoized first worker invocation, plus `Delay`, not
  submission. The concrete native durable context gets that deadline for waits,
  step joins, and RPCs, and is restored before terminal reporting. Late success
  becomes `ErrTimeout`. Application code must cooperate with cancellation.
- Steps run as child tasks of the workflow. `workflow.Permanent` becomes a non-retryable error; the step enforces its own
  timeout (`ErrTimeout`).
- `Context.Raw()` returns the `hatchet.DurableContext`.

## Native limits and lifecycle

`NewDriver` and `NewRunner` perform no I/O and reject negative adapter durations.
Negative `LookupGrace` intentionally disables consistency retries. `NewRunner`
also rejects unrepresentable native retry/timeout ranges and registered
`Step.Timeout(0)` with `workflow.ErrUnsupported`: unlimited is not silently
replaced with 24 hours. Keep the registry and required workflow `MaxDuration`.

Registered `Initial`, `Factor`, and `Max` produce explicit `RetryAfterError`
delays without adding durable history nodes. Engine v0.111.0+ honors those delays;
older engines fall back to native factor/max backoff, which cannot preserve
arbitrary `Initial`. The SDK truncates delays to milliseconds, and the engine can
clamp them to its configured maximum. Set that maximum to cover your retry policy.
`Initial == 0` sends an explicit zero override: engine v0.111.0+ adds no
deliberately positive delay between retries, though scheduling and delivery still
take time. It does not make attempts unlimited. Older engines cannot preserve
that zero-delay policy through their native backoff.
Nonzero per-call backoff remains ignored; per-call attempts/timeouts can only
shorten registration. Durable memo transport must be available: a failed SDK capability
probe now fails closed before application code instead of using process-local
memo values. Wire keys, memo ordering, and `signal_version=2` stay unchanged.

Derived zero-delay calls carry `retry_without_delay` in the private step input.
Missing fields retain the registered policy for old inputs. Upgrade every worker
before allowing producers to depend on this field: old workers ignore it and
apply positive backoff. Drain existing workflows containing affected derived
calls with their old deployment before replacement; replaying those calls can
change the recorded native child input. No durable node or wait predicate is
added, and unaffected calls keep their previous serialized input.

Register before `Host.Start`; registration during acquisition is rejected.
`Host.Stop` is terminal, waits for concurrent acquisition, cancels and joins
admitted application callbacks, and invokes native release once even if its
context has expired. Noncooperative bodies can prevent return. The pinned SDK
does not expose joinable intake/reporting goroutines, and its ordinary
`Client.Close` does not close gRPC connections (embedded mode does release its
backend). Some startup RPCs use background contexts; `RegisterTimeout` only
budgets registration-log observation. Canceled observation waits for the same
handoff and performs cleanup synchronously. SDK worker failures can panic in an
internal goroutine, outside the host's recovery boundary. Use a process
supervisor; do not assume caller-bounded startup or shutdown.

The pinned SDK's `StartBlocking(ctx)` calls `Start()` before waiting for ctx;
it does not make startup cancelable. Its `WithPanicHandler` handles application
task panics, not the
[startup goroutine's panic](https://github.com/hatchet-dev/hatchet/blob/v0.114.11/pkg/worker/worker.go#L534-L559).
The SDK exposes no per-client dialer/interceptor or supplied-connection option
that lets this adapter bound those background startup RPCs. Its public client
wrapper also does not expose the underlying legacy connection's Close method.
See [upstream startup cancellation issue #3283](https://github.com/hatchet-dev/hatchet/issues/3283);
issue closure is not evidence that the pinned implementation is fixed.

Durable shutdown is not race-clean in the pinned SDK. The race detector found
the shutdown eviction logger reading `WaitKind` without the record lock while
a returning native wait clears it under that lock. See
[`EvictAllWaiting`](https://github.com/hatchet-dev/hatchet/blob/v0.114.11/sdks/go/internal/eviction/manager.go#L206-L231)
and [`MarkActive`](https://github.com/hatchet-dev/hatchet/blob/v0.114.11/sdks/go/internal/eviction/cache.go#L155-L174).
The adapter cannot lock that private cache. Joining callbacks before native
release is not an equivalent fix: callbacks unregister the durable records that
native release uses for graceful eviction. Keep calling `Stop`, but do not treat
functional recovery tests or suppressed diagnostics as proof of race-free
shutdown. An SDK correction requires a separate dependency decision.

Worker attempt telemetry follows the core `StepHandler.Handle` contract: it
observes each actual handler invocation and its returned error, not the engine's
terminal outcome. A body that returns nil after its deadline therefore records
one successful attempt observation, while the adapter rejects its late result
with `ErrTimeout`. Both outcomes are intentional at their respective boundaries;
no duplicate adapter attempt is emitted. Client `Result` observes the timeout
separately. Worker telemetry does not publish a terminal engine event.

Workflows calling Child/Spawn fail unless they handle `ErrUnsupported`. Pause producers, drain old workflows with the old
deployment, and reconcile existing child effects before replacing workers and clients together. Native/raw submission or
ordinary Start inside a parent is not a safe substitute. See the canonical
[Hatchet workflow reference](https://pkg.go.dev/arkoren.dev/driver/hatchet/hatchetworkflow) for signal protocol
version 2, retained queued-event matching, retention requirements, and the coordinated upgrade procedure.

## Contributor integration test

The disposable fixture pins the first explicit-retry-delay release, engine
`v0.111.0`, to GHCR multi-architecture digest
`sha256:0776a420bc6af14cc0ca4e5ddb0f6ad6e64375ccbeb2accdc1a88c9877969085`.
Its source tag resolves to `fd1ba5f4b931b270fcbe8e2ddf29f1646c2d5552`;
the [release's public capability declaration](https://github.com/hatchet-dev/hatchet/blob/v0.111.0/sdks/go/engine_version.go)
sets `RetryAfter` to `v0.111.0`. See `compose.yaml` for the arm64 child digest.
This changes only disposable infrastructure, not the Go SDK or a real deployment.

`docker compose up -d --wait` in `driver/hatchet` starts the `hatchet` and `hatchet-postgres` services of its `compose.yaml` (hatchet-lite on
`127.0.0.1:18888` for the API and `127.0.0.1:17077` for gRPC). Then run
`GOWORK=off go test -race ./...` from that module directory to run the
`workflowtest` suite, a client-only check, and an eviction replay check against
them. The tests log in as the admin user that hatchet-lite creates and create an API token
over REST, so no setup is needed. Child/Spawn containment checks require zero physical children/effects and an external-Start
positive control. Allow several minutes for the full suite. Run ids are unique per
run, so the engine can keep its history. `go test -short` skips these tests. Optional overrides: `ARKOREN_HATCHET_API`
and `ARKOREN_HATCHET_GRPC`.
