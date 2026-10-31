package mcpserver_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

func TestListReportsNativeCLIMetadata(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("Exact.v1", func(_ context.Context, input message) (message, error) {
		return input, nil
	}, func(action *mcp.ToolBuilder[message, message]) {
		action.CLI("diagnostics", "show").Positional("text").Example("Hello", `{"text":"hello"}`)
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	listed, err := session.ListTools(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	info, ok := listed.Tools[0].Meta["arkoren.dev/cli"].(map[string]any)

	if !ok || info["group"] != "diagnostics" || listed.Tools[0].Name != "Exact.v1" {
		t.Fatalf("CLI report = %+v", listed.Tools[0])
	}

	examples := info["examples"].([]any)
	input := examples[0].(map[string]any)["input"].(map[string]any)

	if input["text"] != "hello" {
		t.Fatalf("example encoded incorrectly: %+v", input)
	}
}

func TestCallReportsSafeFailureOutsideOutput(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("failure", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, mcp.ToolFailure("unavailable", "capture disabled", jsontext.Value(`{"retry":true}`))
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	info, ok := result.Meta["arkoren.dev/failure"].(map[string]any)

	if !ok || info["code"] != "unavailable" || info["message"] != "capture disabled" {
		t.Fatalf("failure report = %+v", result)
	}

	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("failure violated success schema: %+v", result)
	}

	if info["details"].(map[string]any)["retry"] != true {
		t.Fatalf("details encoded incorrectly: %+v", info)
	}
}

func TestPolicyFailureOverridesSafeFailure(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("guarded", func(context.Context, mcp.NoInput) (message, error) {
		t.Fatal("handler invoked")

		return message{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, message]) {
		action.Available(func(context.Context) (bool, error) {
			return false, mcp.ToolFailure("secret", "private policy", nil)
		})
	})

	expectFailure(t, tool, "tool failed")
}

func TestInputFailureReportsOnlyPointersAndMessages(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("invalid", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, errors.Join(mcp.ErrInvalidInput, validate.Errors{
			"/name": {{Code: "required", Message: "Name is required"}},
		})
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	info := result.Meta["arkoren.dev/failure"].(map[string]any)
	fields := info["details"].(map[string]any)["fields"].(map[string]any)

	if info["code"] != "invalid_input" || fields["/name"].([]any)[0] != "Name is required" {
		t.Fatalf("validation report = %+v", info)
	}
}

// TestInputSchemaOperationalFailureStaysPrivate checks that approval markers
// from schema infrastructure cannot masquerade as safe handler diagnostics.
func TestInputSchemaOperationalFailureStaysPrivate(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("schema", func(context.Context, message) (message, error) {
		t.Error("handler invoked after failed input schema")

		return message{}, nil
	}, func(action *mcp.ToolBuilder[message, message]) {
		action.Input(validate.Struct(func(object *validate.StructBuilder[message]) {
			object.Check(func(context.Context, message) error {
				return mcp.ToolFailure("private", "private schema dependency", jsontext.Value(`{"secret":true}`))
			})
		}))
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: tool.Name(), Arguments: map[string]any{"text": "valid"},
	})

	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("schema failure = %+v", result)
	}

	info := result.Meta["arkoren.dev/failure"].(map[string]any)

	if info["code"] != "internal" || info["message"] != "tool failed" || info["details"] != nil {
		t.Fatalf("schema diagnostics disclosed: %+v", info)
	}

	if result.Content[0].(*sdkmcp.TextContent).Text != "tool failed" {
		t.Fatalf("schema text disclosed: %+v", result.Content)
	}
}

// failedEncoding is an object whose JSON marshaler fails with a marker that
// would be safe only if the application handler had returned it explicitly.
type failedEncoding struct{}

// MarshalJSON preserves an intentionally marked encoding cause. The adapter
// must classify the surrounding output failure before inspecting that marker.
func (failedEncoding) MarshalJSON() ([]byte, error) {
	return nil, mcp.ToolFailure("private", "private encoding dependency", jsontext.Value(`{"secret":true}`))
}

// TestOutputEncodingFailureOverridesSafeMarker exercises the SDK's complete
// serialization boundary, including both text content and failure metadata.
func TestOutputEncodingFailureOverridesSafeMarker(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("encode", func(context.Context, mcp.NoInput) (failedEncoding, error) {
		return failedEncoding{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, failedEncoding]) {
		action.Output(validate.Struct[failedEncoding]())
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("encoding failure = %+v", result)
	}

	info := result.Meta["arkoren.dev/failure"].(map[string]any)

	if info["code"] != "invalid_output" || info["message"] != "invalid tool output" || info["details"] != nil {
		t.Fatalf("encoding diagnostics disclosed: %+v", info)
	}

	if result.Content[0].(*sdkmcp.TextContent).Text != "invalid tool output" {
		t.Fatalf("encoding text disclosed: %+v", result.Content)
	}
}

// numericRequest binds an integer argument without a floating-point conversion.
type numericRequest struct {
	// ID deliberately exceeds the exact-integer range of float64.
	ID int64 `json:"id"`
}

// TestProtocolMetadataPreservesJSONNumbers checks both discovery examples and
// approved failure details across the SDK's real HTTP serialization boundary.
func TestProtocolMetadataPreservesJSONNumbers(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("number", func(context.Context, numericRequest) (message, error) {
		return message{}, mcp.ToolFailure("retry", "try later", jsontext.Value(
			`{"id":9007199254740993,"decimal":1.234567890123456789,"nested":[1e-1000]}`))
	}, func(action *mcp.ToolBuilder[numericRequest, message]) {
		action.CLI("numbers", "read").Example("Exact identifier", `{"id":9007199254740993}`)
	})

	handler, wire := captureProtocol(managed(t, tool))
	session := connect(t, handler, "2026-07-28")

	if _, err := session.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	if raw := *wire.Load(); !strings.Contains(raw, `"input":{"id":9007199254740993}`) {
		t.Fatalf("example number changed on wire: %s", raw)
	}

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: tool.Name(), Arguments: jsontext.Value(`{"id":9007199254740993}`),
	})

	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Fatalf("expected approved failure: %+v", result)
	}

	raw := *wire.Load()

	for _, number := range []string{`"id":9007199254740993`, `"decimal":1.234567890123456789`, `"nested":[1e-1000]`} {
		if !strings.Contains(raw, number) {
			t.Fatalf("failure number changed on wire: %s", raw)
		}
	}
}
