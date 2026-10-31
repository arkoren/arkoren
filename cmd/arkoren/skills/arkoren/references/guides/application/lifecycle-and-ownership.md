---
title: Application Lifecycle and Ownership
nav: Lifecycle and Ownership
description: Order startup, supervise background work, and release each owned resource on failure or shutdown.
order: 20
---

# Application Lifecycle and Ownership

Start the database before binding the HTTP listener, then stop the server before
closing the database. That order keeps requests from using a dependency that is
not ready or has already closed. Use application `Run` for an ordinary service:
it composes, starts, waits, and shuts down as shown in the
[overview](overview.md#start-dependencies-before-serving-requests).

## Register releases when ownership begins

The overview establishes startup and reverse cleanup order. The important failure
boundary is when cleanup becomes active: register it immediately after acquiring
each resource, before later construction can fail.

Cleanup is active immediately, independent of startup progress. If construction
of the server fails after the database's cleanup was registered, the database
is still released even though its startup never ran. Every registered release
runs once in reverse order on failure or shutdown. A release must tolerate a
never-started or partially started component; cleanup is not paired with
successful startup hooks.

Use `Cleanup` for a context-aware release that can fail, or `Defer` for an
infallible `func()` such as unsubscribing or canceling. `MustDefer` also handles
registration returning `(func(), error)` during composition. All three register
on this same stack; see [subscription ownership](composition.md#own-subscriptions-and-infallible-releases).

Keep one acquiring owner per resource. Consumers borrow the driver or facade;
receiving a value does not transfer ownership or require another cleanup.
If a dependency-injection container also manages lifecycle, choose it or the
application as owner, not both. Follow each
[driver's ownership contract](../drivers/choosing-a-driver.md): some constructors
connect immediately, and a wrapper around a native connection does not always
borrow it.

## Supervise running work

Register a server's `Serve` or a consumer's `Run` with `composer.Run` rather than
launching an unmanaged goroutine. Each runner starts on its own goroutine after
startup and must return when its supplied context ends. Handle transient failures
inside the runner if they should not stop the service.

Composition and startup receive the startup context's values, deadline, and
cancellation. Runner contexts retain those values but detach the startup
deadline and cancellation; shutdown or a runner failure cancels them. Do not
capture `composer.Context()` for running work.

Composition and startup callbacks execute synchronously. Their context deadlines
request cancellation; they cannot interrupt construction or startup code that
ignores its context.

An unexpected non-nil runner error or panic records the first failure and cancels
sibling runners. Results returned after cancellation count as normal stops.
A nil return does not cancel siblings: application `Run` still waits for the
whole group, including the background health loop when checks exist. With no
runners or checks, the application waits for caller cancellation or shutdown.

## Handle failures and allow time for shutdown

Application `Run` returns startup failures after rollback, or joins runner and
shutdown failures after a successful start. Classify returned errors with
`errors.Is` or `errors.As` so added cleanup errors do not hide the original cause.
Caller cancellation alone after successful startup is a normal stop.

The application logs composition, startup, runner, and shutdown failures itself.
Rollback includes cleanup failures in its failure record; normal shutdown reports
cleanup failures and runner-wait timeouts before telemetry finalization. Use the
returned error for exit status or recovery rather than logging it again after
`Run`: telemetry has already stopped. Failures before telemetry starts reach the
configured log handler only. Telemetry finalization failures also go directly to
that handler because the failed exporter cannot reliably report its own failure.

If composition fails through `Must`, `MustDefer`, or `Require`, later composition and all
startup work are skipped. If startup returns an error or detects cancellation,
later startup callbacks are skipped and no runners launch. Both paths release
all registered resources. Return ordinary errors from startup, runners, and
cleanup; the composition-only error helpers are explained in
[Composition](composition.md).

Startup, runner, and cleanup panics become errors with stack traces. An unrelated
composition panic remains a programmer failure: the application attempts cleanup
and telemetry finalization, then re-panics with the exact original value.
Secondary cleanup errors do not replace that panic.

On shutdown, liveness and readiness become unavailable immediately. The
application cancels runners and waits for them within the supplied shutdown
budget, then runs cleanup even if waiting timed out. Every release is attempted
even after another fails, panics, or exhausts the shared cleanup context. Errors
are joined, and configured telemetry finalizes last, after the final lifecycle
log and event.

> [!WARNING]
> Cancellation cannot forcibly stop callback code. If a runner ignores its
> context, cleanup can overlap that runner after the waiting timeout. Make
> runners cooperate and release methods safe for this possibility. A cleanup
> callback that ignores its context can also block shutdown.

Application `Run` uses a 30-second shutdown budget by default.
[`Builder.ShutdownTimeout`](https://pkg.go.dev/arkoren.dev/core/application#Builder.ShutdownTimeout)
changes it; zero or negative selects the default. Cleanup receives a context
detached from caller cancellation, preserving values, with the remaining caller
budget but **at least ten seconds**. If runner waiting exhausts the budget,
cleanup still gets a fresh ten seconds. Telemetry finalization independently
gets the remaining caller budget but at least a fresh ten seconds. Rollback uses
the same minimum budgets. These are cooperative budgets, not hard wall-clock
limits; allow for the extra time in your platform's
[shutdown grace period](../first-steps/deploying.md#graceful-shutdown).

## Acquire during startup only when required

If a resource can only be acquired during startup, register its release
immediately after successful acquisition, before any further initialization.
This also works with ordinary application `Run`: cleanup registration through
`Cleanup` or `Defer` is allowed inside an executing startup callback and remains active
if that callback later fails. Return acquisition errors normally rather than
through `Must`, `MustDefer`, or `Require`. Keep registration synchronous and do not retain the
composer after the callback returns. See
[`Composer.Cleanup`](https://pkg.go.dev/arkoren.dev/core/application#Composer.Cleanup)
for the registration contract.

For example, assume your application-owned `subscribe(ctx)` uses the context for
acquisition and returns a nonnil `func()` release on success. `initialize(ctx)`
performs the remaining startup work. Register the release before that later step:

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

If initialization fails, rollback still releases the subscription. Use `Cleanup`
instead when the release takes a context or can fail. An executing startup
callback cannot add `Startup`, `Run`, or `Check` registrations; register those
during composition, before startup begins.

## Separate phases only when another owner needs control

Use [`Start`, `Wait`, and `Shutdown`](https://pkg.go.dev/arkoren.dev/core/application#Application.Start)
separately when startup needs its own deadline or a host controls service
lifetime. Each application starts only once. A failed `Start` already rolls
back; a successful `Start` requires eventual `Shutdown`, even after runner
failure. Canceling the successful startup context does not stop runners.

`Wait` reports the first runner failure, not its waiting context's error. It can
return nil when its context ends while runners remain active. Give `Shutdown` a
live, bounded context rather than the context that just ended. Do not call it
synchronously from composition, startup, a runner, or cleanup: it can wait on its
own caller. The
[`Shutdown` contract](https://pkg.go.dev/arkoren.dev/core/application#Application.Shutdown)
details concurrent calls and rollback ownership when shutdown interrupts startup.

## Observe outcomes without controlling them

Use lifecycle callbacks when a failure must affect startup or shutdown. For
optional observation, bind `events := composer.App().Events()` at the top of
composition and listen on that handle for `Starting`,
`Started`, `Stopping`, `Stopped`, or `Failed`. Listener errors are logged without
changing the lifecycle result. `Starting` runs after composition and telemetry
startup, before user startup callbacks. Register listeners during composition;
their application logger records reach configured telemetry or local capture. If
composition or telemetry initialization fails, `Failed` can occur without
`Starting`. The application emits no built-in `app starting` log.
`Started` means startup completed, not that the
first health pass succeeded; see [health-transition behavior](health-and-readiness.md#inspect-readiness-before-running-fresh-checks)
and the [event bus guide](../events/overview.md).

`Stopping` runs before runner cancellation and cleanup. `Stopped` runs after
runner waiting and cleanup, but before telemetry finalization: its listeners see
`StateStopping` and can still write final telemetry. Only after finalization does
the application publish `StateStopped`. A `Stopped` notification does not imply
that cleanup succeeded; inspect the returned shutdown error.

Keep a subscription needed for `Stopped` or rollback `Failed` outside the
application's cleanup stack, since cleanup runs before those events. For example,
in the function that calls `Run`, with `ctx`, `app`, and your composition callback
`compose` already available (`event` is `arkoren.dev/core/event`):

```go
logger := app.Logger()

unsubscribe, err := app.Events().Listen(application.Stopped, func(ctx context.Context, _ event.Void) error {
	logger.InfoContext(ctx, "shutdown cleanup finished")

	return nil
})
defer unsubscribe()

err = app.Run(ctx, compose)
```

The enclosing function handles or returns `err`; its deferred unsubscribe runs
after `Run`, not during application cleanup. Listener callbacks are synchronous
and must return; error isolation does not let the application interrupt a blocked
listener.

For lifecycle settings and exact runtime contracts, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application).

As wiring grows, keep each resource's construction and cleanup registration
together. [Composition](composition.md) shows how to extract that code into
typed factories and pass their results to consumers without splitting ownership.
