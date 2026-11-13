package mcpserver

import (
	"context"
	"errors"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// availabilityMiddleware intercepts only tool discovery. The SDK still owns the
// complete native registry and all other methods, including guarded tool calls.
func (server *Server) availabilityMiddleware(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
	return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
		if method != "tools/list" {
			return next(ctx, method, request)
		}

		return server.listTools(ctx, request, next)
	}
}

// listTools deliberately returns one complete filtered catalog, never a page.
// The native server's maximum page size ensures filtering cannot lose entries
// at SDK page boundaries. Reject cursors because this adapter never issues one.
// The SDK still supplies native additions; they have no managed policy. Policy
// errors and panics fail the whole request, without exposing diagnostic text.
func (server *Server) listTools(
	ctx context.Context, request sdkmcp.Request, next sdkmcp.MethodHandler,
) (result sdkmcp.Result, err error) {
	defer func() {
		if cause := recover(); cause != nil {
			server.logger.ErrorContext(ctx, "mcp availability panicked", "panic", cause)
			result, err = nil, errors.New("tool listing failed")
		}
	}()

	params, _ := request.GetParams().(*sdkmcp.ListToolsParams)

	if params != nil && params.Cursor != "" {
		return nil, errors.New("tool listing does not support cursors")
	}

	result, err = next(ctx, "tools/list", request)

	if err != nil {
		return nil, err
	}

	listed := result.(*sdkmcp.ListToolsResult)
	visible, err := server.visibleTools(ctx, listed.Tools)

	if err != nil {
		server.logger.ErrorContext(ctx, "mcp availability failed", "error", err)

		return nil, errors.New("tool listing failed")
	}

	return &sdkmcp.ListToolsResult{Tools: visible}, nil
}

// visibleTools constructs request-owned storage without changing SDK metadata.
// Each managed tool is observed once; no visibility survives into another call.
func (server *Server) visibleTools(ctx context.Context, tools []*sdkmcp.Tool) ([]*sdkmcp.Tool, error) {
	visible := make([]*sdkmcp.Tool, 0, len(tools))

	for _, info := range tools {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		tool, managed := server.managed[info.Name]

		if !managed {
			visible = append(visible, info)

			continue
		}

		offered, err := tool.Available(ctx)

		if err != nil {
			return nil, err
		}

		if offered {
			visible = append(visible, info)
		}
	}

	return visible, nil
}
