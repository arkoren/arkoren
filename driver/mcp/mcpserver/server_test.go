package mcpserver_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/mcp"
	"arkoren.dev/driver/mcp/mcpserver"
	"arkoren.dev/foundation/validate"
)

// captureProtocol records the last complete JSON response before sending it to
// the real SDK client. Its generic decoded results use float64, so assertions
// about number fidelity must inspect wire bytes instead. This is test-only
// payload capture; the configured adapter uses nonstreaming JSON responses.
func captureProtocol(handler http.Handler) (http.Handler, *atomic.Pointer[string]) {
	response := new(atomic.Pointer[string])

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, request)

		body := recorded.Body.String()
		response.Store(&body)
		maps.Copy(writer.Header(), recorded.Header())
		writer.WriteHeader(recorded.Code)
		_, _ = writer.Write(recorded.Body.Bytes()) // Test transport reports write failures to its client.
	}), response
}

// ExampleNew calls a managed status tool through the SDK's real HTTP handler
// in process, without opening a listener or accessing the network. Production
// authentication and authorization belong outside this handler.
func ExampleNew() {
	// Status is a fixed sample object, not an application health evaluator.
	// Its boolean output avoids the client's independent generic-number limits.
	type Status struct {
		// Ready is a fixed sample value, not an actual dependency readiness check.
		Ready bool `json:"ready"`
	}

	tool := mcp.NewTool("status", func(context.Context, mcp.NoInput) (Status, error) {
		return Status{Ready: true}, nil
	})
	policy := mcpserver.BuilderFunc(func(listener *mcpserver.Builder) {
		listener.Tools(tool)
	})

	server, err := mcpserver.New("catalog", "1.0", policy)

	if err != nil {
		fmt.Println(err)

		return
	}

	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
			"name":"status","arguments":{},
			"_meta":{
				"io.modelcontextprotocol/protocolVersion":"2026-07-28",
				"io.modelcontextprotocol/clientCapabilities":{}
			}
		}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "status")

	recorded := httptest.NewRecorder()

	server.ServeHTTP(recorded, request)

	if recorded.Code != http.StatusOK {
		fmt.Println("unexpected HTTP status:", recorded.Code)

		return
	}

	var response struct {
		// Result retains the SDK's content and execution-failure representation.
		Result sdkmcp.CallToolResult `json:"result"`
	}

	if err := json.Unmarshal(recorded.Body.Bytes(), &response); err != nil {
		fmt.Println(err)

		return
	}

	if response.Result.IsError || len(response.Result.Content) != 1 {
		fmt.Println("tool failed")

		return
	}

	fmt.Println(response.Result.Content[0].(*sdkmcp.TextContent).Text)
	// Output: {"ready":true}
}

// ExampleServer_Server adds an unmanaged native SDK callback, then invokes it
// through the in-process HTTP handler. Native additions intentionally have no
// managed availability, typed parsing, redaction, or telemetry guarantees.
func ExampleServer_Server() {
	server, err := mcpserver.New("catalog", "1.0")

	if err != nil {
		fmt.Println(err)

		return
	}

	native := server.Server()
	native.AddTool(&sdkmcp.Tool{
		Name: "native.status", InputSchema: jsontext.Value(`{"type":"object"}`),
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ready"}},
		}, nil
	})

	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
			"name":"native.status","arguments":{},
			"_meta":{
				"io.modelcontextprotocol/protocolVersion":"2026-07-28",
				"io.modelcontextprotocol/clientCapabilities":{}
			}
		}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "native.status")

	recorded := httptest.NewRecorder()

	server.ServeHTTP(recorded, request)

	if recorded.Code != http.StatusOK {
		fmt.Println("unexpected HTTP status:", recorded.Code)

		return
	}

	var response struct {
		// Result retains the SDK's content and execution-failure representation.
		Result sdkmcp.CallToolResult `json:"result"`
	}

	if err := json.Unmarshal(recorded.Body.Bytes(), &response); err != nil {
		fmt.Println(err)

		return
	}

	if response.Result.IsError || len(response.Result.Content) != 1 {
		fmt.Println("tool failed")

		return
	}

	fmt.Println(response.Result.Content[0].(*sdkmcp.TextContent).Text)
	// Output: ready
}

// message is an object-shaped test result shared by protocol scenarios.
type message struct {
	// Text is intentionally required so null and missing output are testable.
	Text string `json:"text"`
}

// connect starts a real HTTP endpoint and official SDK client. Negative retries
// prevent the test client from masking a transport failure by replaying work.
func connect(t *testing.T, handler http.Handler, version string) *sdkmcp.ClientSession {
	t.Helper()

	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &sdkmcp.StreamableClientTransport{
		Endpoint: endpoint.URL, MaxRetries: -1, DisableStandaloneSSE: true,
	}, &sdkmcp.ClientSessionOptions{ProtocolVersion: version})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})

	return session
}

