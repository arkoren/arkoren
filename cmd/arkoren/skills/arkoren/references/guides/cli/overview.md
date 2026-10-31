---
title: Arkoren CLI Overview
nav: Overview
description: Install the Arkoren CLI to call MCP tools or provision projects, keys, certificates, and bundled agent skills offline.
order: 10
---

# Arkoren CLI Overview

## Introduction

Use `arkoren` to discover and call a running application's MCP tools from your
terminal or scripts. For example, you can inspect routes, find a failed trace,
or wait for readiness when the endpoint publishes those diagnostics.
The CLI also [creates starter projects](../first-steps/creating-a-project.md)
offline, [generates and validates keys](keys.md), and
[manages its bundled agent skill](../agent-skills/working-with-agents.md).
For local HTTPS, [certificate commands](certificates.md) generate and check TLS
files offline; installing or removing system root trust is a separate,
explicitly confirmed operation.

Remote commands initialize MCP and discover tools through `tools/list`. Tool
schemas supply flags and help; optional CLI metadata supplies `group:operation`
names. `tools` and remote help stop at discovery: neither invokes any tool.
Calling a tool sends `tools/call` for the selected operation.

The `status` command explicitly invokes uniquely advertised identity and health
roles for its report. These roles describe a result's meaning, not a read-only
guarantee; review the endpoint's tool contracts before requesting status. There
is no separate inspection protocol, manifest endpoint, or client-side
authorization system.

## Installing the CLI

You need Go 1.27 or later. Install the executable:

```sh
go install arkoren.dev/cmd/arkoren@latest
```

Go writes `arkoren` to `GOBIN`, or its default binary directory. Add that
directory to `PATH`, then verify the executable without endpoint configuration:

```sh
arkoren --version
arkoren --help
```

These commands print the installed CLI version as JSON and generated root help
as text. Neither reads env files or connects to a server. Use `arkoren new --help`
for the project-creation options.

To record it as a module tool instead, use
`go get -tool arkoren.dev/cmd/arkoren@latest` and invoke `go tool arkoren`.

## Connecting next

With an existing endpoint and accepted credential, follow
[Connecting the CLI](connecting.md), then [Calling Tools](calling-tools.md).
To publish diagnostics in your application first, follow
[Exposing Diagnostics](../observability/exposing-diagnostics.md).

MCP protocol negotiation uses the official SDK. There is no Arkoren core-version
handshake or saved catalog: discovery runs on every remote invocation. Tools
without Arkoren CLI metadata remain callable by their exact advertised names.

For tool declarations and metadata, see the [MCP API reference](https://pkg.go.dev/arkoren.dev/core/mcp).
