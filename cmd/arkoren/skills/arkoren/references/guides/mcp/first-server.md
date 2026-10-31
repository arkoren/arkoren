---
title: Your First MCP Server
nav: First Server
description: Install the MCP modules, serve a tool over local HTTP, and discover and call it with an MCP client.
order: 20
---

# Your First MCP Server

## Introduction

This guide builds a small Go server that exposes a read-only `status` tool.
You will start it on your machine, connect an MCP client, and see the tool's
result. No model-provider API key, database, or Arkoren CLI is needed.

You need Go 1.27 or later. For the client checks,
you also need Node.js 22.19 or later and npm to run the
[MCP Inspector](https://modelcontextprotocol.io/docs/tools/inspector).
The Inspector is a test client, not a dependency of your Go server.

The steps below create two files for a standalone local process. You will
verify discovery and execution over the protocol, not through direct Go calls.

## Install the modules

Create a project:

```sh
mkdir mcp-status
cd mcp-status
go mod init example.com/mcp-status
```

Install the core and MCP server modules:

```sh
go get arkoren.dev/core arkoren.dev/driver/mcp
```

`arkoren.dev/core/mcp` defines tools. `arkoren.dev/driver/mcp/mcpserver` serves them and brings in the
official Go MCP SDK automatically. There is no separate MCP server package to
install globally.

## Define the tool

Create `tools.go`:

```go
package main

import (
	"context"

	"arkoren.dev/core/mcp"
)
```

Add the result object and its handler:

```go
type Status struct {
	Ready bool `json:"ready"`
}

func reportStatus(ctx context.Context, input mcp.NoInput) (Status, error) {
	return Status{Ready: true}, nil
}
```

This sample always reports ready; it does not check real dependencies.

Publish the handler as a package-level tool definition in the same file:

```go
var status = mcp.NewTool("status", reportStatus, func(definition *mcp.ToolBuilder[mcp.NoInput, Status]) {
	definition.Description("Report the sample service's status.")
	definition.ReadOnly(true)
	definition.OpenWorld(false)
})
```

`mcp.NoInput` means the tool accepts an empty JSON object, `{}`. The result is
also an object. The description tells clients what the tool does; the read-only
hint describes its behavior but does not enforce permissions.

## Build the HTTP endpoint

Create `main.go`:

```go
package main

import (
	"log"
	"net/http"

	"arkoren.dev/core/http/middleware/csrf"
	"arkoren.dev/core/http/router"
	"arkoren.dev/driver/mcp/mcpserver"
)
```

Add a function that registers the tool with the server:

```go
func newEndpoint() (*mcpserver.Server, error) {
	return mcpserver.New("status-service", "1.0.0", func(listener *mcpserver.Builder) {
		listener.Tools(status)
	})
}
```

The name and version identify your server to clients. Construction checks the
tool definition and returns an error if it is invalid. It does not open a
network port: the returned endpoint is an `http.Handler`.

## Start the server

Add `main` below `newEndpoint` in `main.go`:

```go
func main() {
	endpoint, err := newEndpoint()
	if err != nil {
		log.Fatal(err)
	}

	routes := router.New()
	routes.Use(csrf.Middleware())
	routes.Mount("/mcp", endpoint)

	log.Print("serving MCP at http://127.0.0.1:8080/mcp")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", routes))
}
```

This unauthenticated example exposes only a fixed, nonsensitive status result.
It binds to loopback and uses Arkoren's [CSRF middleware](../browser-security/csrf-protection.md)
to reject unsafe cross-origin browser requests. Keep that bind
address: before exposing private tools or accepting remote traffic, configure
[authentication, Host and Origin policy, and HTTP limits](securing-mcp.md).

From the project directory, run:

```sh
go run .
```

The terminal logs the MCP URL and stays running. Leave it open for the client
checks below. If the server reports that the address is already in use, stop
the other local service or change the port in both the code and client commands.

## Connect a client

In a second terminal, ask the MCP Inspector to list the server's tools:

```sh
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/mcp \
  --transport http --method tools/list
```

The first run downloads the Inspector if needed. The result lists `status`,
its description, and its input and output schemas. This checks the MCP
connection and discovery, not just whether the HTTP port is open.

Now call the tool through the same client:

```sh
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/mcp \
  --transport http --method tools/call --tool-name status
```

The result's `structuredContent` contains:

```json
{
  "ready": true
}
```

The same JSON is also returned as text content. A successful list followed by
this call verifies discovery, argument handling, handler execution, and the
server's output checks over the actual MCP transport.

To connect an AI application instead, add an HTTP MCP server in that
application's client settings with URL `http://127.0.0.1:8080/mcp`. Choose
Streamable HTTP, not a stdio command. The client must run on your machine to
reach this loopback URL; a cloud-hosted client needs a reachable, secured
deployment instead.

## If the connection fails

Opening `/mcp` in a browser is not an MCP connection test. This stateless
endpoint accepts protocol requests over POST, so an ordinary browser GET returns
`405 Method Not Allowed`. Use the Inspector commands above.

If the Inspector cannot reach the server, check that `go run .` is still running
and that the host, port, and `/mcp` path match. A stdio-only client cannot
connect to this HTTP endpoint. If you inspect requests manually, send
`Content-Type: application/json` and an `Accept` header containing both
`application/json` and `text/event-stream`.

## Next steps

Stop the local example with Ctrl+C. It is a small standalone process, not a
production deployment with graceful shutdown.

Continue with [MCP Tools](tools.md) to accept and validate arguments, test the
operation, and mount it in your existing HTTP service. Before exposing private
tools or remote access, follow [Securing MCP](securing-mcp.md).

For all declarations and settings, see the [core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp)
and [MCP server API](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver).
