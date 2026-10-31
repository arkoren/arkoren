---
title: Application Health and Readiness
nav: Health and Readiness
description: Keep unusable instances out of traffic, tune background checks, and inspect cached readiness safely.
order: 40
---

# Application Health and Readiness

A PostgreSQL outage should remove an unusable instance from traffic, not restart
every replica. Use **readiness** to decide whether an instance can serve work,
**liveness** to decide whether it is still running, and a **startup probe** to
protect initialization from premature restarts.

These examples assume your existing PostgreSQL-backed HTTP service. Add the
registration and routes during its next startup, inside the synchronous
[composition](composition.md) you already use. Here, `composer` is the current
`*application.Composer`, `app` is its application (`composer.App()`), and `routes`
is your existing `*router.Router`. Import `application` from
`arkoren.dev/core/application` and `router` from `arkoren.dev/core/http/router`.
The existing `db` is a `*postgresdatabase.Driver` from
`arkoren.dev/driver/postgres/postgresdatabase`, with its connection startup and
cleanup already wired.

## Configure readiness for traffic

### Check only dependencies required to serve work

If requests cannot succeed without PostgreSQL, include it in readiness. Leave
optional integrations out: an optional shared outage should not remove every
replica from traffic. Register the driver's context-aware connectivity check:

```go
composer.Check("database", db.Ping)
```

Registration does not call `Ping`. After all composition and startup hooks
finish, background checks begin. `Start` does not wait for their results. A nil
callback result means passing; an error or panic means failing. A failed check
makes the instance unready without failing `Start`, changing liveness, or
triggering runner-failure shutdown.

