---
title: Capture Setup
description: Attach bounded application capture and publish selected diagnostics on a protected MCP endpoint.
order: 10
summary:
  - Attach application-owned capture before startup and choose bounded retention.
  - Select tools from existing owners without implicitly starting or exposing them.
  - Protect diagnostic access and verify the published surface with an operator credential.
---

# Capture Setup

Diagnostic capture keeps recent application evidence in memory so an authorized
operator can inspect a running process. Attach it during application composition,
then publish only the owner-provided tools your operators need. Capture does not
mount an endpoint, install authentication, or turn REST routes into MCP tools.

Start with your existing [application composition](../application/composition.md)
and [MCP server](../mcp/first-server.md). Transport construction and client setup
stay there; exporter configuration stays in [Telemetry](../observability/telemetry.md).

## Attach Capture

Inside the composition callback passed to `app.Start` or `app.Run`, obtain the
application handles you will use and attach capture once. Here `composer` is the
callback's `*application.Composer`, with `application` imported from
`arkoren.dev/core/application`:

```go
logger := composer.App().Logger()
telemetry := composer.App().Telemetry()

tools, err := composer.MCPTools("app")
composer.Require(err)
```

The result is `[]mcp.Tool`, not a handler. `composer.Require` stops composition
on error before those definitions are used. The nonempty prefix `app` names the
tools directly: `app.info`, `app.logs`, and `app.traces`, for example. Their CLI
group is also `app`.

Application span, metric, and log recording becomes active after successful
composition and before `Starting` listeners and startup callbacks. Handles
obtained earlier still reach that recording. Event metadata capture begins when
the listener is attached, so it can also observe events during composition.
A valid span context can supply log correlation IDs before startup without
enabling capture recording.

If you configured application telemetry, capture observes that backend's trace
IDs without replacing it. Otherwise the application enables local recording at
startup. Attaching capture does not itself start the primary telemetry driver,
configure an exporter, or change process-wide logging and telemetry defaults.
The application manages capture with its lifecycle.

Call the factory once per application. Every call attaches another capture;
declaration errors can occur after attachment without rollback. Do not retry
blindly. Calls outside synchronous composition return
`application.ErrInvalidPhase`; an empty prefix is rejected before attachment.

## Bound Storage

New capture retains up to 2000 finished spans, 5000 logs, 1000 distinct label
combinations per metric instrument, and 500 event records. These are count
limits, not a byte budget: application log text and attributes still affect
memory use. Capture is process-local evidence, not a durable audit log.

If you need a smaller log window, replace the preceding attachment with this
alternative; do not call both:

```go
tools, err := composer.MCPTools("app", func(tools *application.MCPToolsBuilder) {
	tools.Limits(application.CaptureLimits{Logs: 1000})
})
composer.Require(err)
```

Zero fields select the defaults. Negative fields disable that signal's
retention; a negative `Events` also omits the events tool. Other diagnostic tools
remain available even with retention disabled. If the application already uses
memory telemetry, capture reuses its capacities rather than resizing it.

Events retain names, payload types, and trace IDs, never payload values. Spans
appear only after they finish. Eviction, reset, and process replacement lose
history; raising a limit later cannot recover it. The [retention contract](diagnostic-tools.md#capture-limits)
also describes standalone stores. Review [Telemetry Privacy](../observability/telemetry-privacy.md)
before adding application-specific messages or attributes.

## Select Owners

Use the router and scheduler that already serve your application. Here `public`
is your `*router.Router` from `arkoren.dev/core/http/router`, and `scheduler` is
your `*schedule.Scheduler` from `arkoren.dev/core/service/schedule`:

```go
tools = append(tools, public.MCPTools("public")...)
tools = append(tools, scheduler.MCPTools("jobs")...)
```

These factories neither start their owners nor register listeners. Distinct
prefixes avoid name collisions between instances. The [owner prefixes](diagnostic-tools.md#owner-prefixes)
reference gives the exact protocol names and default CLI groups.

For an inspection-only endpoint, select read-only definitions before registering
them. Import `mcp` from `arkoren.dev/core/mcp`; validate every definition so an
invalid declaration cannot disappear during filtering:

```go
var selected []mcp.Tool
for _, tool := range tools {
	composer.Require(tool.Err())
	if tool.Describe().Annotations.ReadOnly {
		selected = append(selected, tool)
	}
}
```

This excludes the scheduler's `run` action. The selection trusts these shipped
owner definitions; annotations themselves are not authorization. Even a
read-only health tool can execute dependency checks when called with `fresh`.

If different callers need different capabilities, apply
`tool.WithAvailability(policy)` to copies before registration. The application
supplies that policy from its authenticated operator identity. Availability
controls discovery and invocation, not access to an individual resource. Keep
resource checks in shared typed application handlers; see [Availability and
Authorization](../mcp/availability-and-authorization.md).

> [!WARNING]
> Manual scheduler runs execute application code and bypass driver-owned overlap
> guards. Leave `run` unregistered unless you explicitly authorize that capability
> and establish overlap and duplicate-effect safety. Never retry it automatically
> after a lost response.

## Protect Registration

Protect the endpoint before making the tools reachable. Diagnostic reads reveal
source paths, handler identities, log text, telemetry attributes, and possibly
approved health error details. Restrict access to authorized operators and
protect any saved output as well as the live endpoint.

In your existing `mcpserver.New` callback, register `selected` with the shared
application handles obtained above. Here `listener` is the
`*mcpserver.Builder` from `arkoren.dev/driver/mcp/mcpserver`:

```go
listener.Tools(selected...)
listener.Logger(logger)
listener.Telemetry(telemetry)
```

Keep the admin listener private, authenticate and admit requests before the
`/mcp` mount, and apply the Host/Origin and TLS controls in
[Securing MCP](../mcp/securing-mcp.md). A loopback listener with an authenticated
tunnel limits reachability, but the tunnel does not replace endpoint
authentication. Tool construction creates no inspection token or access policy.

Keep application `HealthDetails` disabled unless every affected surface is
operator-protected. Enabling it exposes check error text through MCP health,
fail-fast details, and verbose HTTP readiness; it is not a per-endpoint permission.

## Verify Exposure

After starting the application, use an [authenticated CLI connection](../cli/connecting.md)
to inspect the published catalog:

```sh
arkoren tools --json
```

Confirm that only the intended tools appear for that operator. Then check the
process identity and the existing router:

```sh
arkoren app:info
arkoren public:routes
```

The identity should match the intended process, and the routes should match the
live router. If a tool is absent, check registration, selection, and caller
availability before changing names. Continue with [Application Diagnosis](application-diagnosis.md)
to investigate a request using retained evidence.

For factory settings, see the [application API](https://pkg.go.dev/arkoren.dev/core/application).
