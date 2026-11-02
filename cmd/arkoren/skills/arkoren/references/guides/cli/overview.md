---
title: Overview
description: Choose remote MCP inspection or local provisioning and install the Arkoren CLI.
order: 10
summary:
  - Distinguish remote tool discovery and execution from local provisioning.
  - Install the CLI and verify its version and help without a running endpoint.
  - Choose connection setup or application capture setup as your next task.
---

# Overview

Use `arkoren` when you need to inspect an application from your terminal or
prepare local files before deployment. Remote inspection calls the application's
MCP endpoint; project, key, certificate, and bundled-skill commands do not need a
running application.

## Remote Tools

A remote command comes from the endpoint's current tool catalog. Each invocation
negotiates MCP and reads `tools/list`; there is no saved catalog or separate
inspection protocol or Arkoren core-version handshake. The advertised schema
supplies input flags and help.
Optional CLI metadata supplies names such as `public:routes`; exact MCP names
remain callable without that metadata.

Discovery and execution are separate. `arkoren tools` lists available tools, and
remote `--help` describes them without invoking a handler. Calling a discovered
command sends `tools/call`. The server authenticates the caller and authorizes
the operation; a catalog entry is not permission to execute it.

`arkoren status` goes beyond discovery: it invokes uniquely advertised identity
and health roles. Those roles do not guarantee read-only handlers, and the report
is not a readiness gate. Review an endpoint's contracts before calling its tools.

## Local Tasks

Use [project creation](../first-steps/creating-a-project.md) to prepare a starter
application, or [install the bundled skill](../agent-skills/working-with-agents.md)
for an agent working in an existing project. These commands use local assets,
not MCP discovery.

[Key Commands](keys.md) provision application keys without printing private
material unless you explicitly request an export. [Certificate Commands](certificates.md)
generate local TLS identities without installing trust. Root trust installation
and removal are separate, explicitly approved operations that change a system
store; they are not part of ordinary file generation.

## Installing Arkoren

With Go 1.27 or later, install the executable:

```sh
go install arkoren.dev/cmd/arkoren@latest
```

Go places `arkoren` in `GOBIN`, or its default binary directory. Add that
directory to `PATH`. Check which CLI is installed without reading endpoint
configuration or connecting to a server:

```sh
arkoren --version
```

The result is JSON containing `version`. To read the local command help, run:

```sh
arkoren --help
```

Help is text. Bare `arkoren` prints the same offline root help. To track the CLI
as a module tool instead of installing it on `PATH`, run
`go get -tool arkoren.dev/cmd/arkoren@latest` in your application module and invoke
it as `go tool arkoren`.

## Connecting Next

If you already have a trusted endpoint and an accepted credential, continue with
[Connecting](connecting.md), then [Calling Tools](calling-tools.md). If your
application does not expose diagnostics yet, start with
[Capture Setup](../diagnostics/capture-setup.md). The
[CLI Reference](reference.md) owns command and flag lookup.
