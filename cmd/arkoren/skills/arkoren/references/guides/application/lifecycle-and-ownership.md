---
title: Lifecycle and Ownership
description: Start dependencies in order, supervise running work, and release resources after failures or shutdown.
order: 30
summary:
  - Register releases immediately and preserve reverse-registration rollback regardless of startup progress.
  - Supervise cooperative runners and register resources acquired during startup safely.
  - Budget runner waiting, resource cleanup, and telemetry finalization as separate shutdown phases.
  - Separate host-controlled lifecycle calls from outcome observers that cannot change success.
---

# Lifecycle and Ownership

Your HTTP server must not accept requests before its database is usable or keep
using that database after it closes. The application coordinates this order, but
your composition code chooses the owner of every resource. Start from the
[typed dependencies](composition.md) you have constructed; register each release
where ownership begins, not where startup happens to succeed.

## Register Resource Ownership

Assume `connection` is an unstarted root `*postgresdatabase.Driver` from
`arkoren.dev/driver/postgres/postgresdatabase`, and `listener` is an unstarted
`*server.Server` from `arkoren.dev/core/http/server` whose handlers use that
database. This application is their only lifecycle owner. In composition,
register the database first and the server second:

```go
composer.Cleanup(connection.Stop)
composer.Startup(connection.Start)

composer.Cleanup(listener.Stop)
composer.Startup(listener.Start)
composer.Run(listener.Serve)
```

All composition finishes before startup. The database verifies connectivity,
then the server binds its listener; serving launches only after both startup
callbacks succeed. Cleanup reverses its own registration order: the server
stops before the database. Consumers borrow the facade or driver and register
no second cleanup. A facade constructor does not register driver lifecycle.

Cleanup becomes active immediately, independently of startup progress. If a later
constructor fails, both registered releases still run even though neither startup
ran. If database startup fails, server cleanup still runs before database cleanup.
Every release must therefore tolerate a never-started or partially started
component. Cleanup is not paired positionally with startup hooks.

