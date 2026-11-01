package remote

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// wireTransport retains exact JSON for the SDK's generic result fields. The
// pinned SDK decodes any into float64; keeping a copy at its public Connection
// boundary lets the CLI restore domain values without replacing MCP negotiation,
// HTTP/SSE parsing, protocol errors, cancellation, or session ownership.
// One inspector command performs sequential tools/list and tools/call requests.
type wireTransport struct {
	// transport remains the SDK-owned protocol transport and owns its connection.
	transport mcp.Transport

	// pending maps JSON-RPC request IDs to the two methods with generic values.
	// SDK reads and writes can run concurrently, so capture uses synchronized maps.
	pending sync.Map

	// results retains one raw result per method until its sequential caller reads it.
	// Values are detached jsontext.Value snapshots; no payload is logged or printed.
	results sync.Map
}

// wireConnection observes SDK-decoded JSON-RPC envelopes, not HTTP or SSE frames.
// Its embedded connection retains the SDK's close and session-ID behavior.
type wireConnection struct {
	// Connection owns transport reads, writes, shutdown, and session identity.
	// Capture delegates all operations rather than creating another protocol stack.
	//
	// Example:
	//
	//	if err := connection.Close(); err != nil {
	//		return err
	//	}
	mcp.Connection

	// wire receives detached snapshots correlated with actual outgoing request IDs.
	wire *wireTransport
}

// Connect delegates to the SDK once and adds precision capture to that connection.
// The returned connection has the same lifecycle and failure behavior as the SDK.
//
// Example:
//
//	connection, err := transport.Connect(ctx)
//	if err != nil {
//		return err
//	}
//	defer connection.Close()
func (wire *wireTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := wire.transport.Connect(ctx)

	if err != nil {
		return nil, err
	}

	return &wireConnection{Connection: connection, wire: wire}, nil
}

// Write records only tools/list and tools/call request IDs before delegating.
// A failed write releases its correlation entry; no request is retried here.
//
// Example:
//
//	if err := connection.Write(ctx, message); err != nil {
//		return err
//	}
func (connection *wireConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	request, ok := message.(*jsonrpc.Request)

	if !ok {
		return connection.Connection.Write(ctx, message)
	}

	if request.Method != "tools/list" && request.Method != "tools/call" {
		return connection.Connection.Write(ctx, message)
	}

	connection.wire.results.Delete(request.Method)
	connection.wire.pending.Store(request.ID, request.Method)

	err := connection.Connection.Write(ctx, message)

	if err != nil {
		connection.wire.pending.Delete(request.ID)
	}

	return err
}

// Read copies matching response JSON before the SDK decodes generic values.
// It does not alter messages or interpret protocol errors; the SDK remains the
// authority for whether the corresponding method completed successfully.
//
// Example:
//
//	message, err := connection.Read(ctx)
//	if err != nil {
//		return err
//	}
//	_ = message // The SDK consumes the unchanged envelope.
func (connection *wireConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	message, err := connection.Connection.Read(ctx)

	if err != nil {
		return nil, err
	}

	response, ok := message.(*jsonrpc.Response)

	if !ok {
		return message, nil
	}

	if method, found := connection.wire.pending.LoadAndDelete(response.ID); found {
		connection.wire.results.Store(method, jsontext.Value(bytes.Clone(response.Result)))
	}

	return message, nil
}

// result consumes the raw result after a successful sequential SDK call. Missing
// capture is an internal error, never permission to use rounded fallback values.
func (wire *wireTransport) result(method string) (jsontext.Value, error) {
	value, ok := wire.results.LoadAndDelete(method)

	if !ok {
		return nil, fmt.Errorf("missing raw MCP result for %s", method)
	}

	return value.(jsontext.Value), nil
}

// restoreToolResult replaces only generic payload fields after SDK success.
// Typed content, error flags, and input-required state stay SDK-owned. Raw
// metadata preserves safe structured failure details as well as success values.
func (wire *wireTransport) restoreToolResult(result *mcp.CallToolResult) error {
	raw, err := wire.result("tools/call")

	if err != nil {
		return err
	}

	var document struct {
		// StructuredContent is retained as JSON, not decoded domain data.
		StructuredContent jsontext.Value `json:"structuredContent"`
		// Meta retains exact extension values while the SDK owns protocol fields.
		Meta map[string]jsontext.Value `json:"_meta"`
	}

	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode raw tool result: %w", err)
	}

	if len(document.StructuredContent) > 0 && document.StructuredContent.Kind() != 'n' {
		result.StructuredContent = document.StructuredContent
	}

	for name, value := range document.Meta {
		result.Meta[name] = value
	}

	return nil
}

// restoreTools restores each SDK page's generic schema and metadata fields in
// discovery order. It does not interpret schemas, enforce constraints, or change
// native identities; catalogOperation remains the CLI presentation boundary.
func (wire *wireTransport) restoreTools(tools []*mcp.Tool) error {
	raw, err := wire.result("tools/list")

	if err != nil {
		return err
	}

	var document struct {
		// Tools retains only fields otherwise decoded by the SDK into any.
		Tools []struct {
			// Name correlates raw fields after the SDK filters invalid tools.
			Name string `json:"name"`
			// InputSchema retains exact native input schema JSON.
			InputSchema jsontext.Value `json:"inputSchema"`
			// OutputSchema retains exact native output schema JSON, if advertised.
			OutputSchema jsontext.Value `json:"outputSchema"`
			// Meta preserves optional CLI examples and other extension values.
			Meta map[string]jsontext.Value `json:"_meta"`
		} `json:"tools"`
	}

	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode raw tool catalog: %w", err)
	}

	positions := make(map[string]int, len(document.Tools))

	for index, tool := range document.Tools {
		positions[tool.Name] = index
	}

	for _, tool := range tools {
		index, ok := positions[tool.Name]

		if !ok {
			return fmt.Errorf("missing raw MCP catalog entry for %q", tool.Name)
		}

		encoded := document.Tools[index]
		tool.InputSchema, tool.OutputSchema = encoded.InputSchema, encoded.OutputSchema

		for name, value := range encoded.Meta {
			tool.Meta[name] = value
		}
	}

	return nil
}
