---
title: Application Overview
nav: Overview
description: Decide when to use the optional application coordinator and understand composition, startup, and shutdown.
order: 10
---

# Application Overview

If your HTTP handlers need PostgreSQL, the database must be available before the
server accepts requests and remain available until those requests finish.
`application` coordinates that startup and shutdown order, along with background
workers and readiness checks. The package is optional: if you only need an HTTP
server, you can
[run it directly](../http-networking/server.md); Arkoren's standalone packages
do not require an application.

An **Application** is the runtime coordinator. A **Composer** is the temporary
scope passed to a synchronous composition function: the code that connects your
concrete dependencies and registers their lifecycle callbacks. You construct and
pass dependencies explicitly; the composer does not discover them.

## Start dependencies before serving requests

Import `application` from `arkoren.dev/core/application`. Here `ctx` is your
service-lifetime context, `db` is an existing root
[`*postgresdatabase.Driver`](https://pkg.go.dev/arkoren.dev/driver/postgres/postgresdatabase#Driver),
and `srv` is an existing
[`*server.Server`](https://pkg.go.dev/arkoren.dev/core/http/server#Server)
whose handlers use that database. This application owns their lifecycle; neither
resource has started, and no other owner closes them.

```go
app := application.New()
err := app.Run(ctx, func(composer *application.Composer) {
	composer.Cleanup(db.Stop)
	composer.Startup(db.Start)
	composer.Cleanup(srv.Stop)
	composer.Startup(srv.Start)
	composer.Run(srv.Serve)
})
```

`Startup` registers initialization, `Run` registers supervised running work, and
`Cleanup` registers resource release. Registration does not execute the callback.
For releases with the signature `func()`, such as unsubscribing or canceling,
use `Defer`; use `MustDefer` when acquisition returns `(func(), error)`. These
use the same cleanup stack, not Go function-return timing. See
[infallible releases](composition.md#own-subscriptions-and-infallible-releases).
The database startup verifies connectivity, then the server binds its listener.
Only after startup succeeds does `Serve` begin handling requests.
Passing a driver to a facade does not register or run its lifecycle: the explicit
database startup and cleanup registrations above remain necessary.

Application `Run` waits for the runner group to finish or `ctx` to end, then
shuts down. It cancels running work and waits within its shutdown budget before
cleanup: the server stops before the database because cleanup reverses
registration order. Register each release as soon as you own its resource so it
also runs if later startup
fails. Releases must tolerate a never-started or partially started component.

Handle `err` at your executable boundary. Caller cancellation alone after
successful startup is a normal stop; startup, runner, and shutdown failures are
returned. Focused snippets in these guides omit routine error propagation but
retain checks that determine whether ownership or another consequential action
can proceed.

## Keep construction separate from startup

All composition functions finish, synchronously in argument order, before any
startup callback runs. You can pass the constructed database to handlers during
composition, but defer queries that require connectivity until startup or running
work. Startup callbacks then run in registration order; runners and background
[health monitoring](health-and-readiness.md) wait for every callback to succeed.
Configured telemetry starts before those callbacks and finalizes after cleanup.

Keep the composer in composition code, not request handlers or goroutines.
`composer.Context()` is the startup context; a runner must use the context it
receives instead. When services need shared logging, telemetry, or events, obtain
those handles through `composer.App()` and pass the specific handles, not the
application.

The logger can add trace and span IDs from a valid context even before startup;
use context-aware methods such as `InfoContext`. That correlation does not enable
recording. Application telemetry remains inactive during composition and, without
explicit telemetry configuration or [diagnostic capture](../observability/exposing-diagnostics.md),
records nothing after startup either. See [Telemetry](../observability/telemetry.md)
for instrumentation and exporter setup.

## Choose a public welcome page or operator information

If your service needs a landing page before you build its own UI, register
`app.WelcomeHandler()` on your existing `*router.Router` during composition:

```go
routes.Get("/", app.WelcomeHandler(), application.WelcomeOperation)
```

The exact-root route returns a self-contained HTML page with your configured
application name and links to Arkoren's getting-started guide, documentation,
and GitHub repository. An empty name uses a generic heading. Choose a name
suitable for public display; it is HTML-escaped. The page needs no JavaScript,
external fonts, or network assets, and includes no runtime, configuration, or
build details. It is a landing page, not a health probe. Replace or remove the
registration when you add your own home page.

For operator diagnostics instead, the separate `app.InfoHandler()` returns the
full JSON description: name, version, environment, lifecycle state, log level,
start time, uptime, and binary build information. Restrict its listener or add
authentication and authorization **before** exposing it; it has no built-in
access control. On your existing operator-only router:

```go
operatorRoutes.Get(application.InfoPath, app.InfoHandler(), application.InfoOperation)
```

`app.Info()` returns the same diagnostic snapshot inside your process. Neither
handler is mounted automatically, and neither depends on debug mode. The optional
operation callbacks document only the routes you register; they do not enforce
access policy. See [Exposing Diagnostics](../observability/exposing-diagnostics.md)
for endpoint protection and [Health and Readiness](health-and-readiness.md) for
traffic probes.

For settings and runtime methods, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application).

Continue with [Lifecycle and Ownership](lifecycle-and-ownership.md) to make this
ordering safe when startup fails or shutdown runs out of time.
