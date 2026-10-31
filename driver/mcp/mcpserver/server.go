package mcpserver

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/mcp/mcpserver/internal/wire"
	"arkoren.dev/foundation/validate"
)

// Server adapts immutable tools to stateless MCP Streamable HTTP. It has no
// lifecycle resources; the application owns its listener and authentication.
//
// Example:
//
//	server, err := mcpserver.New("catalog", "1.0")
//	if err != nil {
//		return err
//	}
//	mux.Handle("/mcp", server)
type Server struct {
	// native owns protocol negotiation and registration.
	native *sdkmcp.Server
	// handler enforces the SDK's bounded stateless transport.
	handler http.Handler
	// managed indexes immutable policies without hiding native SDK additions.
	managed map[string]mcp.Tool
	// logger receives local handler failures.
	logger *slog.Logger
	// telemetry is optional; nil resolves the default on every call.
	telemetry *telemetry.Telemetry
}

// registration holds a validated definition and its compiled output contract.
// Compilation is completed for every tool before mutating the SDK registry.
type registration struct {
	// tool carries the typed execution boundary.
	tool mcp.Tool
	// output validates emitted JSON without applying defaults or normalization.
	output validate.EncodedSchema
	// meta is a detached JSON-compatible SDK reporting record.
	meta sdkmcp.Meta
}

// New publishes a tools-only adapter without network I/O. Name and version must
// be nonempty. Callbacks run directly, once, in order; their panics propagate.
// Invalid tools, duplicate names, and unsupported schemas return errors before
// any SDK tool is registered. Handler closures and services remain shared.
// New itself opens no connections; supplied callbacks remain application code.
//
// Example:
//
//	server, err := mcpserver.New("catalog", "1.0", func(listener *mcpserver.Builder) {
//		listener.Logger(slog.Default())
//	})
//	if err != nil {
//		return err
//	}
//	mux.Handle("/mcp", server)
func New(name, version string, configure ...BuilderFunc) (*Server, error) {
	listener := &Builder{}

	for _, configure := range configure {
		configure(listener)
	}

	if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
		return nil, fmt.Errorf("mcpserver: name and version must be nonempty")
	}

	registrations, err := prepare(listener.tools)

	if err != nil {
		return nil, err
	}

	logger := listener.logger

	if logger == nil {
		logger = slog.Default()
	}

	server := &Server{
		native: sdkmcp.NewServer(&sdkmcp.Implementation{Name: name, Version: version}, &sdkmcp.ServerOptions{
			PageSize: int(^uint(0) >> 1), // Catalogs are deliberately unpaginated; see listTools.
		}),
		logger: logger, telemetry: listener.telemetry,
		managed: make(map[string]mcp.Tool, len(listener.tools)),
	}

	for _, entry := range registrations {
		server.register(entry)
		server.managed[entry.tool.Name()] = entry.tool
	}

	server.native.AddReceivingMiddleware(server.availabilityMiddleware)
	server.handler = sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server.native
	}, &sdkmcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, PropagateRequestCancellation: true,
	})

	return server, nil
}

// prepare rejects declaration errors and collisions before SDK publication.
// Schema resolution permits local references only and performs no network I/O.
func prepare(tools []mcp.Tool) ([]registration, error) {
	entries := make([]registration, 0, len(tools))
	names := make(map[string]bool, len(tools))

	for _, tool := range tools {
		if err := tool.Err(); err != nil {
			return nil, fmt.Errorf("mcpserver: tool: %w", err)
		}

		if names[tool.Name()] {
			return nil, fmt.Errorf("mcpserver: duplicate tool %q", tool.Name())
		}

		names[tool.Name()] = true
		info := tool.Describe()
		output, meta, err := wire.Prepare(info)

		if err != nil {
			return nil, err
		}

		entries = append(entries, registration{tool: tool, output: output, meta: meta})
	}

	return entries, nil
}

// ServeHTTP delegates protocol handling without replacing the request context.
// Wrap this handler with application authentication, authorization, origin
// checks, and HTTP timeouts. The SDK bounds request bodies to its default limit.
//
// Example:
//
//	server, err := mcpserver.New("catalog", "1.0")
//	if err != nil {
//		return err
//	}
//	mux.Handle("/mcp", http.HandlerFunc(server.ServeHTTP))
func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	server.handler.ServeHTTP(writer, request)
}

// Server exposes the native SDK server for advanced protocol features. Native
// additions bypass managed error protection, validation, telemetry, and
// CLI metadata. They remain visible in the unpaginated tools/list catalog without
// availability checks. Do not replace managed tools or change managed-list
// middleware through this accessor; doing so can bypass execution guards.
// The shared SDK page size also leaves native resource/prompt lists unpaginated.
// In the example, sdkmcp names github.com/modelcontextprotocol/go-sdk/mcp.
//
// Example:
//
//	server, err := mcpserver.New("catalog", "1.0")
//	if err != nil {
//		return err
//	}
//	native := server.Server()
//	native.AddTool(&sdkmcp.Tool{
//		Name: "native.status", InputSchema: jsontext.Value(`{"type":"object"}`),
//	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
//		return &sdkmcp.CallToolResult{
//			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ready"}},
//		}, nil
//	})
func (server *Server) Server() *sdkmcp.Server {
	return server.native
}
