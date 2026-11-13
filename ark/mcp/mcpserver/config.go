package mcpserver

import (
	"log/slog"
	"slices"

	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
)

// Builder is a construction-only draft. New snapshots its tool registrations;
// retained drafts cannot change a published Server. Services remain shared.
//
// Example:
//
//	server, err := mcpserver.New("catalog", "1.0", func(listener *mcpserver.Builder) {
//		listener.Logger(slog.Default())
//	})
//	if err != nil {
//		return err
//	}
//	mux := http.NewServeMux()
//	mux.Handle("/mcp", server)
type Builder struct {
	// tools preserves registration order and is detached on publication.
	tools []mcp.Tool
	// logger receives original local failures; its storage must be protected.
	logger *slog.Logger
	// telemetry overrides the dynamic default when non-nil.
	telemetry *telemetry.Telemetry
}

// BuilderFunc edits one fresh draft synchronously during New. Nil callbacks
// panic; an expanded nil slice supplies no edits.
//
// Example:
//
//	policy := mcpserver.BuilderFunc(func(listener *mcpserver.Builder) {
//		listener.Logger(slog.Default())
//	})
//	server, err := mcpserver.New("catalog", "1.0", policy)
//	if err != nil {
//		return err
//	}
//	mux := http.NewServeMux()
//	mux.Handle("/mcp", server)
type BuilderFunc = func(*Builder)

// Tools appends managed definitions in order, copying the supplied slice.
// New rejects invalid definitions and duplicate names before SDK registration.
//
// Example:
//
//	status := mcp.NewTool("status", func(context.Context, mcp.NoInput) (struct{}, error) {
//		return struct{}{}, nil
//	})
//	policy := func(listener *mcpserver.Builder) {
//		listener.Tools(status)
//	}
//	server, err := mcpserver.New("catalog", "1.0", policy)
//	if err != nil {
//		return err
//	}
//	mux := http.NewServeMux()
//	mux.Handle("/mcp", server)
func (listener *Builder) Tools(tools ...mcp.Tool) *Builder {
	listener.tools = append(slices.Clone(listener.tools), tools...)

	return listener
}

// Logger replaces the local logger. Nil restores slog.Default at construction.
// Log storage must be protected: handler errors may contain application secrets.
//
// Example:
//
//	policy := func(listener *mcpserver.Builder) {
//		listener.Logger(slog.Default())
//	}
//	server, err := mcpserver.New("catalog", "1.0", policy)
//	if err != nil {
//		return err
//	}
//	mux := http.NewServeMux()
//	mux.Handle("/mcp", server)
func (listener *Builder) Logger(logger *slog.Logger) *Builder {
	listener.logger = logger

	return listener
}

// Telemetry selects the shared telemetry service. Nil follows telemetry.Default
// per call. Arguments, outputs, and error messages are never recorded.
//
// Example:
//
//	policy := func(listener *mcpserver.Builder) {
//		listener.Telemetry(telemetry.Default())
//	}
//	server, err := mcpserver.New("catalog", "1.0", policy)
//	if err != nil {
//		return err
//	}
//	mux := http.NewServeMux()
//	mux.Handle("/mcp", server)
func (listener *Builder) Telemetry(service *telemetry.Telemetry) *Builder {
	listener.telemetry = service

	return listener
}
