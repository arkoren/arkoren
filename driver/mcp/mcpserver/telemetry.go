package mcpserver

import (
	"context"
	"encoding/json/jsontext"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/service/telemetry"
)

// invoke records only the registered name and bounded outcome. Execution and
// recovery finish before observing IsError, including explicit tool failures.
func (server *Server) invoke(ctx context.Context, entry registration, input jsontext.Value) *sdkmcp.CallToolResult {
	service := server.telemetry

	if service == nil {
		service = telemetry.Default()
	}

	ctx, span := service.Tracer("arkoren.dev/driver/mcp").Start(ctx, "mcp call_tool",
		telemetry.Attrs(telemetry.String("arkoren.mcp.tool", entry.tool.Name())))
	defer span.End()

	startedAt := time.Now()
	result := server.execute(ctx, entry, input)

	outcome := "success"

	if result.IsError {
		outcome = "error"
		span.SetStatus(telemetry.Error, "tool failed")
	}

	attributes := []telemetry.Attr{
		telemetry.String("arkoren.mcp.tool", entry.tool.Name()),
		telemetry.String("arkoren.mcp.outcome", outcome),
	}
	span.SetAttributes(attributes...)

	meter := service.Meter("arkoren.dev/driver/mcp")
	meter.Counter("arkoren.mcp.calls", telemetry.Keys("arkoren.mcp.tool", "arkoren.mcp.outcome")).
		Add(ctx, 1, attributes...)
	meter.Histogram("arkoren.mcp.call.duration", telemetry.Unit("s"),
		telemetry.Keys("arkoren.mcp.tool", "arkoren.mcp.outcome")).Record(ctx,
		time.Since(startedAt).Seconds(), attributes...)

	return result
}
