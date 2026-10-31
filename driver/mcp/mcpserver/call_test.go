package mcpserver_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/mcp"
	"arkoren.dev/driver/mcp/mcpserver"
	"arkoren.dev/foundation/validate"
)

// TestOutputMinimumRejectsAdjacentLargeInteger checks the actual encoded value,
// not the rounded value that a generic floating-point decoder would observe.
func TestOutputMinimumRejectsAdjacentLargeInteger(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	tool := mcp.NewTool("minimum", func(context.Context, mcp.NoInput) (numericRequest, error) {
		calls.Add(1)

		return numericRequest{ID: 9007199254740992}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, numericRequest]) {
		action.Output(validate.Struct(func(object *validate.StructBuilder[numericRequest]) {
			object.Field("ID", func(identifier *validate.IntBuilder[int64]) {
				identifier.Min(9007199254740993)
			})
		}))
	})

	expectFailure(t, tool, "invalid tool output")

	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want one", calls.Load())
	}
}

// TestOutputMaximumRejectsAdjacentLargeInteger covers rounding in the opposite
// direction: a value above the exact limit must not become equal to that limit.
func TestOutputMaximumRejectsAdjacentLargeInteger(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("maximum", func(context.Context, mcp.NoInput) (numericRequest, error) {
		return numericRequest{ID: 9007199254740993}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, numericRequest]) {
		action.Output(validate.Struct(func(object *validate.StructBuilder[numericRequest]) {
			object.Field("ID", func(identifier *validate.IntBuilder[int64]) {
				identifier.Max(9007199254740992)
			})
		}))
	})

	expectFailure(t, tool, "invalid tool output")
}

// decimalOutput emits a more precise decimal than its Go field can represent.
type decimalOutput struct {
	// Value supplies the ordinary numeric field declaration used for the schema.
	Value float64 `json:"value"`
}

// MarshalJSON deliberately emits a value just above the schema's decimal bound.
func (decimalOutput) MarshalJSON() ([]byte, error) {
	return []byte(`{"value":0.10000000000000000001}`), nil
}

// TestOutputMaximumRejectsRoundedDecimal retains the output's decimal precision
// when checking a schema bound through the public HTTP protocol boundary.
func TestOutputMaximumRejectsRoundedDecimal(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("decimal", func(context.Context, mcp.NoInput) (decimalOutput, error) {
		return decimalOutput{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, decimalOutput]) {
		action.Output(validate.Struct(func(object *validate.StructBuilder[decimalOutput]) {
			object.Field("Value", func(number *validate.FloatBuilder[float64]) {
				number.Max(0.1)
			})
		}))
	})

	expectFailure(t, tool, "invalid tool output")
}

// fractionalOutput emits a fraction despite its inferred integer field schema.
type fractionalOutput struct {
	// Value supplies the ordinary integer field declaration used for the schema.
	Value int64 `json:"value"`
}

// MarshalJSON deliberately emits a fraction that rounds to one in float64.
func (fractionalOutput) MarshalJSON() ([]byte, error) {
	return []byte(`{"value":1.0000000000000000001}`), nil
}

// TestOutputIntegerRejectsRoundedFraction prevents a noninteger token from
// satisfying an integer schema merely because float64 rounds the token to one.
func TestOutputIntegerRejectsRoundedFraction(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("integer", func(context.Context, mcp.NoInput) (fractionalOutput, error) {
		return fractionalOutput{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, fractionalOutput]) {
		action.Output(validate.Struct(func(object *validate.StructBuilder[fractionalOutput]) {
			object.Field("Value", func(*validate.IntBuilder[int64]) {
			})
		}))
	})

	expectFailure(t, tool, "invalid tool output")
}

// TestOutputUniqueItemsAcceptsDistinctLargeIntegers rejects no valid output
// merely because two mathematically distinct array items share a float64 value.
func TestOutputUniqueItemsAcceptsDistinctLargeIntegers(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("unique", func(context.Context, mcp.NoInput) (map[string][]int64, error) {
		return map[string][]int64{"values": {9007199254740992, 9007199254740993}}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, map[string][]int64]) {
		action.Output(validate.Map(func(entries *validate.MapBuilder[string, []int64]) {
			entries.Values(func(items *validate.SliceBuilder[int64]) {
				items.Unique()
			})
		}))
	})

	result := callEncodedOutput(t, tool)

	if result.IsError || string(result.StructuredContent) != `{"values":[9007199254740992,9007199254740993]}` {
		t.Fatalf("distinct exact values rejected or changed: %+v", result)
	}
}