Use `Cleanup` for `func(context.Context) error` and `Defer` for an infallible
`func()`. Both share the same once-only cleanup stack. Composition's
[`MustDefer`](composition.md#own-subscriptions) combines acquisition with owning
an infallible release. Unlike Go's `defer`, these registrations survive the
composition function's return.

Follow the resource's [driver ownership contract](../drivers/choosing-a-driver.md),
especially when wrapping a native client: a wrapper does not necessarily borrow
it. If a container also manages lifecycle, choose it or the application as owner,
not both. Configured application telemetry already has an owner; do not register
another start or stop for its handle.

## Supervise Running Work

Use application `Run` for an ordinary service. With your service-lifetime context
`ctx` and existing composition callback `compose`, import
`arkoren.dev/core/application`:

```go
app := application.New()
err := app.Run(ctx, compose)
```

`Run` composes, starts, waits for the runner group or caller cancellation, then
shuts down. Handle `err` at the executable boundary. Caller cancellation alone
after successful startup is a normal stop; startup, runner, and shutdown failures
are returned.

Each `composer.Run` callback launches on its own goroutine after startup. Use it
for serving or a consumer loop instead of launching unmanaged goroutines. The
runner must return when its supplied context ends. Handle transient errors inside
the runner when they should not stop the service.

An unexpected nonnil runner error or panic records the first failure and cancels
siblings. Results returned after cancellation count as normal stops. A nil return
does not cancel siblings: application `Run` waits for the entire group, including
the background health loop when checks exist. With no runners or checks, it waits
for caller cancellation or shutdown.

Composition and startup share the startup context's values, deadline, and
cancellation. Runner contexts preserve values but detach that deadline and
cancellation; shutdown or runner failure cancels them. Do not capture
`composer.Context()` for running work. Synchronous composition and startup must
also cooperate with cancellation: a deadline cannot interrupt their code.

## Acquire During Startup

Some resources cannot be acquired until earlier dependencies have started.
`Cleanup` and `Defer` may register releases synchronously inside an executing
startup callback. Register the release immediately after successful acquisition,
before further initialization can fail.

For example, your application-owned
`subscribe func(context.Context) (func(), error)` acquires a subscription and
returns a nonnil release on success. Its implementation cleans up partial results
on failure. `initialize func(context.Context) error` performs the remaining setup:

```go
composer.Startup(func(ctx context.Context) error {
	release, err := subscribe(ctx)
	if err != nil {
		return err
	}
	composer.Defer(release)

	return initialize(ctx)
})
```

If initialization fails, rollback still releases the subscription. The new release
joins the stack at the time of acquisition: it runs before releases registered
earlier during composition, regardless of startup-hook position. Use `Cleanup`
instead if release takes a context or can fail.

Return ordinary startup errors. `Must`, `MustDefer`, `Require`, and child
composition remain composition-only. A startup callback cannot add startup hooks,
runners, checks, or diagnostic tools. Do not retain the composer or register from
spawned goroutines; permission lasts only for the executing callback.

## Handle Failed Startup

A construction failure through `Must`, `MustDefer`, or `Require` skips all later
composition and startup. A startup error or detected cancellation skips later
startup callbacks and launches no runners. Both paths release every registered
resource and finalize telemetry before returning. A failed application cannot
start again; create a new application for another attempt.

Startup, runner, and cleanup panics become errors with stack traces. An unrelated
composition panic remains a programmer failure: cleanup and telemetry finalization
are attempted, then the exact original value is re-panicked. Secondary cleanup
errors do not replace it.

Use `errors.Is` and `errors.As` on returned errors because cleanup and telemetry
failures can be joined with the original cause. The application already logs
lifecycle failures before telemetry finalization. Rollback logs the original cause
with cleanup errors; normal shutdown logs cleanup errors and runner-wait timeouts.
Use the returned error for exit status or recovery rather than duplicate logging
after `Run`, when telemetry has stopped. Failures before telemetry starts reach
the configured log handler only; telemetry-finalization failures go directly to
that handler rather than through the failed exporter.

## Budget Shutdown

Shutdown immediately makes liveness and readiness unavailable. It emits `Stopping`,
cancels runners, and waits within the shutdown context's budget before cleanup.
Every release is attempted even after another fails, panics, or exhausts the shared
cleanup context. Errors are joined; telemetry finalizes last, after the final
lifecycle log and event.

> [!WARNING]
> Cancellation does not forcibly stop callback code. If a runner ignores its
> context, cleanup can overlap that runner after the wait times out. Make runners
> cooperate and releases safe for that possibility. Cleanup or telemetry callbacks
> that ignore cancellation can also prevent shutdown from returning.

Application `Run` uses a 30-second shutdown budget by default. If your drain needs
more time, configure it when constructing the application (`time` is the standard
library package):

```go
app := application.New(func(app *application.Builder) {
	app.ShutdownTimeout(45 * time.Second)
})
```

Zero or negative selects the default. This is not a hard total-duration limit.
Cleanup receives a context detached from caller cancellation but preserving
values, with the remaining caller budget and a minimum of ten seconds. If runner
waiting exhausts the budget, cleanup still gets a fresh ten seconds. Telemetry
finalization independently receives the remaining caller budget with at least
a fresh ten seconds. Rollback uses the same minimum budgets. Account for these
extra phases in your platform's
[shutdown budget](../first-steps/deploying.md#shutdown-budgets).

## Separate Host Control

Use `Start`, `Wait`, and `Shutdown` separately only when another host owns service
lifetime or startup needs a separate deadline. `Start` runs once. A failed `Start`
already rolls back; successful startup always requires eventual `Shutdown`, even
after runner failure. Canceling the successful startup context does not stop
runners.

`Wait` returns the first runner failure, not its waiting context's error. It can
return nil when that context ends while runners remain active. Give `Shutdown`
a live, bounded context, not the context that just ended. Concurrent shutdown
callers wait for the single cleanup pass and receive its result unless their own
waiting context ends. During startup, shutdown cancels startup and waits for its
owner to roll back; timing out that wait does not take rollback ownership away.
Shutdown after failed startup returns nil because rollback is already complete.

Do not call `Shutdown` synchronously from composition, startup, a runner, or
cleanup: it can wait on its own caller. See the
[`Shutdown` contract](https://pkg.go.dev/arkoren.dev/core/application#Application.Shutdown)
for exact concurrent-call behavior.

## Observe Outcomes

Use lifecycle callbacks when failure must affect startup or shutdown. Use event
listeners for observation: listener errors and panics are logged without changing
the lifecycle result. Obtain `events := composer.App().Events()` at the top of
composition when registering observers there, and pass the specific handle to
other components rather than passing the application.

`Starting` follows successful composition and telemetry startup, before user
startup hooks. Listeners registered during composition can log through active
telemetry then. Composition or telemetry startup failures can emit `Failed`
without `Starting`; there is no built-in `app starting` log. `Started` means
runners have launched, not that the first health pass succeeded.

`Stopping` precedes runner cancellation and cleanup. `Stopped` follows waiting
and cleanup but precedes telemetry finalization; its listeners see `StateStopping`
and can write final telemetry. Only after finalization is `StateStopped` published.
`Stopped` does not mean cleanup succeeded: inspect the returned error.
`Failed` carries the original startup or runner cause, not added cleanup or
telemetry errors.

Keep a subscription needed for `Stopped` or rollback `Failed` outside the
application cleanup stack. In the function calling `Run`, assume `app`, `ctx`,
and `compose` already exist. Import `event` from `arkoren.dev/core/event`:

```go
logger := app.Logger()

unsubscribe, err := app.Events().Listen(application.Stopped, func(ctx context.Context, _ event.Void) error {
	logger.InfoContext(ctx, "inventory cleanup completed")

	return nil
})
if err != nil {
	return err
}
defer unsubscribe()
```

Now run the application in that same function:

```go
err = app.Run(ctx, compose)
```

Handle or return `err`; the deferred unsubscribe runs after `Run`, not during
application cleanup. Listeners are synchronous and must return: error isolation
cannot interrupt a blocked observer. Health events have a separate
[background-transition contract](health-and-readiness.md#observe-transitions);
see the [event bus guide](../events/overview.md) for listener behavior.

For lifecycle settings and runtime methods, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application).
