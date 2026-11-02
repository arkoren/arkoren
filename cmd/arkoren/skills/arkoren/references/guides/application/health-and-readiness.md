---
title: Health and Readiness
description: Control traffic with cached dependency checks while keeping restart and startup decisions separate.
order: 40
summary:
  - Select required dependency checks and distinguish traffic readiness from liveness and startup completion.
  - Tune non-overlapping background passes while accounting for cooperative cancellation and stale results.
  - Inspect cached reports safely and run fresh checks without changing the traffic verdict.
  - Interpret health-transition events without treating them as every possible cause of unavailability.
---

# Health and Readiness

A database outage can make requests fail while the service process remains
healthy enough to recover. Use **readiness** to control traffic, **liveness** for
restart decisions, and a **startup probe** to give initialization time to finish.
Restarting every replica does not repair an unavailable shared database.

This page assumes an [application coordinator](overview.md) with dependency
[startup and cleanup](lifecycle-and-ownership.md) already registered. Add checks
and routes during synchronous composition, before startup begins. Import
`application` from `arkoren.dev/core/application`.

## Select Required Checks

Include a dependency when requests cannot succeed without it. Leave optional
integrations out: otherwise their shared outage can remove every replica from
traffic. For an existing root `connection *postgresdatabase.Driver` from
`arkoren.dev/driver/postgres/postgresdatabase`, register its connectivity check:

```go
composer.Check("inventory-database", connection.Ping)
```

Registration does not call `Ping`. After composition and all startup hooks
succeed, a background loop begins checking. `Start` does not wait for its first
result. Nil means passing; an error or panic means failing. A failed check
changes readiness, not startup success, liveness, or runner-failure supervision.
The loop continues so a later successful pass can restore traffic.

Check names must be nonempty and application-wide unique; callbacks must be
nonnil. Invalid registration panics with `ErrNilFunc` or `ErrDuplicateCheck`.
Register only during synchronous composition, not in startup, requests, or later
goroutines; invalid phases panic with `ErrInvalidPhase`. Custom network checks
must propagate their context and close response bodies. In-memory dependencies
need no connectivity check.

## Mount Probes

Probe handlers have no built-in authentication. Mount them on a listener with
restricted network access that your platform can reach. Kubernetes HTTP probes
normally connect to the pod IP; a different port does not make a loopback-only
listener reachable. Keep sensitive
[diagnostics](../diagnostics/capture-setup.md) on loopback with port forwarding
or behind authentication and authorization instead.

With `app := composer.App()` and an existing `routes *router.Router` from
`arkoren.dev/core/http/router`, mount the three application-owned handlers:

```go
routes.Get(application.ReadyPath, app.ReadyHandler())
routes.Get(application.LivePath, app.LiveHandler())
routes.Get(application.StartupPath, app.StartupHandler())
```

The paths are `/readyz`, `/livez`, and `/startupz`. Each returns `200` with
`ok\n` or `503` with `unavailable\n`. None calls a dependency on request; polling
readiness more often does not cause more database calls.

| Probe | Success Means | Platform Use |
| --- | --- | --- |
| Readiness | The application is live and its cached checks pass and remain fresh. Without checks, readiness equals liveness. | Admit traffic. |
| Liveness | The application is running, no runner failure is recorded, and the runner group has not finished. | Decide whether to restart. |
| Startup | Startup completed, without waiting for checks. | Protect initialization from premature restarts. |

Before startup completes, all return `503`. Shutdown makes liveness and readiness
unavailable immediately. The startup probe stays successful during and after
shutdown following successful startup, if its listener remains reachable. No
verdict keeps a stopped listener reachable. A database-check failure alone does
not change liveness, while a runner failure does even if cached checks pass.
Configure your platform's [health probes](../first-steps/deploying.md#health-probes)
accordingly; never use readiness as a restarting probe.

If you include probes in OpenAPI, pass `application.ReadyOperation`,
`LiveOperation`, or `StartupOperation` as the corresponding route's third
argument. These [annotations](../openapi/built-in-handlers.md) describe responses;
they do not mount handlers, enable details, or apply access control.

## Tune Check Timing

With checks registered, readiness remains pending until the first completed
passing pass. A failure makes it unavailable; a later passing pass restores it
without restarting. A cached result remains fresh through three intervals of age.
Older results make readiness unavailable even if their individual checks passed.
Results interrupted by shutdown are discarded.

The first pass starts immediately after startup. Later passes follow an interval
ticker, not a full interval after completion. A 400 ms pass on a one-second
interval normally starts its next pass one second after the first began, not
1.4 seconds later. Checks in a pass run concurrently; the pass waits for all of
them, and background passes never overlap. Slow callbacks delay later observations.

