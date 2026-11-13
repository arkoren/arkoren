package mcpserver_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/mcp"
)

func TestAvailabilityTwoPrincipalsAndPlanChanges(t *testing.T) {
	t.Parallel()

	var upgraded atomic.Bool
	tool := mcp.NewTool("premium", func(context.Context, mcp.NoInput) (message, error) {
		return message{Text: "premium"}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, message]) {
		action.Available(func(ctx context.Context) (bool, error) {
			return ctx.Value(principalKey{}) == "paid" || upgraded.Load(), nil
		})
	})

	server := managed(t, tool)
	paid := connect(t, principalHandler(server, "paid"), "2026-07-28")
	free := connect(t, principalHandler(server, "free"), "2026-07-28")

	requireCatalogSize(t, paid, 1)
	requireCatalogSize(t, free, 0)

	result, err := free.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "premium"})

	if err != nil || !result.IsError {
		t.Fatalf("hidden call = %v, %v", result, err)
	}

	upgraded.Store(true)

	requireCatalogSize(t, free, 1)

	result, err = free.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "premium"})

	if err != nil || result.IsError {
		t.Fatalf("upgraded call = %v, %v", result, err)
	}

	upgraded.Store(false)

	requireCatalogSize(t, free, 0)
	requireCatalogSize(t, paid, 1)
}

// principalHandler simulates outer authentication without modifying the MCP
// session; each HTTP request gets the caller's independently resolved identity.
func principalHandler(handler http.Handler, principal string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), principalKey{}, principal)
		handler.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// requireCatalogSize checks a complete catalog through a real SDK client. The
// adapter deliberately never emits pagination cursors, even for large catalogs.
func requireCatalogSize(t *testing.T, session *sdkmcp.ClientSession, count int) {
	t.Helper()

	result, err := session.ListTools(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	if len(result.Tools) != count || result.NextCursor != "" {
		t.Fatalf("catalog size/cursor = %d/%q, want %d/empty", len(result.Tools), result.NextCursor, count)
	}
}

func TestAvailabilityLegacyContext(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("premium", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, message]) {
		action.Available(func(ctx context.Context) (bool, error) {
			return ctx.Value(principalKey{}) == "paid", nil
		})
	})
	server := managed(t, tool)

	requireCatalogSize(t, connect(t, principalHandler(server, "paid"), "2025-11-25"), 1)
	requireCatalogSize(t, connect(t, principalHandler(server, "free"), "2025-11-25"), 0)
}

func TestAvailabilityFailureListingFailsClosed(t *testing.T) {
	t.Parallel()

	var evaluations atomic.Int64
	tool := mcp.NewTool("private", func(context.Context, mcp.NoInput) (message, error) {
		t.Error("listing or failed policy executed handler")

		return message{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, message]) {
		action.Available(func(context.Context) (bool, error) {
			evaluations.Add(1)

			return false, mcp.ToolError("secret-policy-diagnostic")
		})
	})

	server := managed(t, tool)

	if evaluations.Load() != 0 {
		t.Fatal("construction evaluated policy")
	}

	session := connect(t, server, "2026-07-28")

	result, err := session.ListTools(t.Context(), nil)

	if err == nil || result != nil {
		t.Fatalf("listing did not fail closed: %v, %v", result, err)
	}

	if strings.Contains(err.Error(), "secret-policy-diagnostic") {
		t.Fatal("policy diagnostic leaked", err)
	}

	expectFailure(t, tool, "tool failed")
}

func TestAvailabilityPanicListingIsSanitized(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("private", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, message]) {
		action.Available(func(context.Context) (bool, error) {
			panic("secret-policy-panic")
		})
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.ListTools(t.Context(), nil)

	if err == nil || result != nil {
		t.Fatalf("panic did not fail closed: %v, %v", result, err)
	}

	if strings.Contains(err.Error(), "secret-policy-panic") {
		t.Fatal("policy panic leaked", err)
	}

	expectFailure(t, tool, "tool failed")
}

func TestAvailabilityLargeCatalogIncludesNativeTools(t *testing.T) {
	t.Parallel()

	tools := make([]mcp.Tool, 0, sdkmcp.DefaultPageSize+3)

	for index := range sdkmcp.DefaultPageSize + 3 {
		tools = append(tools, mcp.NewTool(fmt.Sprintf("tool-%04d", index),
			func(context.Context, mcp.NoInput) (message, error) {
				return message{}, nil
			}, func(action *mcp.ToolBuilder[mcp.NoInput, message]) {
				action.Available(func(context.Context) (bool, error) {
					return index%2 == 0, nil
				})
			}))
	}

	server := managed(t, tools...)
	server.Server().AddTool(&sdkmcp.Tool{Name: "native", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{}, nil
		})

	session := connect(t, server, "2026-07-28")

	requireCatalogSize(t, session, (sdkmcp.DefaultPageSize+4)/2+1)

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "native"})

	if err != nil || result.IsError {
		t.Fatalf("native call = %v, %v", result, err)
	}

	if result, err := session.ListTools(t.Context(), &sdkmcp.ListToolsParams{Cursor: "not-issued"}); err == nil {
		t.Fatalf("unexpected cursor accepted: %v", result)
	}
}

func TestAvailabilityListingCancellationReachesPolicy(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	canceled := make(chan struct{})
	tool := mcp.NewTool("wait", func(context.Context, mcp.NoInput) (message, error) {
		t.Error("listing ran handler")

		return message{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, message]) {
		action.Available(func(ctx context.Context) (bool, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)

			return false, ctx.Err()
		})
	})

	session := connect(t, managed(t, tool), "2026-07-28")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	finished := make(chan error, 1)
	var worker sync.WaitGroup

	worker.Go(func() {
		_, err := session.ListTools(ctx, nil)

		finished <- err
	})

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("policy did not start")
	}

	cancel()

	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not reach policy")
	}

	if err := <-finished; err == nil {
		t.Fatal("canceled listing succeeded")
	}

	worker.Wait()
}
