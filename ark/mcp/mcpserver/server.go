package mcpserver

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/ark/mcp/mcpserver/internal/wire"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/foundation/validate"
)

// Server adapts immutable tools to stateless MCP Streamable HTTP. It is not an
// [http.Handler]: [Server.Register] and [Server.EndpointHandler] attach it to a
// [router.Router]. It has no lifecycle resources; the application owns its
// listener, authentication and Origin checks.
//
// Example:
//
//	server, err := mcpserver.New("catalog", "1.0")
//	if err != nil {
//		return err
//	}
//	routes.Group("/mcp", server.Register)
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
//	routes.Group("/mcp", server.Register)
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

// EndpointHandler returns the MCP Streamable HTTP endpoint as a
// [router.Handler], for applications that choose their own path or method set.
// Most applications use [Server.Register] instead.
//
// The handler is the SDK's stateless, JSON-response transport: it accepts POST
// only and answers every other method with 405 and "Allow: POST", ignores the
// request path, keeps no sessions and offers no GET event stream. The request
// context reaches tool handlers unchanged, so identities placed there by
// application middleware stay visible. The SDK bounds request bodies.
//
// The server applies no Origin validation, authentication, authorization or
// timeouts. The application owns them as middleware on the router group that
// registers the endpoint, ahead of it. The MCP specification requires servers
// to validate the Origin header against DNS rebinding, so serve a local
// endpoint behind an Origin check such as the csrf middleware or a custom one.
//
// Example:
//
//	server, err := mcpserver.New("catalog", "1.0")
//	if err != nil {
//		return err
//	}
//	routes.Post("/rpc", server.EndpointHandler())
func (server *Server) EndpointHandler() router.Handler {
	return router.HTTP(server.handler)
}

// Register adds the MCP endpoint to r as POST "/", so the endpoint is the path
// of the group it is attached to. It applies no middleware and no prefix of its
// own, and registers no GET or DELETE route: the router answers those with 405,
// as the specification suggests for a server without sessions or a stream.
//
// Attach it to a group, after the middleware that admits the caller:
// authentication and authorization, Origin validation and timeouts belong to
// the application. Registering it twice on one group panics as any duplicate
// route does.
//
// Example:
//
//	server, err := mcpserver.New("catalog", "1.0")
//	if err != nil {
//		return err
//	}
//	routes := router.New()
//	routes.With(auth.Middleware(authenticator), auth.Require()).Group("/mcp", server.Register)
func (server *Server) Register(r *router.Router) {
	r.Post("/", server.EndpointHandler())
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