// managed publishes one tool with a silent local logger. It performs no I/O;
// connect owns the listener and client resources separately.
func managed(t *testing.T, tools ...mcp.Tool) *mcpserver.Server {
	t.Helper()

	server, err := mcpserver.New("test", "1", func(listener *mcpserver.Builder) {
		listener.Tools(tools...)
		listener.Logger(slog.New(slog.DiscardHandler))
	})

	if err != nil {
		t.Fatal(err)
	}

	return server
}

// expectFailure checks the execution-error shape through the real transport.
// Protocol errors are deliberately not accepted as an equivalent result.
func expectFailure(t *testing.T, tool mcp.Tool, expected string) {
	t.Helper()

	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("failure result = %+v", result)
	}

	content, ok := result.Content[0].(*sdkmcp.TextContent)

	if !ok || content.Text != expected {
		t.Fatalf("failure content = %+v, want %q", result.Content, expected)
	}

	if result.StructuredContent != nil {
		t.Fatalf("failure leaked structured output: %+v", result.StructuredContent)
	}
}

func TestModernClientListsRawSchema(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("Echo.v1", func(_ context.Context, input message) (message, error) {
		return input, nil
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	listed, err := session.ListTools(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	if len(listed.Tools) != 1 || listed.Tools[0].Name != "Echo.v1" {
		t.Fatalf("tools = %+v", listed.Tools)
	}

	schema, ok := listed.Tools[0].InputSchema.(map[string]any)

	if !ok || schema["type"] != "object" {
		t.Fatalf("schema was not raw JSON object: %#v", listed.Tools[0].InputSchema)
	}
}

func TestModernClientCallsWithStructuredOutput(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("Echo.v1", func(_ context.Context, input message) (message, error) {
		return input, nil
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: tool.Name(), Arguments: map[string]any{"text": "hello"},
	})

	if err != nil {
		t.Fatal(err)
	}

	output, ok := result.StructuredContent.(map[string]any)

	if result.IsError || !ok || output["text"] != "hello" {
		t.Fatalf("result = %+v", result)
	}

	content := result.Content[0].(*sdkmcp.TextContent)

	if content.Text != `{"text":"hello"}` {
		t.Fatalf("text = %s", content.Text)
	}
}

func TestLegacyStreamableClientCalls(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("status", func(context.Context, mcp.NoInput) (message, error) {
		return message{Text: "ready"}, nil
	})
	session := connect(t, managed(t, tool), "2025-06-18")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	if result.IsError {
		t.Fatalf("legacy call failed: %+v", result)
	}
}

func TestOrdinaryErrorStaysLocal(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("secret", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, errors.New("database password=secret")
	})

	expectFailure(t, tool, "tool failed")
}

func TestExplicitToolErrorIsExact(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("safe", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, fmt.Errorf("private context: %w", mcp.ToolError("choose another item"))
	})

	expectFailure(t, tool, "choose another item")
}

// TestToolErrorProtocolMessageIsBounded keeps application-approved text useful
// without permitting an unbounded error response. Core retains the full cause.
func TestToolErrorProtocolMessageIsBounded(t *testing.T) {
	t.Parallel()

	approved := strings.Repeat("界", 2000)
	cause := mcp.ToolError(approved)
	tool := mcp.NewTool("safe", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, fmt.Errorf("private context: %w", cause)
	})

	_, err := tool.Call(t.Context(), nil)

	if text, ok := mcp.ToolErrorMessage(err); !ok || text != approved || !errors.Is(err, cause) {
		t.Fatal("core changed the approved cause")
	}

	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("tool failure = %+v", result)
	}

	text := result.Content[0].(*sdkmcp.TextContent).Text

	if text != strings.Repeat("界", 1365) || !utf8.ValidString(text) {
		t.Fatalf("approved message was not bounded at a UTF-8 boundary: %d bytes", len(text))
	}

	info := result.Meta["arkoren.dev/failure"].(map[string]any)

	if info["message"] != text {
		t.Fatalf("metadata and text disagree: %+v", info)
	}
}

// TestEmptyToolErrorDoesNotExposeWrapper preserves explicit empty approval
// rather than replacing it with private wrapping context at the SDK boundary.
func TestEmptyToolErrorDoesNotExposeWrapper(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("empty", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, fmt.Errorf("private context: %w", mcp.ToolError(""))
	})

	expectFailure(t, tool, "")
}

// TestMalformedToolErrorStaysSafe keeps malformed approved text out of both
// JSON metadata and native content serialization, without exposing wrappers.
func TestMalformedToolErrorStaysSafe(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("malformed", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, fmt.Errorf("private context: %w", mcp.ToolError(strings.Repeat("\x80", 5000)))
	})

	expectFailure(t, tool, "tool failed")
}

