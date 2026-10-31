package mcpserver

import (
	"context"
	"encoding/json/jsontext"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// register maps detached metadata to the SDK's non-generic registration API.
// Raw schema bytes retain their JSON meaning rather than encoding as base64.
func (server *Server) register(entry registration) {
	info := entry.tool.Describe()

	server.native.AddTool(&sdkmcp.Tool{
		Meta: entry.meta,
		Name: info.Name, Description: info.Description,
		InputSchema: info.InputSchema, OutputSchema: info.OutputSchema,
		Annotations: &sdkmcp.ToolAnnotations{
			ReadOnlyHint: info.Annotations.ReadOnly, DestructiveHint: &info.Annotations.Destructive,
			IdempotentHint: info.Annotations.Idempotent, OpenWorldHint: &info.Annotations.OpenWorld,
		},
	}, func(ctx context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return server.invoke(ctx, entry, jsontext.Value(request.Params.Arguments)), nil
	})
}
