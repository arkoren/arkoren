---
title: First Server
description: Build a local HTTP MCP server and discover and call its first tool with an MCP client.
order: 20
summary:
  - Create a typed public tool and explicitly register its HTTP MCP endpoint.
  - Discover and call the tool with the MCP Inspector over loopback HTTP.
  - Recognize connection failures and retain application-owned server lifecycle.
---

# First Server

Build a small server that returns a fixed service label through MCP. This
walkthrough takes you from an empty Go project to client discovery and a tool
call over HTTP. It needs no database, model API key, or Arkoren CLI.

Use Go 1.27 or later. For the client checks, install Node.js 22.19 or later and
npm to run the [MCP Inspector](https://modelcontextprotocol.io/docs/tools/inspector).
The Inspector is a client, not a server dependency.

## Project Setup

Create a directory and initialize its module:

```sh
mkdir mcp-service
cd mcp-service
go mod init example.com/mcp-service
```

Install the tool definitions and HTTP adapter:

```sh
go get arkoren.dev/core arkoren.dev/ark/mcp
```

The adapter brings in the official Go MCP SDK. The following steps create
`tools.go` and `main.go` in this directory; append each block to its named file.

## Service Operation

Create `tools.go` with its package and imports:

```go
package main

import (
	"context"

	"arkoren.dev/core/mcp"
)
```

Add an object result and a handler that returns only a public sample label:

```go
type ServiceInfo struct {
	Name string `json:"name"`
}

func describeService(ctx context.Context, input mcp.NoInput) (ServiceInfo, error) {
	return ServiceInfo{Name: "parcel-demo"}, nil
}
```

This is static sample data, not a health check or inspection of the running
process. `mcp.NoInput` represents an empty object, `{}`, not JSON `null`.

## Tool Definition

Publish the handler below it in `tools.go`:

```go
var describe = mcp.NewTool("service.describe", describeService, func(action *mcp.ToolBuilder[mcp.NoInput, ServiceInfo]) {
	action.Description("Return the public name of this sample service.")
	action.ReadOnly(true)
	action.OpenWorld(false)
})
```

The definition supplies the name, schemas, and hints clients discover. It opens
no network connection and does not call the handler. `ReadOnly` describes this
operation; it does not enforce access control.

## Endpoint Registration

Create `main.go` with these imports:

```go
package main

import (
	"log"
	"net/http"

	"arkoren.dev/core/http/middleware/csrf"
	"arkoren.dev/core/http/router"
	"arkoren.dev/ark/mcp/mcpserver"
)
```

Add a constructor that explicitly registers the tool:

```go
func newEndpoint() (*mcpserver.Server, error) {
	return mcpserver.New("parcel-demo", "1.0.0", func(listener *mcpserver.Builder) {
		listener.Tools(describe)
	})
}
```

The name and version identify your server. Construction rejects invalid tool
definitions and duplicate names. The returned endpoint is an `http.Handler`,
not a listening server.

## HTTP Mount

This unauthenticated sample is safe only for the fixed public result shown
above. Keep it on loopback. Before exposing private tools or remote access,
configure [TLS, authentication, Host, and Origin policy](securing-mcp.md).

Append `main` to `main.go`:

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

The [CSRF middleware](../browser-security/csrf-protection.md) rejects unsafe
cross-origin browser requests. It is not authentication. Your application owns
the listener and mount; the MCP endpoint has no `Start` or `Stop` method.

Run the project:

```sh
go run .
```

The terminal logs the endpoint URL and stays running. If port 8080 is occupied,
choose another port in both the code and the client commands below.

## Client Verification

Leave the server running and use a second terminal to discover its tools:

```sh
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/mcp \
  --transport http --method tools/list
```

The first invocation downloads the Inspector if needed. The response lists
`service.describe` with its description and object schemas. Discovery does not
execute the handler.

Call the listed operation through the same HTTP transport:

```sh
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/mcp \
  --transport http --method tools/call --tool-name service.describe
```

The result's `structuredContent` contains this object, also returned as text
content:

```json
{
  "name": "parcel-demo"
}
```

A successful list and call confirm protocol discovery and execution, not merely
an open HTTP port. To use an AI application's client instead, configure an MCP
server with this URL and Streamable HTTP transport. A stdio-only client cannot
connect. The client must run locally to reach this loopback address; a hosted
client needs a reachable, secured deployment.

## Connection Failures

If the client cannot connect, check that `go run .` is still running and that
the host, port, and `/mcp` path match. Opening `/mcp` in a browser is not an MCP
test: this stateless endpoint uses POST, and GET returns `405 Method Not Allowed`.

For manual protocol requests, send `Content-Type: application/json` and an
`Accept` header containing both `application/json` and `text/event-stream`.
Prefer an MCP client, which also handles protocol negotiation.

## Application Integration

Stop the example with Ctrl+C. This standalone process does not implement
graceful shutdown. When adding MCP to an existing application, retain its
[HTTP lifecycle and timeouts](../http-networking/server.md); register the MCP
handler without adding a second listener or lifecycle owner.

Continue with [Tools](tools.md) for validated arguments, output contracts, and
safe failures. For exact declarations and settings, see the
[core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp) and
[MCP server API](https://pkg.go.dev/arkoren.dev/ark/mcp/mcpserver).