Keep the defaults unless dependency latency or your traffic-removal budget needs
different timing. The interval defaults to five seconds; the timeout defaults to
two fifths of the configured interval, or two seconds by default. To use a shorter
cadence, set both during construction (`time` is the standard library package):

```go
app := application.New(func(app *application.Builder) {
	app.ReadinessInterval(2 * time.Second)
	app.ReadinessTimeout(500 * time.Millisecond)
})
```

Zero or negative selects the respective default. Keep the timeout shorter than
the interval. These settings are not setters on a running application.

> [!WARNING]
> Timeouts cancel the pass context, not callback code. A check that ignores
> cancellation prevents completion and delays later passes and shutdown. Keep
> checks lightweight, honor cancellation, and return an error when canceled.
> Returning nil after the deadline is not automatically converted into failure.

## Inspect Cached Results

When traffic stops reaching an instance, request `/readyz?verbose` on the
restricted probe listener first. It returns JSON with the plain probe's status
and no new dependency calls. Presence of `verbose` selects JSON regardless of
its value, including `false`. A failed check normally appears as:

```json
{
  "status": "unavailable",
  "checked_at": "2026-11-01T09:00:00Z",
  "checks": {
    "inventory-database": {
      "status": "failed",
      "error": "reason withheld"
    }
  }
}
```

The timestamp identifies the completed observation, not the request time. Reports
also explain `not live`, `no health pass completed`, and `health result stale`.
A stale report can still show passing checks; inspect the overall status and
reason. Plain and verbose probes set `Cache-Control: no-store` and
`X-Content-Type-Options: nosniff`.

Keep `HealthDetails` disabled unless every affected endpoint is operator-protected.
For an operator-restricted deployment, enable details during construction:

```go
app := application.New(func(app *application.Builder) {
	app.HealthDetails(true)
})
```

This includes backend error text, which can reveal addresses and connection
details. Names remain visible when details are disabled. Actual errors are logged
on failure transitions regardless of this setting; protect those logs too.

The setting also governs cached and fresh MCP health reports, `healthy` results,
and check details in fail-fast condition failures. It does not redact trusted
in-process `Checks`, `RunChecks`, or `CheckHealth` results, or event payloads.
Protect all affected remote endpoints before enabling it.

Inside the process, inspect cached typed records without contacting dependencies.
With the existing `app` and the standard library `fmt` package:

```go
for _, status := range app.Checks() {
	fmt.Println(status.Name, status.Err == nil, status.CheckedAt)
}
```

`Checks` returns copied records in registration order; error values remain shared.
It returns nil before the first completed pass and when no checks exist.
`CheckedAt` is the latest pass's completion time; `Since` records the first
observation or latest passing/failing transition. Use
[application diagnosis](../diagnostics/application-diagnosis.md) for remote
investigation, and the
[`ReadyHandler` contract](https://pkg.go.dev/arkoren.dev/core/application#Application.ReadyHandler)
for report edge cases.

## Run Fresh Checks

An admin command or test may need the dependency's current state rather than the
cached traffic verdict. With an already started `app` and a caller context `ctx`
carrying a deadline:

```go
err := app.CheckHealth(ctx)
```

Nil means every registered check passed. Otherwise the error joins failed checks
in registration order, each prefixed with `health` and its name. This is the
command's dependency verdict, not an application startup error. Use
`app.RunChecks(ctx)` if you need individual passing and failing `CheckStatus`
records; their `Since` is zero because a fresh run has no transition history.

Both methods run checks concurrently with the configured readiness timeout and
the caller's deadline, and wait for every callback to return. They start no
dependencies, update no readiness cache, and publish no transition events. Calling
before composition cannot see checks not yet registered. A fresh success does
not restore traffic; the next passing background observation does.

Test callbacks directly for their behavior. For wiring tests, compose and start
the application before fresh checks and arrange shutdown even if assertions fail.
For cached transitions, use `testing/synctest` rather than fixed sleeps: a fresh
check assertion does not exercise the readiness cache.

## Observe Transitions

Background logs record failure and recovery transitions, not every repeated
verdict. `CheckFailed` fires for a first failing observation or a change from
passing to failing; `CheckRecovered` fires after a failing check passes.
`Ready` and `Unready` report the first completed pass's verdict and later changes
to whether all checks pass. These events describe background observations,
independently of lifecycle liveness.

Staleness, lifecycle changes, and explicit fresh checks publish none of these
events, so events alone cannot explain every `503`. Health payloads are trusted
in-process data; send errors only to authorized destinations, and do not mutate
shared payload slices. Listener failures are logged without changing lifecycle
success. Choose [observers rather than hooks](lifecycle-and-ownership.md#observe-outcomes)
when alerts must not control startup or shutdown.

For check records, probe handlers, and settings, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application).
