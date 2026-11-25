---
title: Serving Tools
description: Register typed tools with the MCP Server ark, add its HTTP route, and understand its transport, availability, telemetry, and native SDK behavior.
order: 20
summary:
  - Register selected tool definitions and add the endpoint route to your router.
  - Understand the stateless Streamable HTTP transport and its limits.
  - See when availability runs for listing and calls, and how tool calls are recorded.
  - Recognize what native SDK additions bypass and test the endpoint over HTTP.
---

# Serving Tools

Tool definitions from [core MCP](../mcp/tools.md) do nothing until you serve
them. The MCP Server ark, `arkoren.dev/ark/mcp/mcpserver`, registers the
definitions you select with the official Go MCP SDK and returns a server whose
`Register` method adds one `POST` route to a router group. Your application owns
the listener, authentication, Origin validation, deadlines, and shutdown. Install it with:

```sh
go get arkoren.dev/ark/mcp
```

Registered tools are **managed tools**: they receive typed input parsing,
validation of encoded output, safe error mapping, panic recovery, and optional
payload-free call telemetry. The endpoint does not discover service methods
automatically.

## Endpoint Registration

Assemble the selected tools at application composition time. With `shipping`
as the `mcp.Tool` defined in the [Tools guide](../mcp/tools.md#input-rules):

```go
endpoint, err := mcpserver.New("shipping", "1.0.0", func(listener *mcpserver.Builder) {
	listener.Tools(shipping)
})
```

Handle `err`, then attach the endpoint to a group at `/mcp`:
`routes.Group("/mcp", endpoint.Register)`. `endpoint.EndpointHandler()` returns
the same handler for a path or method set of your own.
Construction opens no port and rejects invalid declarations, duplicate names,
and unsupported output schemas before registering any tool with the SDK.
`Tools` appends registrations and copies supplied slices. Importing a package or
calling its `MCPTools(prefix)` factory does not expose anything by itself.

Follow the [HTTP route](first-server.md#http-route) for the public local sample,
or [authenticated registration](securing.md#typed-authentication) for private
tools. Authentication, Host/Origin checks, request deadlines, and listener
timeouts remain application responsibilities.

## Lifecycle Ownership

The MCP endpoint has no lifecycle resources or `Start`/`Stop` methods. Retain
your existing HTTP listener's startup, serving runner, and cleanup ownership.
With Arkoren composition, register the listener's `Start`, `Serve`, and `Stop`,
not the endpoint; [HTTP Server](../http-networking/server.md) covers that wiring.
There is no adapter tool-duration setting, and a write timeout alone does not
stop application work.

## Transport Behavior

The adapter uses stateless Streamable HTTP with JSON responses and the SDK's
default 4 MiB request-body limit. It issues no persistent session ID, stores no
events for resumable delivery, and returns 405 for GET/DELETE (the router, since
only `POST` is registered) and 413 for
oversized bodies. It does not serve legacy HTTP+SSE or managed server-initiated
requests.

The module pins Go MCP SDK v1.8.0, supporting protocol versions 2026-07-28,
2025-11-25, 2025-06-18, 2025-03-26, and 2024-11-05. Adapter tests exercise
2026-07-28 and 2025-06-18 clients. Request-cancellation propagation is enabled,
but the SDK applies it only to 2026-07-28 and later: an older client's disconnect
does not necessarily cancel its tool. Outer request context values remain
available to managed handlers.

The SDK's localhost DNS-rebinding protection is enabled. It is not a public
Host allowlist or an Origin policy. The adapter offers no custom body-limit,
OAuth, or execution-timeout setting; configure these at the appropriate
[outer HTTP boundary](securing.md).

## Availability Checks

The adapter evaluates each managed tool's [availability policy](../mcp/availability-and-authorization.md)
during every `tools/list` and again on every call, before argument decoding and
validation. Register one static catalog; do not cache per-user servers to
implement discovery filtering.

Listing returns the complete filtered catalog without pagination or a next
cursor, and rejects a nonempty cursor. A policy error or panic fails the whole
listing with `tool listing failed` rather than returning a partial catalog.
Policy diagnostics stay in local logs, even when marked with `mcp.ToolError`.
Native SDK tools remain visible without managed availability; see
[Native Extensions](#native-extensions).

## Call Telemetry

With an existing telemetry service named `signals`, add
`listener.Telemetry(signals)` inside endpoint construction. Omitted or nil
telemetry follows `telemetry.Default()` per call. Managed tools emit
`mcp call_tool` spans, call counts, and durations in seconds labeled by registered
tool name and success/error outcome. They capture no arguments, output, raw
errors, or model-inference attributes.

Protected error logs remain separate; `listener.Logger(logger)` selects an
existing `*slog.Logger`. Configure [telemetry export](../observability/telemetry.md)
and [diagnostic capture](../diagnostics/capture-setup.md) separately.

## Native Extensions

`endpoint.Server()` exposes the native SDK server for resources, prompts,
native tools, or another SDK transport such as stdio. Arkoren has no typed
resources/prompts facade or managed stdio adapter.

Native tools remain visible in discovery but bypass managed availability,
typed validation, safe-error mapping, recovery, telemetry, and CLI metadata.
Do not replace managed registrations or their list middleware through this
accessor: doing so can bypass guards or leave metadata stale.

## Testing Over HTTP

Direct `Tool.Call` tests in [core](../mcp/tools.md#focused-tests) do not exercise
adapter recovery, redaction, or advertised-output validation. Test those
boundaries with an MCP client against the endpoint handler, as in the
[adapter HTTP tests](https://github.com/arkoren/arkoren/blob/main/ark/mcp/mcpserver/server_test.go),
which use modern and legacy Streamable HTTP clients to check discovery, calls,
identity propagation, and safe errors.

For declarations and settings, see the
[MCP server API](https://pkg.go.dev/arkoren.dev/ark/mcp/mcpserver).
