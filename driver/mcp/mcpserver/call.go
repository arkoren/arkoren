package mcpserver

import (
	"context"
	"encoding/json/jsontext"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/driver/mcp/mcpserver/internal/wire"
)

// execute contains application execution and output validation inside one
// recovery boundary. It never retries a handler after any kind of failure.
func (server *Server) execute(
	ctx context.Context,
	entry registration,
	input jsontext.Value,
) (result *sdkmcp.CallToolResult) {
	defer func() {
		if cause := recover(); cause != nil {
			server.logger.ErrorContext(ctx, "mcp tool panicked", "tool", entry.tool.Name(), "panic", cause)
			result = wire.Failure("tool failed")
		}
	}()

	output, err := entry.tool.Call(ctx, input)

	if err != nil {
		server.logger.ErrorContext(ctx, "mcp tool failed", "tool", entry.tool.Name(), "error", err)

		return wire.ErrorResult(err)
	}

	result, err = wire.Output(ctx, entry.output, output)

	if err != nil {
		server.logger.ErrorContext(ctx, "mcp tool output validation failed", "tool", entry.tool.Name(), "error", err)

		return wire.OutputFailure(err)
	}

	return result
}
