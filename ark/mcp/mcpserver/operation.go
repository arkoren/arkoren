package mcpserver

import (
	"net/http"

	"arkoren.dev/core/http/router"
)

// Operation describes the MCP endpoint that [Server.Register] and
// [Server.EndpointHandler] serve, on the route the caller chooses. The endpoint
// speaks the stateless, JSON-response form of the MCP Streamable HTTP
// transport: a client POSTs one JSON-RPC message, and the answer is the JSON-RPC
// response at 200 or, for a notification, an empty 202. The JSON-RPC schemas are
// kept open because the catalog of tools, with their input and output schemas,
// is advertised by the protocol's tools/list, not by HTTP. Tool failures travel
// inside the 200 JSON-RPC result. Every other method is 405 with "Allow: POST",
// and there is no event stream.
//
// The annotation changes no handler behavior and installs no middleware:
// authentication, authorization, Origin validation and timeouts remain the
// application's.
//
// Example:
//
//	routes.Post("/rpc", server.EndpointHandler(), mcpserver.Operation)
func Operation(operation *router.OperationBuilder) {
	message := router.RawSchema(`{"type":"object","required":["jsonrpc"],"properties":{
		"jsonrpc":{"type":"string","const":"2.0"},
		"id":{"description":"Present on requests, absent on notifications."},
		"method":{"type":"string","description":"The MCP method, for example tools/list or tools/call."},
		"params":{"type":"object"}},"additionalProperties":true}`)

	operation.Summary("MCP endpoint")
	operation.Description("Model Context Protocol endpoint over the stateless Streamable HTTP transport. " +
		"POST one JSON-RPC 2.0 message with Content-Type application/json; the client advertises that it accepts " +
		"application/json. A request is answered with its JSON-RPC response at 200; a notification is answered " +
		"with an empty 202. Tool errors are reported inside the JSON-RPC result, not as HTTP errors. " +
		"There are no sessions and no GET event stream; other methods answer 405.")
	operation.Tags("mcp")
	operation.Body("application/json", message).BodyRequired(true)
	operation.JSONResponse(http.StatusOK, router.RawSchema(`{"type":"object","required":["jsonrpc"],"properties":{
		"jsonrpc":{"type":"string","const":"2.0"},"id":{},"result":{},"error":{"type":"object"}},"additionalProperties":true}`))
	operation.Response(http.StatusOK, "The JSON-RPC response")
	operation.Response(http.StatusAccepted, "A notification was accepted; there is no body")
	operation.Response(http.StatusBadRequest, "The message is not valid JSON-RPC or the request is not acceptable to the transport")
	operation.Response(http.StatusMethodNotAllowed, "Only POST is served")
	operation.ResponseHeader(http.StatusMethodNotAllowed, "Allow", router.RawSchema(`{"type":"string","const":"POST"}`))
}
