---
title: Exposing Diagnostics
description: Attach bounded capture before startup, select owner tools, and register them on a protected MCP endpoint.
order: 80
---

# Exposing Diagnostics

## Introduction

To let operators inspect a running service, publish selected state as ordinary
[MCP tools](../mcp/overview.md). Start with an existing Arkoren application and
its real router or scheduler, not new empty instances. This page adds diagnostic
definitions to your existing composition; [MCP server setup](../mcp/first-server.md)
owns installation, transport, and client verification.

Use `application` from `arkoren.dev/core/application` and `mcp` from `arkoren.dev/core/mcp`.
Here `composer` is the `*application.Composer` passed to your composition function,
`public` is its
`*router.Router` (`arkoren.dev/core/http/router`), and `scheduler` is its
`*schedule.Scheduler` (`arkoren.dev/core/service/schedule`).

These tools inspect the running service; they do not expose its REST routes as
MCP operations. To publish HTTP request and response contracts, use
[the OpenAPI overview](../openapi/overview.md) instead. Its JSON and browser UI routes
are separate opt-in endpoints and need their own exposure policy.

## Attaching capture before startup

Inside the composition function passed to `app.Start` or `Run`, call
`composer.MCPTools` once. Keep the selection and endpoint
construction in that same function:

```go
logger := composer.App().Logger()
telemetry := composer.App().Telemetry()

tools, err := composer.MCPTools("app")
composer.Require(err)
```

`composer.Require` aborts composition on error before `tools` is used. The factory
returns native `[]mcp.Tool`, not an HTTP handler. It attaches telemetry observation
and event capture to the existing application. The application owns capture
lifecycle; no endpoint is mounted by this call.

Attaching capture does not start a primary telemetry driver or configure an
exporter. Without `Builder.Telemetry`, the application enables only local
telemetry recording after composition succeeds, before `Starting` listeners and
startup callbacks run. With an explicitly configured telemetry handle, the
application starts that handle at the same lifecycle point and capture observes
its trace IDs without replacing the backend. For exporter configuration, use
[Telemetry](telemetry.md).

Application spans, metrics, and captured logs remain inactive during composition;
event metadata can be captured as soon as the event listener is attached. A
logger or telemetry handle obtained before this call still reaches recording
once startup enables it. Separately, a valid span context can add IDs to local
`InfoContext` output even before startup; correlation alone does not enable
recording. This call does not change process-wide logging or telemetry defaults.

The prefix must be nonempty and names every returned tool directly: `"app"`
produces `app.info`, `app.logs`, `app.traces`, and the other application diagnostics.
The same prefix is their CLI group, so logs are available as `app:logs`.

Call once per application: each call attaches its own capture. Calling after
composition returns `application.ErrInvalidPhase`. Declaration errors can be returned after
capture attachment without rollback, so do not retry construction blindly.

If the default retention does not fit your memory budget, replace that call with
a construction callback; do not add a second attachment:

```go
tools, err := composer.MCPTools("app", func(tools *application.MCPToolsBuilder) {
	tools.Limits(application.CaptureLimits{Spans: 2000, Logs: 2000})
})
composer.Require(err)
```

Zero limit fields select 2000 finished spans, 5000 logs, 1000 distinct label
combinations per metric instrument, and 500 event records. Negative fields disable
that signal's retention; disabling events omits the events tool. Event records
contain names, payload types, and trace IDs, never payload values. Existing
configured memory telemetry is reused with its original capacities: these limits
do not resize it. See the
[inventory's retention contract](diagnostic-tools.md#retention-and-disclosure).

## Selecting owners and tools

Add definitions from the router and scheduler already serving your application:

```go
tools = append(tools, public.MCPTools("public")...)
tools = append(tools, scheduler.MCPTools("jobs")...)
```

These factories do not start owners or listeners. Check each returned tool's
`Err()` before registering it. Use distinct prefixes for multiple instances;
the [owner naming table](diagnostic-tools.md#owners-and-prefixes) defines protocol
names and CLI groups.

The application chooses the subset appropriate for this endpoint. For a
read-only endpoint, filter the combined definitions before registration:

```go
var selected []mcp.Tool
for _, tool := range tools {
	if tool.Describe().Annotations.ReadOnly {
		selected = append(selected, tool)
	}
}
```

The scheduler's `run` tool is excluded. This is a composition choice based on
trusted owner definitions, not runtime authorization derived from annotations.
For caller-dependent capabilities, apply `tool.WithAvailability(policy)` to
copies before registration. Availability controls discovery and whether the
caller can invoke the tool; it does not authorize access to a particular
resource. Keep those Gate checks in shared typed application handlers so they
apply across transports. See [tool availability and authorization](../mcp/availability-and-authorization.md).

> [!WARNING]
> Manual scheduler runs execute application code and bypass driver-owned overlap
> guards. Omit `run` unless operators need it, authorize it explicitly, and never
> automatically retry it after a lost response.

## Registering on the protected endpoint

In the existing `mcpserver.New` callback, register only `selected` and use the
application's telemetry and logger:

```go
listener.Tools(selected...)
listener.Telemetry(telemetry)
listener.Logger(logger)
```

`listener` is the existing `*mcpserver.Builder` from
`arkoren.dev/driver/mcp/mcpserver`. Mount the resulting endpoint at `/mcp` on your
admin router. Keep the listener private, authenticate and admit requests before
the mount, and configure Host/Origin checks and TLS as described in
[Securing MCP](../mcp/securing-mcp.md). Definitions do not
install authentication or create a special server-side inspection token.

Diagnostic reads expose source paths, handler names, application log text,
telemetry attributes, and possibly approved health error details. Treat even a
read-only endpoint as privileged. A loopback listener plus an authenticated
tunnel is preferable to a publicly reachable admin port; a tunnel does not
replace endpoint authentication.

`HealthDetails(true)` affects both verbose readiness and MCP health diagnostics,
including check details in fail-fast failures. Keep it disabled unless all those
surfaces are operator-protected; it is not a per-endpoint permission.

## Verifying the published surface

Start the application using its existing lifecycle. With the
[CLI connected](../cli/connecting.md) using an already-issued credential, inspect
the catalog and make one read:

```sh
arkoren tools --json
arkoren app:info
arkoren public:routes
```

These names assume the `app` application prefix and `public` router prefix
above. Confirm only the intended tools appear, identity matches the process,
and routes match the live router. If a tool is absent, check selection and caller
availability before changing names. Continue with [Diagnosing an
Application](diagnosing-an-application.md).

To add application-specific tools, use [MCP tool declarations and CLI
metadata](../mcp/tools.md), not a separate inspector extension system. For the
factory settings, see the [application API](https://pkg.go.dev/arkoren.dev/core/application).