// TestOutputAcceptsExactNumberOutsideFloat64 checks raw HTTP because the SDK
// client's own generic result decoder cannot represent this valid JSON number.
func TestOutputAcceptsExactNumberOutsideFloat64(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("large", func(context.Context, mcp.NoInput) (map[string]jsontext.Value, error) {
		return map[string]jsontext.Value{"number": jsontext.Value(`1e1000`)}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, map[string]jsontext.Value]) {
		action.Output(validate.Map[string, jsontext.Value]())
	})

	result := callEncodedOutput(t, tool)

	if result.IsError || string(result.StructuredContent) != `{"number":1e1000}` {
		t.Fatalf("exact large number rejected or changed: %+v", result)
	}
}

// TestOutputSchemaSDKOverflowReturnsDeclarationError prevents the SDK's hidden
// schema probe from turning a constructor failure into a panic.
func TestOutputSchemaSDKOverflowReturnsDeclarationError(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("output-default", func(context.Context, mcp.NoInput) (map[string]jsontext.Value, error) {
		return nil, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, map[string]jsontext.Value]) {
		action.Output(validate.Map(func(entries *validate.MapBuilder[string, jsontext.Value]) {
			entries.Default(map[string]jsontext.Value{"number": jsontext.Value(`1e1000`)})
		}))
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	server, err := mcpserver.New("test", "1", func(listener *mcpserver.Builder) {
		listener.Tools(tool)
	})

	if err == nil || server != nil {
		t.Fatalf("unsupported output schema returned server=%v, err=%v", server, err)
	}
}

// TestInputSchemaSDKOverflowReturnsDeclarationError covers the SDK's independent
// input-schema probe without changing application input parsing semantics.
func TestInputSchemaSDKOverflowReturnsDeclarationError(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("input-default", func(context.Context, map[string]jsontext.Value) (message, error) {
		return message{Text: "unused"}, nil
	}, func(action *mcp.ToolBuilder[map[string]jsontext.Value, message]) {
		action.Input(validate.Map(func(entries *validate.MapBuilder[string, jsontext.Value]) {
			entries.Default(map[string]jsontext.Value{"number": jsontext.Value(`1e1000`)})
		}))
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	server, err := mcpserver.New("test", "1", func(listener *mcpserver.Builder) {
		listener.Tools(tool)
	})

	if err == nil || server != nil {
		t.Fatalf("unsupported input schema returned server=%v, err=%v", server, err)
	}
}

// encodedOutput retains the emitted bytes without the SDK client's independent
// float64 result-decoding limit. Only managed-output protocol fields are needed.
type encodedOutput struct {
	// IsError distinguishes execution failure from a successful emitted object.
	IsError bool `json:"isError"`
	// StructuredContent preserves every numeric token in the emitted object.
	StructuredContent jsontext.Value `json:"structuredContent"`
}

// callEncodedOutput invokes a managed tool through the public stateless HTTP
// protocol without normalizing either its schema or its response document.
func callEncodedOutput(t *testing.T, tool mcp.Tool) encodedOutput {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
			"name":"`+tool.Name()+`","arguments":{},
			"_meta":{
				"io.modelcontextprotocol/protocolVersion":"2026-07-28",
				"io.modelcontextprotocol/clientCapabilities":{}
			}
		}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", tool.Name())

	recorded := httptest.NewRecorder()

	managed(t, tool).ServeHTTP(recorded, request)

	if recorded.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d: %s", recorded.Code, recorded.Body)
	}

	var reply struct {
		// Result excludes generic JSON number decoding while preserving semantics.
		Result encodedOutput `json:"result"`
		// Error keeps protocol failures distinct from expected execution failures.
		Error jsontext.Value `json:"error"`
	}

	if err := json.Unmarshal(recorded.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}

	if len(reply.Error) != 0 {
		t.Fatalf("unexpected protocol error: %s", reply.Error)
	}

	return reply.Result
}

// TestProtocolPreservesRawJSONNumbers checks opaque JSON fields, not float64
// application values. No generic decoder may round an argument or result token.
func TestProtocolPreservesRawJSONNumbers(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("raw", func(
		_ context.Context, input map[string]jsontext.Value,
	) (map[string]jsontext.Value, error) {
		if string(input["integer"]) != "9007199254740993" || string(input["decimal"]) != "1.234567890123456789" {
			t.Error("raw arguments were rounded before the handler")
		}

		return input, nil
	}, func(action *mcp.ToolBuilder[map[string]jsontext.Value, map[string]jsontext.Value]) {
		action.Input(validate.Map[string, jsontext.Value]()).Output(validate.Map[string, jsontext.Value]())
	})

	handler, wire := captureProtocol(managed(t, tool))
	session := connect(t, handler, "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: tool.Name(), Arguments: jsontext.Value(`{"integer":9007199254740993,"decimal":1.234567890123456789}`),
	})

	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Fatalf("raw call failed: %+v", result)
	}

	raw := *wire.Load()

	for _, number := range []string{`"decimal":1.234567890123456789`, `"integer":9007199254740993`} {
		if !strings.Contains(raw, number) {
			t.Fatalf("structured numbers changed on wire: %s", raw)
		}
	}
}