func TestHandlerPanicIsSafe(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("panic", func(context.Context, mcp.NoInput) (message, error) {
		panic("private panic")
	})

	expectFailure(t, tool, "tool failed")
}

func TestInvalidInputDoesNotInvokeHandler(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	tool := mcp.NewTool("input", func(_ context.Context, input message) (message, error) {
		calls.Add(1)

		return input, nil
	})

	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: tool.Name(), Arguments: map[string]any{"unknown": "private input"},
	})

	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || calls.Load() != 0 {
		t.Fatalf("result = %+v, calls = %d", result, calls.Load())
	}

	if result.Content[0].(*sdkmcp.TextContent).Text != "invalid tool input" {
		t.Fatalf("input failure = %+v", result.Content)
	}
}

func TestUnknownToolIsProtocolError(t *testing.T) {
	t.Parallel()

	session := connect(t, managed(t), "2026-07-28")

	_, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "unknown"})

	if err == nil {
		t.Fatal("unknown tool was not a protocol error")
	}
}

func TestDuplicateRejected(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("duplicate", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, nil
	})

	server, err := mcpserver.New("test", "1", func(listener *mcpserver.Builder) {
		listener.Tools(tool, tool)
	})

	if err == nil || server != nil {
		t.Fatalf("duplicate accepted: %v, %v", server, err)
	}
}

func TestZeroToolRejected(t *testing.T) {
	t.Parallel()

	server, err := mcpserver.New("test", "1", func(listener *mcpserver.Builder) {
		listener.Tools(mcp.Tool{})
	})

	if !errors.Is(err, mcp.ErrInvalidTool) || server != nil {
		t.Fatalf("invalid tool accepted: %v, %v", server, err)
	}
}

func TestInvalidIdentityRejected(t *testing.T) {
	t.Parallel()

	server, err := mcpserver.New(" ", "1")

	if err == nil || server != nil {
		t.Fatalf("invalid identity accepted: %v, %v", server, err)
	}
}

func TestPublicationDetachesBuilder(t *testing.T) {
	t.Parallel()

	var retained *mcpserver.Builder
	tool := mcp.NewTool("original", func(context.Context, mcp.NoInput) (message, error) {
		return message{}, nil
	})
	tools := []mcp.Tool{tool}

	server, err := mcpserver.New("test", "1", func(listener *mcpserver.Builder) {
		listener.Tools(tools...)
		retained = listener
	})

	if err != nil {
		t.Fatal(err)
	}

	tools[0] = mcp.Tool{}
	retained.Tools(mcp.Tool{})

	session := connect(t, server, "2026-07-28")

	listed, err := session.ListTools(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	if len(listed.Tools) != 1 || listed.Tools[0].Name != "original" {
		t.Fatalf("publication mutated: %+v", listed.Tools)
	}
}

func TestConstructionCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != "callback" {
			t.Error("construction panic did not propagate")
		}
	}()

	_, _ = mcpserver.New("test", "1", func(*mcpserver.Builder) {
		panic("callback")
	})
}

// principalKey is application-owned context metadata, not MCP protocol state.
type principalKey struct{}

func TestOuterAuthenticationContextPreserved(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("identity", func(ctx context.Context, _ mcp.NoInput) (message, error) {
		principal, _ := ctx.Value(principalKey{}).(string)

		return message{Text: principal}, nil
	})

	server := managed(t, tool)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), principalKey{}, "alice")
		server.ServeHTTP(writer, request.WithContext(ctx))
	})

	session := connect(t, handler, "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	if result.IsError || result.StructuredContent.(map[string]any)["text"] != "alice" {
		t.Fatalf("context lost: %+v", result)
	}
}

func TestLegacyOuterAuthenticationContextPreserved(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("identity", func(ctx context.Context, _ mcp.NoInput) (message, error) {
		principal, _ := ctx.Value(principalKey{}).(string)

		return message{Text: principal}, nil
	})

	server := managed(t, tool)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), principalKey{}, "alice")
		server.ServeHTTP(writer, request.WithContext(ctx))
	})

	session := connect(t, handler, "2025-06-18")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	if result.IsError || result.StructuredContent.(map[string]any)["text"] != "alice" {
		t.Fatalf("legacy context lost: %+v", result)
	}
}

