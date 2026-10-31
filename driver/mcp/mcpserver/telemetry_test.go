package mcpserver_test

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/driver/mcp/mcpserver"
)

func TestTelemetryExecutionErrorsArePayloadFree(t *testing.T) {
	t.Parallel()

	store := memorytelemetry.New()
	service := telemetry.New(store)

	if err := service.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := service.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	tool := mcp.NewTool("registered", func(context.Context, message) (message, error) {
		return message{}, mcp.ToolError("private-output")
	})

	server, err := mcpserver.New("test", "1", func(listener *mcpserver.Builder) {
		listener.Tools(tool)
		listener.Telemetry(service)
		listener.Logger(slog.New(slog.DiscardHandler))
	})

	if err != nil {
		t.Fatal(err)
	}

	session := connect(t, server, "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: tool.Name(), Arguments: map[string]any{"text": "private-input"},
	})

	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Fatal("expected execution error")
	}

	verifyFailureTelemetry(t, store)
}

// verifyFailureTelemetry checks bounded error classification and payload
// exclusion independently of the real-client setup and execution mechanics.
func verifyFailureTelemetry(t *testing.T, store *memorytelemetry.Driver) {
	t.Helper()

	spans := store.Spans()

	if len(spans) != 1 {
		t.Fatalf("spans = %+v", spans)
	}

	if spans[0].Scope != "arkoren.dev/driver/mcp" || spans[0].Name != "mcp call_tool" {
		t.Fatalf("span identity = %+v", spans[0])
	}

	if spans[0].Status != telemetry.Error {
		t.Fatalf("execution failure status = %v", spans[0].Status)
	}

	if len(spans[0].Events) != 0 || len(spans[0].Attrs) != 2 {
		t.Fatalf("unexpected span data: %+v", spans[0])
	}

	tool, _ := spans[0].Attr("arkoren.mcp.tool")
	outcome, _ := spans[0].Attr("arkoren.mcp.outcome")

	if tool.AsString() != "registered" || outcome.AsString() != "error" {
		t.Fatalf("unexpected attributes: %+v", spans[0].Attrs)
	}

	verifyErrorMetrics(t, store.Metrics())
}

// verifyErrorMetrics checks the failed-call series and serializes snapshots to
// catch accidental payload labels or inference-specific instrument names.
func verifyErrorMetrics(t *testing.T, metrics []memorytelemetry.Metric) {
	t.Helper()

	if len(metrics) != 2 {
		t.Fatalf("metrics = %+v", metrics)
	}

	point, ok := metrics[0].Point("registered", "error")

	if !ok || point.Sum != 1 {
		t.Fatalf("error counter point = %+v", metrics[0])
	}

	data, err := json.Marshal(metrics)

	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), "private-") || strings.Contains(string(data), "gen_ai.") {
		t.Fatalf("payload or inference telemetry leaked: %s", data)
	}
}
