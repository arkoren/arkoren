---
title: Overview
description: Define typed application operations as SDK-independent MCP tools while retaining application-owned policy and serving.
order: 10
summary:
  - Understand how MCP clients discover and call typed application operations.
  - Separate tool definitions from HTTP serving and business authorization.
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

`arkoren.dev/core/mcp` defines the last step: immutable tools that do not depend
on a protocol SDK. Serving them is a separate task owned by the
[MCP Server ark](../mcp-server/first-server.md), which registers selected
definitions with the official Go MCP SDK and returns an endpoint whose route your
application registers on its router.

Defining tools does not require a model API key or a model conversation in your
Go process. If your application needs to run the model itself, use
[Running Agents](../llm/running-agents.md) instead. MCP tools, LLM agents, and
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
output checks, and focused tests. Its fragments keep error results
visible while omitting routine propagation; complete functions and programs
handle their errors.

## Core Responsibilities

A tool definition owns typed argument decoding, input validation, and an
optional availability policy that runs before decoding. It also distinguishes
errors safe to show a client (`mcp.ToolError`, `mcp.ToolFailure`) from internal
ones. Your application keeps authentication, resource authorization, and
business effects.

A definition becomes remotely accessible only when you explicitly register it
with the MCP Server ark. Follow its [First Server](../mcp-server/first-server.md)
walkthrough for a loopback server and a real client call, then
[Serving Tools](../mcp-server/serving.md) and [Securing MCP](../mcp-server/securing.md)
before exposing private operations. Use
[Capability Access](availability-and-authorization.md) to distinguish offering
an operation from authorizing its selected records.

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
require the [native SDK boundary](../mcp-server/serving.md#native-extensions); they do not gain
managed protections automatically.

For declarations, see the [core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp).
