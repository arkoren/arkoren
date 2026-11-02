---
title: Overview
description: Expose typed application operations as discoverable MCP tools while retaining application-owned policy and HTTP serving.
order: 10
summary:
  - Understand how MCP clients discover and call typed application operations.
  - Separate managed tool protections from HTTP serving and business authorization.
  - Choose the server walkthrough or diagnostic capture path for your application.
---

# Overview

An assistant requesting a shipment quote needs a named operation, an argument
schema, and a result it can interpret. The **Model Context Protocol (MCP)**
provides that connection between an AI application's client and your application.
Your service still owns the calculation, access decisions, and any effects.

## Request Path

The AI application is the **host**. Its MCP client discovers tools and calls
them with JSON arguments; your MCP server dispatches those calls to application
operations:

```text
AI host → MCP client → HTTP endpoint → typed operation
```

`arkoren.dev/core/mcp` defines immutable tools without depending on a protocol
SDK. `arkoren.dev/driver/mcp/mcpserver` registers selected definitions with the
official Go MCP SDK and returns an `http.Handler`. Your application mounts that
handler and owns the listener, authentication, deadlines, and shutdown.

Serving tools does not require a model API key or a model conversation in your
Go process. If your application needs to run the model itself, use
[Running Agents](../llm/running-agents.md) instead. MCP serving, LLM agents, and
the [documentation skill](../agent-skills/working-with-agents.md) are separate tasks.

## Typed Operations

Start with a narrow application operation rather than exposing arbitrary service
methods. For a shipment quote, define its JSON argument and result shapes:

```go
type QuoteRequest struct {
	WeightGrams int `json:"weight_grams"`
}

type Quote struct {
	PriceCents int `json:"price_cents"`
}
```

With `quote` as your existing
`func(context.Context, QuoteRequest) (Quote, error)`, import
`arkoren.dev/core/mcp` and publish its tool definition:

```go
tool := mcp.NewTool("shipping.quote", quote, func(action *mcp.ToolBuilder[QuoteRequest, Quote]) {
	action.Description("Quote domestic shipping in cents for a parcel's weight.")
	action.ReadOnly(true)
	action.OpenWorld(false)
})
```

Clients discover `shipping.quote` with object schemas using `weight_grams` and
`price_cents`. Go types provide inferred schemas; explicit validation adds domain
constraints. Tool names are static protocol identifiers, not per-user routes.
Read-only and other annotations are descriptive hints, not permission checks.

The [Tools guide](tools.md) completes this example with weight validation,
output checks, registration, and focused tests. Its fragments keep error results
visible while omitting routine propagation; complete functions and programs
handle their errors.

## Managed Serving

A definition becomes remotely accessible only when you explicitly register it
with `mcpserver.Builder.Tools` and mount the endpoint. These **managed tools**
receive typed input parsing, validation of encoded output, safe error mapping,
panic recovery, and optional payload-free call telemetry.

The endpoint uses stateless Streamable HTTP, not a client-launched stdio command.
It does not own an HTTP listener or retry handlers. Output validation happens
after execution, so a failed result does not undo business effects.

Follow [First Server](first-server.md) for a complete loopback server and a real
client discovery/call path. Before serving private operations or remote traffic,
configure [Securing MCP](securing-mcp.md). Then use
[Capability Access](availability-and-authorization.md) to distinguish offering an
operation from authorizing its selected records.

## Diagnostic Tools

For inspecting an existing application, use
[Capture Setup](../diagnostics/capture-setup.md) rather than writing domain tools.
Diagnostic factories return ordinary tool definitions; importing a package
exposes nothing. Select registrations explicitly, attach application capture
before startup, and follow the diagnostic privacy constraints.

Use [Application Diagnosis](../diagnostics/application-diagnosis.md) for the
inspection workflow and [Diagnostic Tools](../diagnostics/diagnostic-tools.md)
for individual operation names and result boundaries.

The [Arkoren CLI](../cli/overview.md) is an optional MCP client, not a second
catalog or protocol. MCP resources, prompts, native tools, and other transports
require the [native SDK boundary](tools.md#native-extensions); they do not gain
managed protections automatically.

For declarations and adapter settings, see the [core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp)
and [MCP server API](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver).