// TestProtocolCapturesExactSchemaNumbers checks the owned schema snapshots
// across publication, reporting, retained draft edits, and SDK registration.
func TestProtocolCapturesExactSchemaNumbers(t *testing.T) {
	t.Parallel()

	schema := validate.Struct(func(object *validate.StructBuilder[numericRequest]) {
		object.Field("ID", func(identifier *validate.IntBuilder[int64]) {
			identifier.Min(9007199254740993)
		})
	})

	var retained *mcp.ToolBuilder[numericRequest, numericRequest]
	tool := mcp.NewTool("number", func(_ context.Context, input numericRequest) (numericRequest, error) {
		return input, nil
	}, func(action *mcp.ToolBuilder[numericRequest, numericRequest]) {
		retained = action
		action.Input(schema).Output(schema)
	})

	report := tool.Describe()
	clear(report.InputSchema)
	clear(report.OutputSchema)
	retained.Input(validate.Struct[numericRequest]()).Output(validate.Struct[numericRequest]())

	handler, wire := captureProtocol(managed(t, tool))
	session := connect(t, handler, "2026-07-28")

	if _, err := session.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	if raw := *wire.Load(); strings.Count(raw, `"minimum":9007199254740993`) != 2 {
		t.Fatalf("schema snapshot changed on wire: %s", raw)
	}

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: tool.Name(), Arguments: jsontext.Value(`{"id":9007199254740993}`),
	})

	if err != nil {
		t.Fatal(err)
	}

	if result.IsError || !strings.Contains(*wire.Load(), `"structuredContent":{"id":9007199254740993}`) {
		t.Fatalf("typed number changed on wire: %s", *wire.Load())
	}
}

// TestOutputValidatorResourceLimitStaysInternal distinguishes a bounded exact
// validator's resource refusal from invalid handler JSON or a schema violation.
// This valid number exceeds the documented exponent limit, not float64's range.
func TestOutputValidatorResourceLimitStaysInternal(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	tool := mcp.NewTool("large", func(context.Context, mcp.NoInput) (map[string]jsontext.Value, error) {
		calls.Add(1)

		return map[string]jsontext.Value{"number": jsontext.Value(`1e4097`)}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, map[string]jsontext.Value]) {
		action.Output(validate.Map[string, jsontext.Value]())
	})

	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("validator resource failure = %+v", result)
	}

	info := result.Meta["arkoren.dev/failure"].(map[string]any)

	if info["code"] != "internal" || info["message"] != "tool failed" || info["details"] != nil {
		t.Fatalf("validator limit misclassified as bad output: %+v", info)
	}

	if result.Content[0].(*sdkmcp.TextContent).Text != "tool failed" {
		t.Fatalf("validator diagnostics disclosed: %+v", result.Content)
	}

	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want one", calls.Load())
	}
}