Register checks only during synchronous composition, never from startup hooks,
requests, or later goroutines. Use nonempty, application-wide unique names and
nonnil callbacks; invalid registration panics. See
[`Composer.Check`](https://pkg.go.dev/arkoren.dev/core/application#Composer.Check)
for validation errors. If you write a custom network check, propagate its context
and close any received response body; in-memory dependencies need no connectivity
check.

### Mount probes where the platform can reach them

Probe handlers have no built-in authentication. Use `routes` on a listener with
restricted network access that your platform can still reach. Kubernetes HTTP
probes normally connect to the pod IP: changing the probe port does not make a
loopback-only listener reachable. Keep
[sensitive diagnostics](../observability/exposing-diagnostics.md) on loopback
with port forwarding or behind authentication instead.

During router composition, mount the application-owned handlers:

```go
routes.Get(application.LivePath, app.LiveHandler())       // /livez
routes.Get(application.ReadyPath, app.ReadyHandler())     // /readyz
routes.Get(application.StartupPath, app.StartupHandler()) // /startupz
```

Each returns `200` with `ok\n` or `503` with `unavailable\n`. None runs a
dependency check; readiness reads the latest completed background pass.

- Use `/readyz` to control traffic. It succeeds when the application is live and
  its cached checks are passing and fresh. Without registered checks, readiness
  equals liveness.
- Use `/livez` for restart decisions. It succeeds while the application is
  running, no runner failure is recorded, and the runner group has not finished.
  A database failure alone does not change it.
- Use `/startupz` to protect initialization. It succeeds once startup completes,
  **without waiting for health checks**, and remains successful during and after
  shutdown if its listener remains reachable.

Before startup completes, all three return `503`. Shutdown immediately makes
liveness and readiness return `503`; no verdict keeps a stopped listener
reachable. Never configure readiness as a restarting probe: restarting replicas
does not repair their shared database. Set the platform's probe paths and
shutdown grace period using the [deployment guide](../first-steps/deploying.md#health-probes).

If these routes belong in your OpenAPI document, pass `application.LiveOperation`,
`ReadyOperation`, or `StartupOperation` as the corresponding route's third
argument. These [built-in annotations](../openapi/built-in-handlers.md) describe
responses only; they do not mount routes, enable details, or provide access control.

## Tune timing only when needed

Readiness is a continuing observation, not a one-time startup gate. With checks
registered, it is pending (`503`) until the first pass completes. A passing pass
makes a live application ready; a failing pass makes it unready; a later passing
pass restores readiness without a restart. A cached result stays fresh through
three intervals of age. An older result makes readiness `503`, even if every
cached check passed. Results interrupted by shutdown are discarded.

The first background pass starts immediately after startup. Later passes follow
an interval ticker, not a full interval after the previous pass finishes. For
example, a 400 ms pass on a one-second interval normally starts its next pass one
second after the first started, not 1.4 seconds later. Checks within a pass run
concurrently, but the pass waits for every callback, and background passes never
overlap. A slow pass therefore delays later observations. Polling `/readyz` more
often does not add database calls or make its verdict fresher.

Keep the defaults unless dependency latency or your traffic-removal budget needs
different timing. The interval defaults to five seconds; the timeout defaults to
two fifths of the configured interval (two seconds by default). Change
[`ReadinessInterval`](https://pkg.go.dev/arkoren.dev/core/application#Builder.ReadinessInterval)
and [`ReadinessTimeout`](https://pkg.go.dev/arkoren.dev/core/application#Builder.ReadinessTimeout)
in your existing application construction settings, not on the running `app`.
Zero or negative selects the respective default. Keep the timeout shorter than
the interval.

> [!WARNING]
> A timeout cancels the pass context, not callback code. Checks that ignore
> cancellation prevent completion and delay later passes and shutdown. Keep checks
> lightweight, honor cancellation, and return an error when canceled. Returning
> nil after the deadline is not automatically converted into failure.

## Inspect readiness before running fresh checks

### Read the cached verdict

When traffic stops reaching an instance, first request `/readyz?verbose` on the
restricted probe listener. It returns a JSON report with the same HTTP status as
the plain readiness probe and makes no new dependency calls. Check names and
passing/failing statuses show which dependency failed; the report also explains
when the application is not live, has no completed pass, or has a stale result.
A stale report can still show passing checks, so inspect the overall verdict too.

Plain and verbose probe responses both set `Cache-Control: no-store` and
`X-Content-Type-Options: nosniff`.

Failed checks report `reason withheld` by default. Enable
[`HealthDetails(true)`](https://pkg.go.dev/arkoren.dev/core/application#Builder.HealthDetails)
during construction only where access is restricted to authorized operators:
errors can expose addresses and connection details. Check names remain visible
with details disabled. Actual errors are logged on failure transitions regardless
of this setting, so restrict access to those logs too.

The same setting governs cached and fresh MCP health reports, `healthy` results,
and check details in fail-fast condition failures. Protect all affected endpoints
before enabling it. It does not redact trusted in-process results from `Checks`,
`RunChecks`, or `CheckHealth`, or the errors passed to event listeners.

Inside your process, `app.Checks()` returns a copy of the cached results in
registration order without contacting dependencies. It returns nil before the
first completed pass and when no checks exist. The
[`CheckStatus` reference](https://pkg.go.dev/arkoren.dev/core/application#CheckStatus)
defines the observation and transition timestamps. For exact JSON fields, query
parameter behavior, and report edge cases, see
[`ReadyHandler`](https://pkg.go.dev/arkoren.dev/core/application#Application.ReadyHandler).
For remote investigation, use [Diagnosing an
Application](../observability/diagnosing-an-application.md).

### Run a fresh check for an admin command or test

If you need to know whether PostgreSQL is reachable now, rather than what the
traffic probe last observed, run fresh checks. With your existing started `app`
and a caller context `ctx` carrying a deadline:

```go
err := app.CheckHealth(ctx)
```

A nil error means all registered checks passed; a nonnil error joins the failed
checks in registration order, prefixed with `health` and each check name. Treat
that error as the admin command's failed dependency verdict, not as an application
startup error. If you need individual passing and failing results instead, use
[`app.RunChecks(ctx)`](https://pkg.go.dev/arkoren.dev/core/application#Application.RunChecks).

Both methods run checks concurrently with the configured readiness timeout in
addition to the caller's deadline, and both still wait for every callback to
return. They do not start dependencies, update cached readiness, or publish
health-transition events. Calling before composition cannot see checks that
composition has not registered yet. A fresh success therefore does not itself
restore traffic: the next successful background pass does.

Test callbacks directly for their own behavior. To verify wiring, compose and
start the application before running fresh checks, and arrange shutdown even if
an assertion fails. To verify cached readiness transitions instead, use
`testing/synctest`, not fixed sleeps; fresh-check assertions do not test the cache.

Background logs identify failure and recovery transitions rather than repeating
every verdict. If you need alerts, the corresponding
[`CheckFailed` and readiness events](https://pkg.go.dev/arkoren.dev/core/application#CheckFailed)
describe completed background-pass transitions, not every cause of `503`:
staleness, lifecycle changes, and fresh checks do not publish them. Send error
details only to authorized destinations. Observer errors are logged without
changing lifecycle success; use
[lifecycle observation](lifecycle-and-ownership.md#observe-outcomes-without-controlling-them)
when choosing observers rather than hooks.