func TestConcurrentCallsDoNotReplay(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	tool := mcp.NewTool("count", func(context.Context, mcp.NoInput) (message, error) {
		calls.Add(1)

		return message{Text: "done"}, nil
	})

	session := connect(t, managed(t, tool), "2026-07-28")

	const count = 8
	failures := make(chan error, count)
	var workers sync.WaitGroup

	for range count {
		workers.Go(func() {
			result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

			if err == nil && result.IsError {
				err = errors.New("unexpected execution failure")
			}

			failures <- err
		})
	}

	workers.Wait()
	close(failures)

	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}

	if calls.Load() != count {
		t.Fatalf("calls = %d, want %d", calls.Load(), count)
	}
}

func TestRequestCancellationReachesHandler(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	canceled := make(chan struct{})
	tool := mcp.NewTool("wait", func(ctx context.Context, _ mcp.NoInput) (message, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)

		return message{}, ctx.Err()
	})

	session := connect(t, managed(t, tool), "2026-07-28")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	finished := make(chan error, 1)
	var worker sync.WaitGroup

	worker.Go(func() {
		_, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool.Name()})

		finished <- err
	})

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}

	cancel()

	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP cancellation did not reach handler")
	}

	if err := <-finished; err == nil {
		t.Fatal("canceled call succeeded")
	}

	worker.Wait()
}

func TestOutputValidationDoesNotReplaySideEffects(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	tool := mcp.NewTool("mutate", func(context.Context, mcp.NoInput) (message, error) {
		calls.Add(1)

		return message{Text: "x"}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, message]) {
		action.Output(validate.Struct(func(object *validate.StructBuilder[message]) {
			object.Field("Text", func(text *validate.StringBuilder[string]) {
				text.Min(3)
			})
		}))
	})

	expectFailure(t, tool, "invalid tool output")

	if calls.Load() != 1 {
		t.Fatalf("side effects ran %d times", calls.Load())
	}
}

func TestInputNormalizationRunsOnce(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("normalize", func(_ context.Context, input message) (message, error) {
		return input, nil
	}, func(action *mcp.ToolBuilder[message, message]) {
		action.Input(validate.Struct(func(object *validate.StructBuilder[message]) {
			object.Field("Text", func(text *validate.StringBuilder[string]) {
				text.TrimPrefix("x")
			})
		}))
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: tool.Name(), Arguments: map[string]any{"text": "xxvalue"},
	})

	if err != nil {
		t.Fatal(err)
	}

	if result.IsError || result.StructuredContent.(map[string]any)["text"] != "xvalue" {
		t.Fatalf("normalizer repeated: %+v", result)
	}
}

func TestOutputNormalizationDoesNotRun(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("output", func(context.Context, mcp.NoInput) (message, error) {
		return message{Text: "xxvalue"}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, message]) {
		action.Output(validate.Struct(func(object *validate.StructBuilder[message]) {
			object.Field("Text", func(text *validate.StringBuilder[string]) {
				text.TrimPrefix("x")
			})
		}))
	})
	session := connect(t, managed(t, tool), "2026-07-28")

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool.Name()})

	if err != nil {
		t.Fatal(err)
	}

	if result.IsError || result.StructuredContent.(map[string]any)["text"] != "xxvalue" {
		t.Fatalf("output was normalized: %+v", result)
	}
}

// nullOutput simulates a custom marshaler violating its inferred field schema.
type nullOutput struct {
	// Text is inferred as a string even though MarshalJSON emits null.
	Text string `json:"text"`
}

// MarshalJSON deliberately emits a null string to test exact wire validation.
func (nullOutput) MarshalJSON() ([]byte, error) {
	return []byte(`{"text":null}`), nil
}

func TestOutputNullRejected(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("null", func(context.Context, mcp.NoInput) (nullOutput, error) {
		return nullOutput{}, nil
	}, func(action *mcp.ToolBuilder[mcp.NoInput, nullOutput]) {
		action.Output(validate.Struct(func(object *validate.StructBuilder[nullOutput]) {
			object.Field("Text", func(*validate.StringBuilder[string]) {
			})
		}))
	})

	expectFailure(t, tool, "invalid tool output")
}

func TestBodyLimitRejectsOversizedRequest(t *testing.T) {
	t.Parallel()

	server := managed(t)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat(" ", (4<<20)+1)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	recorder := httptest.NewRecorder()

	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit status = %d: %s", recorder.Code, recorder.Body)
	}
}

func TestListDoesNotInvokeTools(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	tool := mcp.NewTool("mutate", func(context.Context, mcp.NoInput) (message, error) {
		calls.Add(1)

		return message{}, nil
	})

	server := managed(t, tool)
	server.Server().AddTool(&sdkmcp.Tool{
		Name: "native", InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)

		return &sdkmcp.CallToolResult{}, nil
	})

	session := connect(t, server, "2026-07-28")

	listed, err := session.ListTools(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 0 || len(listed.Tools) != 2 {
		t.Fatalf("list invoked tools or omitted native additions: %+v", listed.Tools)
	}
}
