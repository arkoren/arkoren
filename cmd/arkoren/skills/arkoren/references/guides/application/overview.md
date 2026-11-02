---
title: Application Overview
nav: Overview
description: Decide whether your service needs an application coordinator and understand its boundaries.
order: 10
summary:
  - Choose the optional coordinator without giving up standalone packages or explicit dependencies.
  - Understand composition, startup, supervised work, and reverse-order cleanup.
  - Separate public welcome content from protected operator information and shared telemetry.
---

# Application Overview

An HTTP service often needs its database before accepting requests and must keep
that database open until requests finish. The `application` package coordinates
these dependencies across startup, running work, and shutdown. It does not
construct dependencies for you or make them available through a lookup container.

## Choose Coordination

Use an application when several components need one startup order, one owner for
cleanup, or shared supervision. If your existing host already manages those
responsibilities, keep using it: Arkoren's router, server, and service packages
work independently. You can [run an HTTP server directly](../http-networking/server.md)
or [adopt individual packages](../first-steps/installation.md#existing-applications)
without adding an application.

To create an idle coordinator, import `arkoren.dev/core/application`:

```go
app := application.New(func(app *application.Builder) {
	app.Name("inventory")
})
```

This resolves construction settings but starts no resources or listeners.
Choose [component settings](../first-steps/configuration.md#component-construction)
when constructing the application; its builder is not a runtime control handle.

## Understand Phases

A **composer** is the temporary scope passed to your composition callback.
Composition constructs concrete dependencies, passes typed values to their
consumers, and registers work. Every composition callback finishes synchronously,
in argument order, before any startup callback runs.

Inside composition, `Startup` registers initialization, `Run` registers supervised
running work, and `Cleanup` registers resource release. These calls register
callbacks; they do not execute them. Startup callbacks run in registration order.
Only after all succeed do runners and background health checks launch. A
constructed dependency is therefore not necessarily connected or ready to use.

Application `Run` combines startup, waiting for runners or caller cancellation,
and shutdown. Shutdown cancels runners, waits within its budget, then releases
resources in reverse cleanup-registration order. Register cleanup immediately
when ownership begins: it also runs if later construction or startup fails,
including for components whose startup never ran.

[Composition](composition.md) shows how to construct and share typed values.
[Lifecycle and Ownership](lifecycle-and-ownership.md) explains rollback,
supervision, and cooperative shutdown. A successful startup does not establish
continuing availability; [Health and Readiness](health-and-readiness.md) keeps
that separate from lifecycle state.

## Share Handles

Services that need logging, telemetry, or events receive those specific handles,
not the application. At the top of a composition callback, bind only the handles
it uses, then pass them to constructors. The handles are stable: a logger obtained
before capture is attached still reaches that capture after startup. Keep the
composer out of request handlers and background goroutines. Its context belongs
to startup; runners use their supplied context instead.

The logger adds trace and span IDs from a valid context even before startup;
use context-aware methods such as `InfoContext`. Correlation does not enable
recording. Application telemetry records nothing without an explicit
[telemetry configuration](../observability/telemetry.md) or
[diagnostic capture](../diagnostics/capture-setup.md). Recording starts after
composition, and telemetry finalizes after resource cleanup and final lifecycle
events. The application owns that lifecycle.

## Choose Handler Exposure

If the service needs a temporary public home page, mount the welcome handler on
your existing `routes *router.Router` (`arkoren.dev/core/http/router`):

```go
routes.Get("/", app.WelcomeHandler(), application.WelcomeOperation)
```

The exact-root route serves self-contained HTML containing the escaped application
name and Arkoren resource links. Choose a name suitable for public display; an
empty name uses a generic heading. It fetches no external assets, needs no
JavaScript, and exposes no runtime, configuration, or build details. Replace it
when you add your own home page; it is not a health probe. For a first request
through a running service, follow the [quickstart](../first-steps/quickstart.md#first-request).

Operator information is different: `InfoHandler` exposes name, version,
environment, lifecycle state, log level, start time, uptime, and binary build
details. Restrict network access or add authentication and authorization before
mounting it. On an existing operator-only router:

```go
operatorRoutes.Get(application.InfoPath, app.InfoHandler(), application.InfoOperation)
```

`app.Info()` returns the same snapshot inside the process. Neither handler is
mounted automatically or controlled by debug mode. Operation annotations document
the registered routes; they do not protect them. Follow
[diagnostic endpoint protection](../diagnostics/capture-setup.md) before remote
exposure, and use dedicated [probes](health-and-readiness.md#mount-probes) for
traffic and restart decisions.

Focused fragments in these guides keep error results visible but omit routine
propagation. Checks that determine ownership or another consequential decision
remain explicit; complete application code must handle its errors.

For construction and runtime contracts, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application).
