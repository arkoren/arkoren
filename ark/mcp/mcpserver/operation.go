package mcpserver

import "arkoren.dev/core/http/router"

// Operation is the OpenAPI metadata for mounting a [Server] on a router. The
// endpoint speaks the MCP streamable HTTP protocol: one path carries JSON-RPC
// requests, notifications and event streams whose catalog is advertised by
// tools/list, not by an HTTP operation. The router's OpenAPI export cannot
// describe a [router.Router.Mount] (an opaque catch-all) as one path and
// reports an error for a visible annotation, so Operation hides the
// registration: the route stays callable and listed by Routes, and export
// neither includes nor rejects it. Passing it states that omission on purpose.
//
// It changes no handler behavior and installs no middleware; authentication,
// authorization and origin checks remain the application's.
//
// Example:
//
//	server, err := mcpserver.New("catalog", "1.0")
//	if err != nil {
//		return err
//	}
//	routes.Mount("/mcp", server, mcpserver.Operation)
func Operation(operation *router.OperationBuilder) {
	operation.Hidden(true)
}
