---
title: MCP Overview
nav: Overview
description: Understand how typed application operations become discoverable MCP tools, then choose your implementation path.
order: 10
---

# MCP Overview

## Introduction

An assistant looking up an invoice needs to discover the operation, understand
its arguments, and ask your application to execute it. The **Model Context
Protocol (MCP)** standardizes that connection. Your application still decides
who can connect, which operations are available, and which records they may access.

## How the pieces connect

The AI application is the **host**. Its **MCP client** connects to your **MCP
server**, lists tools, and calls them by name with JSON arguments:

```text
AI application → MCP client → MCP server → application services
```

Arkoren separates the application operation from its protocol transport:

- `arkoren.dev/core/mcp` publishes immutable tools from typed Go handlers.
  It has no MCP SDK dependency and starts no server.
- `arkoren.dev/driver/mcp/mcpserver` registers selected tools with the official
  Go MCP SDK and returns an ordinary `http.Handler`.
- Your HTTP service owns listening, authentication, request limits, and shutdown.
  Shared application services own resource authorization and business effects.

You do not need a model-provider API key, a separate MCP daemon, the Arkoren CLI,
or Arkoren's router to serve tools. The host handles its own model conversation.
If your Go application instead needs to call a model and run an agent, start
with [Agents and Tools](../llm/agents-and-tools.md). Arkoren has no outbound MCP
client facade.

## Choose a path

Start with [Your First MCP Server](first-server.md) for module installation,
a loopback endpoint, and a tool call through an MCP client. Then use
[MCP Tools](tools.md) to add validated application behavior and mount it in an
existing service.

Before exposing private data or remote access, follow
[Securing MCP](securing-mcp.md). Once the caller is authenticated,
[Availability and Authorization](availability-and-authorization.md) separates
paid-plan or administrator capabilities from access to individual records.

If your goal is diagnosing an existing application rather than defining domain
operations, use [Exposing Diagnostics](../observability/exposing-diagnostics.md).
Shipped diagnostic factories return ordinary `[]mcp.Tool`; importing a package
exposes nothing. Select and register tools explicitly. Application capture must
attach before startup, and its diagnostic outputs have their own privacy limits.
The [Arkoren CLI](../cli/overview.md) is an optional client, not another protocol.

## Know the managed boundary

Tools registered with `mcpserver.Builder.Tools` are **managed tools**. They
receive typed input validation, encoded-output checks without normalization,
safe error responses, panic recovery, and optional call telemetry. The supported
endpoint uses stateless **Streamable HTTP**, not a stdio command launched by the client.
Numeric output constraints use exact decimal values within the
[strict validator's resource limits](tools.md#numeric-validation-and-limits).
Output checks do not make handler effects transactional.

MCP also defines resources and prompts. Arkoren has no typed facade for them;
they and other transports require the [native SDK boundary](tools.md#extend-through-the-native-sdk-deliberately).
Native tool registrations do not inherit managed protections.

Focused guide snippets keep error results visible but omit routine propagation.
Complete functions and programs handle errors before using results.

For every declaration and adapter setting, see the [core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp)
and [MCP server API](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver).
