# Temporal Workflow

`arkoren.dev/driver/temporal/temporalworkflow` runs the Arkoren `workflow` facade on
[Temporal](https://temporal.io). Each facade workflow is a Temporal workflow,
each step an activity.

Install with `go get arkoren.dev/driver/temporal`. The examples import
`"arkoren.dev/core/service/workflow"`, `"arkoren.dev/driver/temporal"`, and
`"arkoren.dev/driver/temporal/temporalworkflow"`. For a standalone Compose file,
host configuration, and cluster-health check, see
[running Temporal locally](../../../cmd/arkoren/skills/arkoren/references/guides/workflows/drivers.md#running-locally).

Given the [onboarding workflow and step](../../../core/service/workflow/README.md),
register a worker before starting your existing `app`. This address
matches the module's local Compose server:

```go
err := app.Run(ctx, func(compose *application.Composer) {
	registry := compose.Must(workflow.Handlers(Onboard, SendWelcome))
	host := temporal.New(func(host *temporal.Builder) {
		host.HostPort("localhost:17233")
		host.TaskQueue("app")
	})
	compose.Cleanup(host.Stop)
	// Worker: register workflows and activities before host.Start.
	runner := compose.Must(temporalworkflow.NewRunner(host, registry))
	client := workflow.NewClient(runner.Driver())
	// Pass client to your application services here.
	_ = client
	compose.Startup(host.Start)
})
```

Import `application` from `arkoren.dev/core/application`. Composition completes
before startup; cleanup is registered before runner validation can fail.

After the application starts the host, use
`client.Start(ctx, Onboard, "user-42", user)` to submit your `User` payload.

A process that only starts and controls runs needs no worker. Use this
alternative in its composition callback, with the same `registry`:

```go
err := app.Run(ctx, func(compose *application.Composer) {
	host := temporal.New(func(host *temporal.Builder) {
		host.HostPort("localhost:17233")
		host.TaskQueue("app")
		host.NoWorker(true)
	})
	compose.Cleanup(host.Stop)
	driver := compose.Must(temporalworkflow.NewDriver(host, func(driver *temporalworkflow.DriverBuilder) {
		driver.Registry(registry)
	}))
	client := workflow.NewClient(driver)
	// Pass client to your application services here.
	_ = client
	compose.Startup(host.Start)
})
```

## Execution and handle safety

Use the same prefix (`Prefix`, default `arkoren`) and task queue in both
processes. `Registry` lets the driver set the Temporal execution timeout
from MaxDuration plus Delay as a backstop; the runner always enforces
MaxDuration with a timer inside the workflow. `NewRunner` on a `NoWorker(true)`
host is an error. Never block on `ctx.Done()` in a workflow function; poll it.

Results, statuses, cancellation, and signals verify the native workflow type
before using a named handle. A wrong workflow name returns `workflow.ErrNotFound`
without reading output or issuing the command. Native clients therefore need
description permission as well as command permission. These checks do not replace
application authorization, and IDs remain unique across the namespace.

An explicit step `Timeout(0)` removes the attempt timeout; the workflow's
MaxDuration remains the upper bound. Retry Max caps the first retry as well as
later retries; Max zero does not inherit Temporal's implicit 100-times-initial
cap. Backend duration limits still apply: the requested largest Go duration can
be reduced by the server, so this is not full Go-duration portability or a
universal uncapped duration guarantee.
The integration-tested two-attempt policy on server 1.29.1 retained `876000h`
(100 times 365 days), rather than the requested largest Go duration. Check your
server's version and persisted policy instead of assuming that bound universally.

A completed step policy with multiple attempts and Initial zero returns
`workflow.ErrUnsupported` before encoding or scheduling the activity, even if
the handler would have succeeded on its first attempt. No approximate positive
delay is inserted. Single-attempt policies remain supported because backoff is
unused; `Step.Derive` determines the local policy independently of registration.
Ordinary defaults are unchanged: one attempt, and positive one-second initial
backoff when `Retry` enables further attempts. Whole-workflow execution retries
remain disabled; step retry counts stay explicitly finite.

Child-result and Spawn-acknowledgement waits end when the parent is canceled or
reaches MaxDuration, independently of native future completion. Spawn still uses
detached child ownership: an error while waiting does not prove the child never
started. `Child` acquires cancellation ownership only when Temporal atomically
creates the native child. A duplicate start joins through a result-reading
activity, ignores the replacement input, and acquires no ownership. Canceling or
timing out that parent ends only its wait; the original run retains its owner and
can still finish through a signal. Newly created children still stop with their
parent, using native cancellation or parent-close termination. This ownership
clarification adds no history command or replay marker and preserves the existing
child/join command sequence. Workflow logging resolves bound and grouped `slog.LogValuer` values
before forwarding them to the native logger, so native formatting cannot bypass
their redaction.

### Existing histories require a rollout decision

Older workers silently mapped zero initial delay to a positive interval.
Rejecting that step now changes its native command sequence. Keep affected
executions routed to legacy workers until they finish, or drain them before
deploying this change. The driver adds no native version marker and does not
use `IsReplaying` to bypass rejection. Mixing old and new workers on the same
unversioned task queue does not provide that routing guarantee.

If you intentionally want the previous observed behavior, configure its positive
initial interval explicitly, including local derived definitions. The previous
fallback was one second before applying Max. Replay representative retained
histories before deployment; a matching-policy replay fixture is not proof that
every application history is compatible. Applications needing truly immediate
retries can choose a compatible driver or explicitly design a bounded workflow
loop of single-attempt steps; that changes history and requires its own rollout.

## Native ownership and shutdown

`NoWorker` defaults to false; a later false restores worker permission without
forcing an empty worker. `NewFrom` borrows the SDK client and applies TaskQueue,
WorkerOptions, WorkerStopTimeout, and NoWorker. Configure logging on the supplied
native client: NewFrom ignores the host Logger setting. Stop leaves that client
open. A nonzero native worker stop timeout wins over the host timeout. SDK option
records are copied by value, not recursively.

Call `host.Stop` after `host.Start` has returned. Stop initiates cleanup even
with an expired context; its context bounds waiting for native worker cleanup,
not arbitrary callback execution. An owned client is released after native
worker cleanup completes, possibly after Stop returns. The native worker timeout
is a grace wait, not a total shutdown budget: SDK hooks must return, and activity
code must observe context cancellation. A canceled startup releases acquired
resources, but native worker startup itself has no context and must return first.

## Contributor verification

Contributor integration setup: `docker compose up -d --wait` in `driver/temporal` starts
the `temporal` dev server of its `compose.yaml` on `127.0.0.1:17233`. See the package documentation for
the semantics of MaxDuration, cancellation, signals, and children.
`TestIntegrationServerVersionAndRetryMaximum` observes the server version through
the public native SDK and records its server-persisted activity retry maximum;
the image tag alone is not version evidence.

## Construction callbacks

Construction callbacks run synchronously once, in order, on one fresh draft.
Each setting returns that same draft. Omitted callbacks and expanded nil or empty
slices leave defaults unchanged; a supplied nil callback panics. Publication
detaches owned settings, not clients, services, SDK internals, or function captures.
Use `DriverBuilder` with `NewDriver` and `RunnerBuilder` with `NewRunner`.
Only `DriverBuilder` has `Registry`; the required `NewRunner` registry supplies its driver registry.
