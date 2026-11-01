package remote

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestCursorFollowUsesTailBoundedWaitAndRestartNotice checks the opted-in
// convention independently of Arkoren owners and the retired SSE protocol.
func TestCursorFollowUsesTailBoundedWaitAndRestartNotice(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	server := externalServer(&sdkmcp.Tool{Name: "cursor", InputSchema: map[string]any{"type": "object"},
		Meta: sdkmcp.Meta{"arkoren.dev/cli": map[string]any{"group": "external", "operation": "events", "cursor": true}}},
		func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			var input map[string]any

			if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
				return nil, err
			}

			if calls.Add(1) == 1 {
				if input["cursor"] != "tail" || input["wait"] != "0s" {
					t.Errorf("initial input = %v", input)
				}

				return &sdkmcp.CallToolResult{StructuredContent: map[string]any{"cursor": "opaque", "items": []any{}}}, nil
			}

			if input["cursor"] != "opaque" || input["wait"] != "25s" {
				t.Errorf("poll input = %v", input)
			}

			return &sdkmcp.CallToolResult{StructuredContent: map[string]any{"cursor": "new-epoch", "items": []any{}, "instance_changed": true}}, nil
		})

	base, err := ParseBaseURL(nativeServerURL(t, server))

	if err != nil {
		t.Fatal(err)
	}

	native := &Client{Base: base}

	if _, err := native.FetchManifest(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = native.Session.Close() })

	var notice jsontext.Value
	err = native.Follow(ctx, "cursor", []byte("{}"), func(data []byte) error {
		notice = append(notice, data...)
		cancel()

		return nil
	})

	if err != nil || string(notice) != `{"instance_changed":true}` || calls.Load() != 2 {
		t.Fatalf("notice = %s, calls = %d, err = %v", notice, calls.Load(), err)
	}
}

// externalServer registers one raw SDK tool without assuming any Arkoren
// conventions. Native fixtures can return scalar/text/multimodal MCP results.
func externalServer(tool *sdkmcp.Tool, handler sdkmcp.ToolHandler) *sdkmcp.Server {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "external", Version: "1"}, nil)
	server.AddTool(tool, handler)

	return server
}

// nativeServerURL serves an SDK server without any Arkoren metadata or owner
// fixture. Native interop tests choose its tools and protocol capabilities.
func nativeServerURL(t *testing.T, server *sdkmcp.Server) string {
	t.Helper()

	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true})
	host := httptest.NewServer(handler)
	t.Cleanup(host.Close)

	return host.URL
}
