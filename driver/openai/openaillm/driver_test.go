package openaillm_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
	"arkoren.dev/driver/openai/openaillm"
)

// TestChatStreamMixedOutput characterizes arrival-order deltas separately from
// index-ordered completed calls, including refusal text and usage-only chunks.
func TestChatStreamMixedOutput(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "chat_stream_mixed.sse"))

	events, err := collect(t, newDriver(t, server, openaillm.ChatCompletions), ask("hi"))

	if err != nil {
		t.Fatal(err)
	}

	want := []llm.Event{
		llm.TextDelta{Text: "A"},
		llm.ToolCallStarted{ID: "call_second", Name: "second"},
		llm.ToolCallStarted{ID: "call_first", Name: "first"},
		llm.ToolCallDelta{ID: "call_first", Arguments: "{}"},
		llm.TextDelta{Text: "B"},
		llm.ToolCallDelta{ID: "call_second", Arguments: `{"x":1}`},
		llm.Done{Response: llm.Response{
			ID: "chat-final", Model: "chat-model", StopReason: llm.StopContentFilter,
			Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
				llm.Text{Text: "AB"}, llm.Text{Text: "No."},
				llm.ToolCall{ID: "call_first", Name: "first", Arguments: []byte("{}")},
				llm.ToolCall{ID: "call_second", Name: "second", Arguments: []byte(`{"x":1}`)},
			}},
			Usage: llm.Usage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, CacheWriteTokens: 2, ReasoningTokens: 4},
		}},
	}

	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

// TestResponsesStreamMixedOutput preserves both reasoning delta spellings and
// call-ID fallback, while the terminal snapshot controls completed content.
func TestResponsesStreamMixedOutput(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "responses_stream_mixed.sse"))

	events, err := collect(t, newDriver(t, server, openaillm.Responses), ask("hi"))

	if err != nil {
		t.Fatal(err)
	}

	want := []llm.Event{
		llm.ReasoningDelta{Text: "Summary"},
		llm.ReasoningDelta{Text: "Thought"},
		llm.ToolCallDelta{ID: "orphan", Arguments: "{}"},
		llm.ToolCallStarted{ID: "call", Name: "lookup"},
		llm.ToolCallDelta{ID: "call", Arguments: "{}"},
		llm.TextDelta{Text: "partial"},
		llm.Done{Response: llm.Response{
			ID: "resp-mixed", Model: "responses-model", StopReason: llm.StopMaxTokens,
			Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
				llm.Reasoning{Text: "First\n\nSecond", Signature: "rs:opaque:state"},
				llm.Text{Text: "final"}, llm.Text{Text: "No."},
				llm.ToolCall{ID: "call", Name: "lookup", Arguments: []byte("{}")},
			}},
			Usage: llm.Usage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, CacheWriteTokens: 2, ReasoningTokens: 4},
		}},
	}

	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

// TestChatStreamHookSeesConvertedRequest checks the hook runs after tool and
// schema conversion, while streaming still requests final usage.
func TestChatStreamHookSeesConvertedRequest(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "chat_stream_text.sse"))
	calls := 0
	driver := newDriver(t, server, openaillm.ChatCompletions, func(driver *openaillm.Builder) {
		driver.ChatHook(func(params *openai.ChatCompletionNewParams) {
			calls++

			if params.Tools[0].OfFunction.Function.Name != "lookup_code" {
				t.Error("hook did not see converted tool")
			}

			if params.ResponseFormat.OfJSONSchema == nil {
				t.Error("hook did not see converted output schema")
			}

			params.Store = openai.Bool(true)
			params.StreamOptions.IncludeUsage = openai.Bool(false)
		})
	})

	request := ask("hi")
	request.Tools = []llm.ToolSpec{lookupTool}
	request.Output = []byte(`{"type":"object","properties":{}}`)

	if _, err := collect(t, driver, request); err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Fatalf("hook calls = %d, want 1", calls)
	}

	body := rec.last(t).Body

	if dig(t, body, "store") != true || dig(t, body, "stream_options", "include_usage") != true {
		t.Fatalf("body = %v", body)
	}
}

// TestResponsesStreamHookSeesConvertedRequest protects the explicit Store
// override for streaming as well as the hook's post-conversion timing.
func TestResponsesStreamHookSeesConvertedRequest(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_stream_text.sse"))
	calls := 0
	driver := newDriver(t, server, openaillm.Responses, func(driver *openaillm.Builder) {
		driver.ResponsesHook(func(params *responses.ResponseNewParams) {
			calls++

			if params.Tools[0].OfFunction.Name != "lookup_code" {
				t.Error("hook did not see converted tool")
			}

			if params.Text.Format.OfJSONSchema == nil {
				t.Error("hook did not see converted output schema")
			}

			params.Store = openai.Bool(true)
		})
	})

	request := ask("hi")
	request.Tools = []llm.ToolSpec{lookupTool}
	request.Output = []byte(`{"type":"object","properties":{}}`)

	if _, err := collect(t, driver, request); err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Fatalf("hook calls = %d, want 1", calls)
	}

	if got := dig(t, rec.last(t).Body, "store"); got != true {
		t.Fatalf("store = %v, want explicit true", got)
	}
}

// TestChatStreamBreakCancelsRequest checks early iterator exit retires the
// native HTTP stream instead of leaving its server-side request running.
func TestChatStreamBreakCancelsRequest(t *testing.T) {
	t.Parallel()

	canceled := make(chan struct{})
	server, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(fixture(t, "chat_stream_truncated.sse"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	})

	for _, err := range newDriver(t, server, openaillm.ChatCompletions).Stream(t.Context(), ask("hi")) {
		if err != nil {
			t.Fatal(err)
		}

		break
	}

	<-canceled
}

// TestStreamParentCancellationAfterDelta checks both native APIs retain an
// already delivered delta, report cancellation, and release the HTTP request.
func TestStreamParentCancellationAfterDelta(t *testing.T) {
	t.Parallel()

	for _, api := range apis {
		characterizeStreamCancellation(t, api)
	}
}

// characterizeStreamCancellation cancels from the consumer after one event.
// Channel synchronization proves cleanup without sleeps or a live provider.
func characterizeStreamCancellation(t *testing.T, api openaillm.API) {
	t.Helper()

	canceled := make(chan struct{})
	server, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		name := "responses_stream_truncated.sse"

		if api == openaillm.ChatCompletions {
			name = "chat_stream_truncated.sse"
		}

		_, _ = w.Write(fixture(t, name))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	events := 0
	var terminal error

	for _, err := range newDriver(t, server, api).Stream(ctx, ask("hi")) {
		if err != nil {
			terminal = err

			break
		}

		events++
		cancel()
	}

	if events != 1 || !errors.Is(terminal, context.Canceled) {
		t.Fatalf("api %q: events = %d, error = %v", api, events, terminal)
	}

	<-canceled
}

// TestResponsesHistoryFlushBoundaries checks adjacent text coalesces only
// until a tool or reasoning boundary, including a discarded foreign trace.
func TestResponsesHistoryFlushBoundaries(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))

	request := ask("hi")
	request.Messages = []llm.Message{{Role: llm.RoleAssistant, Parts: []llm.Part{
		llm.Text{Text: "A"}, llm.Text{Text: "B"},
		llm.Reasoning{Text: "foreign"}, llm.Text{Text: "C"},
		llm.ToolCall{ID: "call", Name: "lookup"},
		llm.Text{Text: "D"}, llm.Reasoning{Signature: "rs:opaque:state"},
	}}, llm.User(llm.Text{Text: "before"}, llm.ToolResult{CallID: "call", Content: "result"},
		llm.Text{Text: "after"})}

	if _, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	want := []any{
		map[string]any{"role": "assistant", "content": "AB"},
		map[string]any{"role": "assistant", "content": "C"},
		map[string]any{"type": "function_call", "call_id": "call", "name": "lookup", "arguments": "{}"},
		map[string]any{"role": "assistant", "content": "D"},
		map[string]any{"type": "reasoning", "id": "rs", "encrypted_content": "opaque:state", "summary": []any{}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "before"}}},
		map[string]any{"type": "function_call_output", "call_id": "call", "output": "result"},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "after"}}},
	}

	if got := dig(t, rec.last(t).Body, "input"); !reflect.DeepEqual(got, want) {
		t.Fatalf("input = %#v, want %#v", got, want)
	}
}

// TestResponsesInvalidDocumentIsLocal keeps malformed document validation at
// the conversion boundary, before the native hook or HTTP client can run.
func TestResponsesInvalidDocumentIsLocal(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))

	request := ask("hi")
	request.Messages = []llm.Message{llm.User(llm.Document{Data: []byte("data")})}

	_, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), request)

	if !errors.Is(err, llm.ErrInvalidRequest) || len(rec.all()) != 0 {
		t.Fatalf("error = %v, requests = %d", err, len(rec.all()))
	}
}

// TestErrorClassificationPrecedence retains context-length classification
// even if an endpoint also reports an ordinarily retryable server status.
func TestErrorClassificationPrecedence(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusInternalServerError, "context_length_exceeded", "too long"), llm.ErrContextLength)
}

// TestErrorInvalidSchemaIsNotUnsupported distinguishes a bad application
// schema from an endpoint that cannot accept structured-output parameters.
func TestErrorInvalidSchemaIsNotUnsupported(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, apiError(http.StatusBadRequest, "invalid_json_schema", "invalid json_schema"))

	for _, api := range apis {
		request := ask("hi")
		request.Output = []byte(`{"type":"object","properties":{}}`)

		_, err := newDriver(t, server, api).Generate(t.Context(), request)

		if !errors.Is(err, llm.ErrInvalidRequest) {
			t.Fatalf("api %q: error = %v, want invalid request", api, err)
		}
	}
}

// TestEmbedRepeatedIndexIsUnavailable rejects duplicate native indexes even
// when the number of vectors matches, without exposing a partial result.
func TestEmbedRepeatedIndexIsUnavailable(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`))
	})

	result, err := newDriver(t, server, openaillm.Responses).Embed(t.Context(), llm.EmbedRequest{
		Model: "m", Inputs: []string{"a", "b"},
	})

	if !errors.Is(err, llm.ErrUnavailable) || len(result.Vectors) != 0 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

// TestEmbedOutOfRangeIndexIsUnavailable rejects indexes beyond the submitted
// batch rather than panicking or silently losing an embedding.
func TestEmbedOutOfRangeIndexIsUnavailable(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[1]}]}`))
	})

	_, err := newDriver(t, server, openaillm.Responses).Embed(t.Context(), llm.EmbedRequest{
		Model: "m", Inputs: []string{"a"},
	})

	if !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

// TestStrictSchemaTraversesCompositions characterizes recursive normalization
// of composition branches and legacy definitions through the public driver.
func TestStrictSchemaTraversesCompositions(t *testing.T) {
	t.Parallel()

	schema := strictBody(t, `{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}}}],
		"oneOf":[{"type":"object","properties":{"b":{"type":"string"}}}],
		"allOf":[{"type":"object","properties":{"c":{"type":"string"}}}],
		"definitions":{"entry":{"type":"object","properties":{"d":{"type":"string"}}}}}`)

	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		if got := dig(t, schema, keyword, 0, "additionalProperties"); got != false {
			t.Fatalf("%s additionalProperties = %v", keyword, got)
		}
	}

	if got := dig(t, schema, "definitions", "entry", "additionalProperties"); got != false {
		t.Fatalf("definitions additionalProperties = %v", got)
	}
}

// TestResponsesRefusalOutranksLaterToolCalls preserves refusal classification
// even for an empty refusal followed by an executable call and ordinary text.
func TestResponsesRefusalOutranksLaterToolCalls(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[
			{"type":"message","content":[{"type":"refusal","refusal":""}]},
			{"type":"function_call","call_id":"call","name":"lookup","arguments":"{}"},
			{"type":"message","content":[{"type":"output_text","text":"after"}]}]}`))
	})

	response, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), ask("hi"))

	if err != nil {
		t.Fatal(err)
	}

	if response.StopReason != llm.StopRefusal {
		t.Fatalf("stop = %q, want refusal", response.StopReason)
	}

	want := []llm.Part{llm.Text{Text: ""},
		llm.ToolCall{ID: "call", Name: "lookup", Arguments: []byte("{}")}, llm.Text{Text: "after"}}

	if !reflect.DeepEqual(response.Message.Parts, want) {
		t.Fatalf("parts = %#v, want %#v", response.Message.Parts, want)
	}
}

// TestResponsesToolUseSurvivesLaterText checks text does not erase a previously
// observed tool-use stop reason in a multi-item terminal response.
func TestResponsesToolUseSurvivesLaterText(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[
			{"type":"function_call","call_id":"call","name":"lookup","arguments":"{}"},
			{"type":"message","content":[{"type":"output_text","text":"after"}]}]}`))
	})

	response, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), ask("hi"))

	if err != nil {
		t.Fatal(err)
	}

	if response.StopReason != llm.StopToolUse {
		t.Fatalf("stop = %q, want tool use", response.StopReason)
	}
}

// TestResponsesStreamFailedKeepsEarlierDeltas preserves delivered text when a
// later native failed event terminates the stream, without publishing Done.
func TestResponsesStreamFailedKeepsEarlierDeltas(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"partial"}

data: {"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded"}}}

`))
	})

	events, err := collect(t, newDriver(t, server, openaillm.Responses), ask("hi"))

	if !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("error = %v, want rate limited", err)
	}

	if !reflect.DeepEqual(events, []llm.Event{llm.TextDelta{Text: "partial"}}) {
		t.Fatalf("events = %#v, want the single preceding delta", events)
	}
}

// TestChatGenerateText checks Chat Completions request encoding and response
// metadata against a recorded text reply, without contacting a provider.
func TestChatGenerateText(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "chat_text.json"))
	driver := newDriver(t, server, openaillm.ChatCompletions)

	request := ask("Say pineapple")
	request.Instructions = "Be terse."
	request.MaxTokens = 50
	request.Reasoning = llm.EffortHigh

	response, err := driver.Generate(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	if text(response.Message) != "pineapple" || response.StopReason != llm.StopEndTurn || response.ID != "chatcmpl-1" {
		t.Errorf("response = %+v", response)
	}

	wantUsage := llm.Usage{
		InputTokens:     20,
		OutputTokens:    5,
		CacheReadTokens: 8,
		ReasoningTokens: 3,
	}

	if response.Usage != wantUsage {
		t.Errorf("usage = %+v, want %+v", response.Usage, wantUsage)
	}

	got := rec.last(t)

	if got.Path != "/chat/completions" {
		t.Errorf("path = %q", got.Path)
	}

	// A single text part goes out as a plain string for older servers.
	if dig(t, got.Body, "messages", 0, "role") != "system" || dig(t, got.Body, "messages", 0, "content") != "Be terse." ||
		dig(t, got.Body, "messages", 1, "role") != "user" || dig(t, got.Body, "messages", 1, "content") != "Say pineapple" {
		t.Errorf("messages = %v", got.Body["messages"])
	}

	if dig(t, got.Body, "max_completion_tokens") != 50.0 || dig(t, got.Body, "reasoning_effort") != "high" || has(got.Body, "stream_options") || has(got.Body, "store") {
		t.Errorf("body = %v", got.Body)
	}
}

// TestChatGenerateToolCall preserves the native call identifier, tool name, and
// JSON arguments so the facade can execute the requested tool.
func TestChatGenerateToolCall(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "chat_tool_call.json"))
	driver := newDriver(t, server, openaillm.ChatCompletions)

	request := ask("Code for Paris?")
	request.Tools = []llm.ToolSpec{lookupTool}
	request.ToolChoice = llm.ToolChoice{
		Kind: llm.ToolChoiceNamed,
		Name: "lookup_code",
	}

	response, err := driver.Generate(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	call, ok := response.Message.Parts[0].(llm.ToolCall)

	if len(response.Message.Parts) != 1 || !ok || call.ID != "call_paris" || string(call.Arguments) != `{"city":"Paris"}` || response.StopReason != llm.StopToolUse {
		t.Errorf("response = %+v", response)
	}

	body := rec.last(t).Body

	if dig(t, body, "tools", 0, "type") != "function" || dig(t, body, "tools", 0, "function", "name") != "lookup_code" {
		t.Errorf("tools = %v", body["tools"])
	}

	// Chat mode leaves schemas alone by default.
	if has(dig(t, body, "tools", 0, "function", "parameters"), "additionalProperties") || has(dig(t, body, "tools", 0, "function"), "strict") && dig(t, body, "tools", 0, "function", "strict") != false {
		t.Errorf("tool function = %v, want the schema untouched", dig(t, body, "tools", 0, "function"))
	}

	if dig(t, body, "tool_choice", "function", "name") != "lookup_code" {
		t.Errorf("tool_choice = %v", body["tool_choice"])
	}
}

// TestChatStrictSchemasOn checks default strict tool schemas on the wire.
// Optional properties must be normalized without changing the shared fixture.
func TestChatStrictSchemasOn(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "chat_text.json"))
	driver := newDriver(t, server, openaillm.ChatCompletions, func(driver *openaillm.Builder) {
		driver.StrictSchemas(true)
	})

	request := ask("Extract")
	request.Output = []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`)

	if _, err := driver.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	format := dig(t, rec.last(t).Body, "response_format")

	if dig(t, format, "type") != "json_schema" || dig(t, format, "json_schema", "strict") != true ||
		dig(t, format, "json_schema", "schema", "additionalProperties") != false {
		t.Errorf("response_format = %v", format)
	}
}

// TestChatHistoryMessages covers ordered system, user, assistant, and tool
// history encoding, preserving each role's native Chat representation.
func TestChatHistoryMessages(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "chat_text.json"))
	driver := newDriver(t, server, openaillm.ChatCompletions)

	request := ask("Code for Paris?")
	request.Messages = append(request.Messages,
		llm.Message{
			Role: llm.RoleAssistant,
			Parts: []llm.Part{
				llm.Reasoning{
					Text:      "dropped",
					Signature: "rs_x:y",
				},
				llm.Text{
					Text: "Looking.",
				},
				llm.ToolCall{
					ID:        "call_paris",
					Name:      "lookup_code",
					Arguments: []byte(`{"city":"Paris"}`),
				},
			},
		},
		llm.User(llm.ToolResult{
			CallID:  "call_paris",
			Content: "7421",
		}, llm.Text{
			Text: "thanks",
		}, llm.Image{
			URL: "https://example.com/a.png",
		}),
	)

	if _, err := driver.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	messages := dig(t, rec.last(t).Body, "messages").([]any)

	var roles []string

	for _, message := range messages {
		roles = append(roles, dig(t, message, "role").(string))
	}

	if want := []string{"user", "assistant", "tool", "user"}; !slices.Equal(roles, want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}

	assistant := messages[1]

	if dig(t, assistant, "content") != "Looking." || dig(t, assistant, "tool_calls", 0, "id") != "call_paris" ||
		dig(t, assistant, "tool_calls", 0, "function", "arguments") != `{"city":"Paris"}` {
		t.Errorf("assistant = %v", assistant)
	}

	if dig(t, messages[2], "tool_call_id") != "call_paris" || dig(t, messages[2], "content") != "7421" {
		t.Errorf("tool message = %v", messages[2])
	}

	if dig(t, messages[3], "content", 1, "image_url", "url") != "https://example.com/a.png" {
		t.Errorf("user message = %v", messages[3])
	}
}

// TestChatStopReasons checks token exhaustion is not normal completion.
// The recorded reply must retain the contract's maximum-token stop reason.
func TestChatStopReasons(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "chat_length.json"))

	response, err := newDriver(t, server, openaillm.ChatCompletions).Generate(t.Context(), ask("hi"))

	if err != nil {
		t.Fatal(err)
	}

	if response.StopReason != llm.StopMaxTokens {
		t.Errorf("%s: StopReason = %q, want %q", "chat_length.json", response.StopReason, llm.StopMaxTokens)
	}
}

// TestChatRefusalStopReason distinguishes a policy refusal from token exhaustion.
// A normal HTTP reply can carry this stop reason without an operation error.
func TestChatRefusalStopReason(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "chat_refusal.json"))

	response, err := newDriver(t, server, openaillm.ChatCompletions).Generate(t.Context(), ask("hi"))

	if err != nil {
		t.Fatal(err)
	}

	if response.StopReason != llm.StopRefusal {
		t.Errorf("%s: StopReason = %q, want %q", "chat_refusal.json", response.StopReason, llm.StopRefusal)
	}
}

// TestChatDocumentIsUnsupported rejects document parts before sending a Chat
// request, rather than silently omitting content the endpoint cannot represent.
func TestChatDocumentIsUnsupported(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "chat_text.json"))

	request := ask("read")
	request.Messages = []llm.Message{llm.User(llm.Document{
		Data:      []byte("pdf"),
		MediaType: "application/pdf",
	})}

	_, err := newDriver(t, server, openaillm.ChatCompletions).Generate(t.Context(), request)

	if !errors.Is(err, llm.ErrUnsupported) || len(rec.all()) != 0 {
		t.Errorf("error = %v, requests = %d, want llm.ErrUnsupported before any request", err, len(rec.all()))
	}
}

// TestChatUnknownPartIsUnsupported rejects unrecognized message parts locally.
// The returned error must match the contract's unsupported-operation sentinel.
func TestChatUnknownPartIsUnsupported(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "chat_text.json"))
	driver := newDriver(t, server, openaillm.ChatCompletions)

	_, err := driver.Generate(t.Context(), llm.Request{
		Model: "m",
		Messages: []llm.Message{llm.User(unknownPart{
			Text: llm.Text{
				Text: "x",
			},
		})},
	})

	if !errors.Is(err, llm.ErrUnsupported) {
		t.Errorf("error = %v, want llm.ErrUnsupported", err)
	}
}

// TestChatHookEditsParams verifies native hooks run per request, not during
// construction, and their edits reach the encoded SDK request.
func TestChatHookEditsParams(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "chat_text.json"))
	calls := 0
	driver := newDriver(t, server, openaillm.ChatCompletions, func(driver *openaillm.Builder) {
		driver.ChatHook(func(params *openai.ChatCompletionNewParams) {
			calls++
			params.Temperature = openai.Float(0.5)
		})
	})

	if calls != 0 {
		t.Fatal("native hook ran during construction")
	}

	if _, err := driver.Generate(t.Context(), ask("hi")); err != nil {
		t.Fatal(err)
	}

	if got := dig(t, rec.last(t).Body, "temperature"); got != 0.5 || calls != 1 {
		t.Errorf("temperature = %v, want 0.5", got)
	}
}

// TestChatStreamText replays SSE text chunks and checks the final response.
// Delta order and usage must survive conversion into contract events.
func TestChatStreamText(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "chat_stream_text.sse"))
	driver := newDriver(t, server, openaillm.ChatCompletions)

	events, err := collect(t, driver, ask("pineapple"))

	if err != nil {
		t.Fatal(err)
	}

	if len(events) != 3 || events[0] != (llm.TextDelta{
		Text: "pine",
	}) || events[1] != (llm.TextDelta{
		Text: "apple",
	}) {
		t.Fatalf("events = %+v", events)
	}

	done := events[2].(llm.Done)

	if text(done.Response.Message) != "pineapple" || done.Response.StopReason != llm.StopEndTurn ||
		done.Response.Usage.InputTokens != 4 || done.Response.Usage.OutputTokens != 2 || done.Response.ID != "chatcmpl-s1" {
		t.Errorf("done = %+v", done)
	}

	body := rec.last(t).Body

	if dig(t, body, "stream") != true || dig(t, body, "stream_options", "include_usage") != true {
		t.Errorf("body = %v, want stream with include_usage", body)
	}
}

// TestChatStreamToolCall assembles fragmented tool arguments while retaining
// started/delta event order and the completed call in the final response.
func TestChatStreamToolCall(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "chat_stream_tool_call.sse"))
	driver := newDriver(t, server, openaillm.ChatCompletions)

	events, err := collect(t, driver, ask("Paris?"))

	if err != nil {
		t.Fatal(err)
	}

	if len(events) != 4 {
		t.Fatalf("events = %+v", events)
	}

	if events[0] != (llm.ToolCallStarted{
		ID:   "call_s1",
		Name: "lookup_code",
	}) ||
		events[1] != (llm.ToolCallDelta{
			ID:        "call_s1",
			Arguments: `{"city":`,
		}) ||
		events[2] != (llm.ToolCallDelta{
			ID:        "call_s1",
			Arguments: `"Paris"}`,
		}) {
		t.Errorf("events = %+v", events)
	}

	done := events[3].(llm.Done)
	call, ok := done.Response.Message.Parts[0].(llm.ToolCall)

	if !ok || call.ID != "call_s1" || string(call.Arguments) != `{"city":"Paris"}` || done.Response.StopReason != llm.StopToolUse {
		t.Errorf("done = %+v", done)
	}
}

// TestChatStreamTruncatedIsUnavailable distinguishes incomplete SSE delivery
// from successful completion while retaining deltas already received.
func TestChatStreamTruncatedIsUnavailable(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "chat_stream_truncated.sse"))

	_, err := collect(t, newDriver(t, server, openaillm.ChatCompletions), ask("hi"))

	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("error = %v, want llm.ErrUnavailable", err)
	}
}

// conformance runs the llmtest suite against the real endpoint named by
// OPENAI_BASE_URL or authenticated by OPENAI_API_KEY (both are read by the
// SDK). OPENAI_MODEL and OPENAI_EMBEDDING_MODEL choose the models.
func conformance(t *testing.T, api openaillm.API, opts ...llmtest.Option) {
	t.Helper()

	if testing.Short() {
		t.Skip("conformance tests call a real endpoint")
	}

	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("OPENAI_BASE_URL") == "" {
		t.Skip("set OPENAI_API_KEY or OPENAI_BASE_URL to run the conformance suite")
	}

	opts = append(opts,
		llmtest.WithModel(cmp.Or(os.Getenv("OPENAI_MODEL"), "gpt-5-mini")),
		llmtest.WithEmbeddingModel(cmp.Or(os.Getenv("OPENAI_EMBEDDING_MODEL"), "text-embedding-3-small")),
	)

	llmtest.Run(t, func(t *testing.T) llm.Driver {
		driver, err := openaillm.New(func(driver *openaillm.Builder) {
			driver.API(api)
		})

		if err != nil {
			t.Fatal(err)
		}

		return driver
	}, opts...)
}

// TestConformanceResponses runs the shared LLM contract against the Responses
// endpoint; the helper controls live-provider setup and short-mode skipping.
func TestConformanceResponses(t *testing.T) {
	conformance(t, openaillm.Responses)
}

// TestConformanceChatCompletions applies the same LLM contract to Chat mode.
// Endpoint-specific unsupported capabilities remain explicit suite options.
func TestConformanceChatCompletions(t *testing.T) {
	conformance(t, openaillm.ChatCompletions, llmtest.WithoutDocuments(), llmtest.WithoutReasoning())
}

// TestNewDoesNoIO protects the constructor's network-free boundary.
// Configuring a local endpoint must not send requests before an operation.
func TestNewDoesNoIO(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "models.json"))

	if _, err := openaillm.New(func(driver *openaillm.Builder) {
		driver.BaseURL(server.URL)
		driver.APIKey("k")
	}); err != nil {
		t.Fatal(err)
	}

	if len(rec.all()) != 0 {
		t.Error("New sent a request")
	}
}

// TestNewRejectsInvalidSettings checks both constructor paths use the same
// configuration validation, even when wrapping an existing native client.
func TestNewRejectsInvalidSettings(t *testing.T) {
	t.Parallel()

	for _, configure := range []func(*openaillm.Builder){
		func(driver *openaillm.Builder) {
			driver.API(openaillm.API("unknown"))
		},
		func(driver *openaillm.Builder) {
			driver.MaxRetries(-1)
		},
		func(driver *openaillm.Builder) {
			driver.EmbedBatchSize(-1)
		},
	} {
		if _, err := openaillm.New(configure); err == nil {
			t.Error("New accepted an invalid setting")
		}

		if _, err := openaillm.NewFrom(openai.NewClient(), configure); err == nil {
			t.Error("NewFrom accepted an invalid setting")
		}
	}
}

// TestPingListsModels checks readiness uses the native model-list endpoint.
// A local server records the path without requiring provider credentials.
func TestPingListsModels(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "models.json"))

	if err := newDriver(t, server, openaillm.Responses).Ping(t.Context()); err != nil {
		t.Fatal(err)
	}

	got := rec.last(t)

	if got.Method != http.MethodGet || got.Path != "/models" || got.Authorization != "Bearer test-key" {
		t.Errorf("request = %+v", got)
	}
}

// TestPingFailure preserves backend failure classification from readiness I/O.
// A failed model listing must not be reported as a healthy provider.
func TestPingFailure(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, apiError(http.StatusUnauthorized, "invalid_api_key", "Incorrect API key provided."))

	err := newDriver(t, server, openaillm.Responses).Ping(t.Context())

	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("Ping error = %v, want llm.ErrInvalidRequest", err)
	}
}

// TestSDKRetriesAreOffByDefault counts requests after a retryable failure.
// Retry policy belongs to the caller unless native retries are configured.
func TestSDKRetriesAreOffByDefault(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, apiError(http.StatusInternalServerError, "server_error", "boom"))

	_, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), ask("hi"))

	if !errors.Is(err, llm.ErrUnavailable) || len(rec.all()) != 1 {
		t.Errorf("error = %v after %d requests, want one failed request", err, len(rec.all()))
	}
}

// TestNewFromUsesTheGivenClient protects native client identity and connection
// settings while confirming wrapper-specific settings still apply.
func TestNewFromUsesTheGivenClient(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "chat_text.json"))
	client := openai.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("from-client"), option.WithMaxRetries(0))

	driver, err := openaillm.NewFrom(client, func(driver *openaillm.Builder) {
		driver.API(openaillm.ChatCompletions)
		driver.APIKey("ignored")
		driver.BaseURL("https://must-not-be-used.invalid")
		driver.HTTPClient(&http.Client{})
		driver.Organization("ignored-org")
		driver.Project("ignored-project")
		driver.MaxRetries(0)
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Generate(t.Context(), ask("hi")); err != nil {
		t.Fatal(err)
	}

	if got := rec.last(t).Authorization; got != "Bearer from-client" {
		t.Errorf("Authorization = %q, want the client's key", got)
	}

	if len(driver.Client().Options) == 0 {
		t.Error("Client() lost the options")
	}
}

// TestNewReadsSDKEnvironmentFallbacks checks empty connection settings retain
// SDK environment lookup while all requests stay on a local fixture server.
func TestNewReadsSDKEnvironmentFallbacks(t *testing.T) {
	server, rec := serve(t, replay(t, http.StatusOK, "models.json"))
	t.Setenv("OPENAI_BASE_URL", server.URL)
	t.Setenv("OPENAI_API_KEY", "environment-key")
	driver, err := openaillm.New(func(driver *openaillm.Builder) {
		driver.APIKey("")
		driver.BaseURL("")
		driver.MaxRetries(0)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := rec.last(t).Authorization; got != "Bearer environment-key" {
		t.Fatalf("Authorization = %q", got)
	}
}

// TestOrganizationAndProjectHeaders checks optional account-routing settings
// become SDK request headers rather than altering the generated message.
func TestOrganizationAndProjectHeaders(t *testing.T) {
	t.Parallel()

	var organization, project string

	server, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		organization, project = r.Header.Get("OpenAI-Organization"), r.Header.Get("OpenAI-Project")
		replay(t, http.StatusOK, "models.json")(w, r)
	})

	driver := newDriver(t, server, openaillm.Responses, func(driver *openaillm.Builder) {
		driver.Organization("org-1")
		driver.Project("proj-1")
	})

	if err := driver.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}

	if organization != "org-1" || project != "proj-1" {
		t.Errorf("headers = %q, %q", organization, project)
	}
}

// TestBuilderPoliciesApplyInOrder combines reusable construction callbacks.
// Later edits must win without replaying earlier policies during requests.
func TestBuilderPoliciesApplyInOrder(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "models.json"))
	var retained *openaillm.Builder
	calls := 0
	driver, err := openaillm.New(func(driver *openaillm.Builder) {
		calls++
		retained = driver
		driver.BaseURL(server.URL)
		driver.APIKey("first")
	}, func(driver *openaillm.Builder) {
		calls++

		if driver != retained || calls != 2 {
			t.Fatal("callbacks did not share one ordered draft")
		}

		driver.APIKey("second")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.APIKey("later")

	if err := driver.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := rec.last(t).Authorization; got != "Bearer second" {
		t.Errorf("Authorization = %q", got)
	}
}

// failsWith asserts that, in both modes and for both Generate and Stream, a
// server answering with handler yields want, with the SDK's error kept in the
// chain.
func failsWith(t *testing.T, handler http.HandlerFunc, want error) {
	t.Helper()

	server, rec := serve(t, handler)

	for _, api := range apis {
		driver := newDriver(t, server, api)

		_, err := driver.Generate(t.Context(), ask("hi"))

		if !errors.Is(err, want) {
			t.Errorf("api %q: Generate error = %v, want %v", api, err, want)
		}

		var apiErr *openai.Error

		if !errors.As(err, &apiErr) {
			t.Errorf("api %q: the SDK error is not in the chain: %v", api, err)
		}

		if _, err := collect(t, driver, ask("hi")); !errors.Is(err, want) {
			t.Errorf("api %q: Stream error = %v, want %v", api, err, want)
		}
	}

	// SDK retries are off, so each call is one request: 2 modes x 2 calls.
	if got := len(rec.all()); got != 4 {
		t.Errorf("the server saw %d requests, want 4 (no SDK retries)", got)
	}
}

// TestErrorRateLimited maps provider throttling to the contract retry category.
// Both supported APIs must expose the same errors.Is result.
func TestErrorRateLimited(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusTooManyRequests, "rate_limit_exceeded", "Rate limit reached."), llm.ErrRateLimited)
}

// TestErrorServerErrorIsUnavailable classifies a provider server failure as
// unavailable rather than blaming the caller's request or model selection.
func TestErrorServerErrorIsUnavailable(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusInternalServerError, "server_error", "The server had an error."), llm.ErrUnavailable)
}

// TestErrorOverloadedIsUnavailable preserves retryable backend-overload meaning.
// The native error code must not escape as an unclassified failure.
func TestErrorOverloadedIsUnavailable(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusServiceUnavailable, "", "The engine is currently overloaded."), llm.ErrUnavailable)
}

// TestErrorUnknownModelIsNotFound translates the native model-not-found code.
// Callers can classify missing models without parsing provider messages.
func TestErrorUnknownModelIsNotFound(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusNotFound, "model_not_found", "The model `x` does not exist."), llm.ErrNotFound)
}

// TestErrorUnknownModelWithoutCodeIsNotFound covers compatible endpoints that
// omit structured codes while still returning a missing-model HTTP response.
func TestErrorUnknownModelWithoutCodeIsNotFound(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusNotFound, "", `model "x" not found, try pulling it first`), llm.ErrNotFound)
}

// TestErrorContextLengthExceeded identifies prompts that exceed model context.
// This must not be confused with a temporary unavailable-provider condition.
func TestErrorContextLengthExceeded(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusBadRequest, "context_length_exceeded", "This model's maximum context length is 8 tokens."), llm.ErrContextLength)
}

// TestErrorContentFilterIsRefused preserves policy refusal as its own category.
// A filtered request is neither a missing model nor a transport failure.
func TestErrorContentFilterIsRefused(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusBadRequest, "invalid_prompt", "Your prompt was flagged as potentially violating our usage policy."), llm.ErrRefused)
}

// TestErrorBadRequestIsInvalidRequest classifies malformed provider requests.
// The same contract error is available from either generation API.
func TestErrorBadRequestIsInvalidRequest(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusBadRequest, "invalid_value", "Invalid value for 'x'."), llm.ErrInvalidRequest)
}

// TestErrorUnauthorizedIsInvalidRequest treats rejected credentials as invalid
// request configuration rather than a retryable provider outage.
func TestErrorUnauthorizedIsInvalidRequest(t *testing.T) {
	t.Parallel()

	failsWith(t, apiError(http.StatusUnauthorized, "invalid_api_key", "Incorrect API key provided."), llm.ErrInvalidRequest)
}

// TestErrorEmbedMapsLikeGenerate checks embedding calls share generation's
// provider-error classification rather than introducing a separate mapping.
func TestErrorEmbedMapsLikeGenerate(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, apiError(http.StatusNotFound, "model_not_found", "The model `x` does not exist."))

	_, err := newDriver(t, server, openaillm.Responses).Embed(t.Context(), llm.EmbedRequest{
		Model:  "x",
		Inputs: []string{"a"},
	})

	if !errors.Is(err, llm.ErrNotFound) {
		t.Errorf("error = %v, want llm.ErrNotFound", err)
	}
}

// apis lists both modes, for tests whose expectation does not depend on one.
var apis = []openaillm.API{openaillm.Responses, openaillm.ChatCompletions}

// recorded is one request a test server received.
type recorded struct {
	// Method identifies the HTTP verb sent by the native SDK.
	Method string

	// Path identifies the API endpoint without the test server's base URL.
	Path string

	// Authorization captures the credential header for connection-setting tests.
	Authorization string

	// Body is the decoded JSON request inspected by endpoint-specific tests.
	Body map[string]any
}

// recorder collects the requests of a test server.
type recorder struct {
	// mu serializes server writes with test-side reads of requests.
	mu sync.Mutex

	// requests retains received requests in arrival order.
	requests []recorded
}

// all returns a copy of the requests received so far.
func (recorder *recorder) all() []recorded {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	return append([]recorded(nil), recorder.requests...)
}

// last returns the most recent request and fails the test when there is none.
func (recorder *recorder) last(t *testing.T) recorded {
	t.Helper()

	requests := recorder.all()

	if len(requests) == 0 {
		t.Fatal("the server received no request")
	}

	return requests[len(requests)-1]
}

// fixture reads a file of testdata.
func fixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))

	if err != nil {
		t.Fatal(err)
	}

	return data
}

// serve starts a server that records every request and answers it with
// handler. The handler runs after the request is recorded.
func serve(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *recorder) {
	t.Helper()

	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))

		var body map[string]any

		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body %q is not JSON: %v", raw, err)
			}
		}

		rec.mu.Lock()
		rec.requests = append(rec.requests, recorded{
			Method:        r.Method,
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			Body:          body,
		})
		rec.mu.Unlock()

		handler(w, r)
	}))
	t.Cleanup(server.Close)

	return server, rec
}

// replay serves the fixture name with status. A .sse fixture is sent as an
// event stream, anything else as JSON.
func replay(t *testing.T, status int, name string) http.HandlerFunc {
	t.Helper()

	data := fixture(t, name)
	contentType := "application/json"

	if strings.HasSuffix(name, ".sse") {
		contentType = "text/event-stream"
	}

	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}
}

// apiError serves an OpenAI error body with status.
func apiError(status int, code, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)

		body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error", "code": code, "param": nil}})
		_, _ = w.Write(body)
	}
}

// newDriver returns a driver for a test server in the given mode.
func newDriver(
	t *testing.T,
	server *httptest.Server,
	api openaillm.API,
	configure ...func(*openaillm.Builder),
) *openaillm.Driver {
	t.Helper()

	all := append([]func(*openaillm.Builder){func(driver *openaillm.Builder) {
		driver.BaseURL(server.URL)
		driver.APIKey("test-key")
		driver.API(api)
	}}, configure...)

	driver, err := openaillm.New(all...)

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// ask returns a request of one user message.
func ask(prompt string) llm.Request {
	return llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.UserText(prompt)},
	}
}

// dig walks a decoded JSON value by object keys (string) and array indexes
// (int), failing the test when the path does not exist.
func dig(t *testing.T, value any, path ...any) any {
	t.Helper()

	for _, step := range path {
		switch step := step.(type) {
		case string:
			object, ok := value.(map[string]any)

			if !ok {
				t.Fatalf("cannot read %q of %T (path %v)", step, value, path)
			}

			value, ok = object[step]

			if !ok {
				t.Fatalf("no %q in %v (path %v)", step, object, path)
			}
		case int:
			list, ok := value.([]any)

			if !ok || step >= len(list) {
				t.Fatalf("cannot read index %d of %v (path %v)", step, value, path)
			}

			value = list[step]
		}
	}

	return value
}

// has reports whether a decoded JSON object has key.
func has(value any, key string) bool {
	object, _ := value.(map[string]any)
	_, ok := object[key]

	return ok
}

// text joins the text parts of message.
func text(message llm.Message) string {
	var out strings.Builder

	for _, part := range message.Parts {
		if part, ok := part.(llm.Text); ok {
			out.WriteString(part.Text)
		}
	}

	return out.String()
}

// collect drains a stream and returns its events and the error that ended it.
func collect(t *testing.T, driver llm.Driver, request llm.Request) ([]llm.Event, error) {
	var events []llm.Event

	for event, err := range driver.Stream(t.Context(), request) {
		if err != nil {
			return events, err
		}

		events = append(events, event)
	}

	return events, nil
}

// lookupTool supplies a reusable structured tool for request and stream tests.
// Its optional verbose property also exercises strict-schema normalization.
var lookupTool = llm.ToolSpec{
	Name:        "lookup_code",
	Description: "Looks up the secret code of a city.",
	InputSchema: []byte(`{"type":"object","properties":{"city":{"type":"string"},"verbose":{"type":"boolean"}},"required":["city"]}`),
}

// TestResponsesGenerateText checks request encoding, output text, and response
// metadata against a local replay of the native Responses API.
func TestResponsesGenerateText(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))
	driver := newDriver(t, server, openaillm.Responses)

	request := ask("Say pineapple")
	request.Instructions = "Be terse."
	request.MaxTokens = 50
	request.Reasoning = llm.EffortLow

	response, err := driver.Generate(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	if text(response.Message) != "pineapple" || response.Message.Role != llm.RoleAssistant {
		t.Errorf("message = %+v", response.Message)
	}

	if response.ID != "resp_text1" || response.Model != "gpt-5.2-2026-01-01" || response.StopReason != llm.StopEndTurn {
		t.Errorf("response = %+v", response)
	}

	wantUsage := llm.Usage{
		InputTokens:      20,
		OutputTokens:     5,
		CacheReadTokens:  8,
		CacheWriteTokens: 2,
		ReasoningTokens:  3,
	}

	if response.Usage != wantUsage {
		t.Errorf("usage = %+v, want %+v", response.Usage, wantUsage)
	}

	got := rec.last(t)

	if got.Method != http.MethodPost || got.Path != "/responses" || got.Authorization != "Bearer test-key" {
		t.Errorf("request = %s %s auth %q", got.Method, got.Path, got.Authorization)
	}

	if dig(t, got.Body, "store") != false {
		t.Errorf("store = %v, want false", got.Body["store"])
	}

	if !slices.Equal(dig(t, got.Body, "include").([]any), []any{"reasoning.encrypted_content"}) {
		t.Errorf("include = %v", got.Body["include"])
	}

	if dig(t, got.Body, "model") != "gpt-5.2" || dig(t, got.Body, "instructions") != "Be terse." ||
		dig(t, got.Body, "max_output_tokens") != 50.0 || dig(t, got.Body, "reasoning", "effort") != "low" {
		t.Errorf("body = %v", got.Body)
	}

	if dig(t, got.Body, "input", 0, "role") != "user" || dig(t, got.Body, "input", 0, "content", 0, "text") != "Say pineapple" {
		t.Errorf("input = %v", got.Body["input"])
	}

	if has(got.Body, "temperature") || has(got.Body, "tools") || has(got.Body, "text") {
		t.Errorf("body has unset fields: %v", got.Body)
	}
}

// TestResponsesGenerateToolCall preserves call identity and JSON arguments for
// execution by the facade, including the contract's tool-use stop reason.
func TestResponsesGenerateToolCall(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_tool_call.json"))
	driver := newDriver(t, server, openaillm.Responses)

	request := ask("Code for Paris?")
	request.Tools = []llm.ToolSpec{lookupTool}
	request.ToolChoice = llm.ToolChoice{
		Kind: llm.ToolChoiceNamed,
		Name: "lookup_code",
	}

	response, err := driver.Generate(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	want := llm.ToolCall{
		ID:        "call_paris",
		Name:      "lookup_code",
		Arguments: []byte(`{"city":"Paris"}`),
	}

	if len(response.Message.Parts) != 1 || response.StopReason != llm.StopToolUse {
		t.Fatalf("response = %+v", response)
	}

	got, ok := response.Message.Parts[0].(llm.ToolCall)

	if !ok || got.ID != want.ID || got.Name != want.Name || string(got.Arguments) != string(want.Arguments) {
		t.Errorf("tool call = %+v, want %+v", response.Message.Parts[0], want)
	}

	body := rec.last(t).Body

	if dig(t, body, "tools", 0, "type") != "function" || dig(t, body, "tools", 0, "name") != "lookup_code" ||
		dig(t, body, "tools", 0, "strict") != true || dig(t, body, "tools", 0, "description") != lookupTool.Description {
		t.Errorf("tools = %v", body["tools"])
	}

	if dig(t, body, "tool_choice", "type") != "function" || dig(t, body, "tool_choice", "name") != "lookup_code" {
		t.Errorf("tool_choice = %v", body["tool_choice"])
	}
}

// TestResponsesToolChoiceModes checks each tool-selection policy's native JSON
// representation, keeping explicit choices distinct from provider defaults.
func TestResponsesToolChoiceModes(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))
	driver := newDriver(t, server, openaillm.Responses)

	for kind, want := range map[llm.ToolChoiceKind]string{llm.ToolChoiceNone: "none", llm.ToolChoiceRequired: "required"} {
		request := ask("hi")
		request.Tools = []llm.ToolSpec{lookupTool}
		request.ToolChoice = llm.ToolChoice{
			Kind: kind,
		}

		if _, err := driver.Generate(t.Context(), request); err != nil {
			t.Fatal(err)
		}

		if got := dig(t, rec.last(t).Body, "tool_choice"); got != want {
			t.Errorf("tool_choice for kind %q = %v, want %q", kind, got, want)
		}
	}
}

// TestResponsesHistoryItems checks that a conversation holding reasoning, a
// tool call and its result is sent as the stateless item sequence the API
// expects, with the reasoning signature split back into ID and content.
func TestResponsesHistoryItems(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))
	driver := newDriver(t, server, openaillm.Responses)

	request := ask("Code for Paris?")
	request.Messages = append(request.Messages,
		llm.Message{
			Role: llm.RoleAssistant,
			Parts: []llm.Part{
				llm.Reasoning{
					Text:      "thinking",
					Signature: "rs_abc123:gAAAAB:opaque==",
				},
				llm.Reasoning{
					Text: "foreign trace without our signature",
				},
				llm.Text{
					Text: "Let me look.",
				},
				llm.ToolCall{
					ID:        "call_paris",
					Name:      "lookup_code",
					Arguments: []byte(`{"city":"Paris"}`),
				},
			},
		},
		llm.User(llm.ToolResult{
			CallID:  "call_paris",
			Content: "7421",
		}, llm.Text{
			Text: "thanks",
		}),
	)

	if _, err := driver.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	input := dig(t, rec.last(t).Body, "input").([]any)

	var kinds []string

	for _, item := range input {
		kind, _ := item.(map[string]any)["type"].(string)

		if kind == "" || kind == "message" {
			kind = "message:" + dig(t, item, "role").(string)
		}

		kinds = append(kinds, kind)
	}

	want := []string{"message:user", "reasoning", "message:assistant", "function_call", "function_call_output", "message:user"}

	if !slices.Equal(kinds, want) {
		t.Fatalf("item kinds = %v, want %v", kinds, want)
	}

	reasoning := input[1]

	if dig(t, reasoning, "id") != "rs_abc123" || dig(t, reasoning, "encrypted_content") != "gAAAAB:opaque==" {
		t.Errorf("reasoning item = %v", reasoning)
	}

	if summary, ok := dig(t, reasoning, "summary").([]any); !ok || len(summary) != 0 {
		t.Errorf("reasoning summary = %v, want an empty list", reasoning)
	}

	if dig(t, input[2], "content") != "Let me look." {
		t.Errorf("assistant item = %v", input[2])
	}

	call, output := input[3], input[4]

	if dig(t, call, "call_id") != "call_paris" || dig(t, call, "name") != "lookup_code" || dig(t, call, "arguments") != `{"city":"Paris"}` {
		t.Errorf("function_call = %v", call)
	}

	if dig(t, output, "call_id") != "call_paris" || dig(t, output, "output") != "7421" {
		t.Errorf("function_call_output = %v", output)
	}
}

// TestResponsesImageAndDocumentItems checks multimodal user parts retain their
// media, source, and ordering when encoded as native input items.
func TestResponsesImageAndDocumentItems(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))
	driver := newDriver(t, server, openaillm.Responses)

	request := ask("look")
	request.Messages = []llm.Message{llm.User(
		llm.Image{
			Data:      []byte("png"),
			MediaType: "image/png",
		},
		llm.Image{
			URL: "https://example.com/a.jpg",
		},
		llm.Document{
			Data:      []byte("pdf"),
			MediaType: "application/pdf",
		},
	)}

	if _, err := driver.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	content := dig(t, rec.last(t).Body, "input", 0, "content")

	if dig(t, content, 0, "type") != "input_image" || dig(t, content, 0, "image_url") != "data:image/png;base64,cG5n" {
		t.Errorf("inline image = %v", dig(t, content, 0))
	}

	if dig(t, content, 1, "image_url") != "https://example.com/a.jpg" {
		t.Errorf("image URL = %v", dig(t, content, 1))
	}

	if dig(t, content, 2, "type") != "input_file" || dig(t, content, 2, "file_data") != "data:application/pdf;base64,cGRm" ||
		dig(t, content, 2, "filename") != "document.pdf" {
		t.Errorf("document = %v", dig(t, content, 2))
	}
}

// TestResponsesReasoningEncryptedContentRoundTrip preserves opaque provider
// reasoning state so a later request can continue the model's conversation.
func TestResponsesReasoningEncryptedContentRoundTrip(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_reasoning.json"))
	driver := newDriver(t, server, openaillm.Responses)

	first, err := driver.Generate(t.Context(), ask("17 * 3?"))

	if err != nil {
		t.Fatal(err)
	}

	reasoning, ok := first.Message.Parts[0].(llm.Reasoning)

	if !ok || reasoning.Text != "Multiply 17 by 3." || reasoning.Signature != "rs_abc123:gAAAAB:opaque==" {
		t.Fatalf("parts = %+v", first.Message.Parts)
	}

	if first.Usage.ReasoningTokens != 25 || text(first.Message) != "51" {
		t.Errorf("response = %+v", first)
	}

	// Sending the whole reply back yields the same ID and encrypted content.
	request := ask("17 * 3?")
	request.Messages = append(request.Messages, first.Message, llm.UserText("Add 1."))

	if _, err := driver.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	item := dig(t, rec.last(t).Body, "input", 1)

	if dig(t, item, "type") != "reasoning" || dig(t, item, "id") != "rs_abc123" || dig(t, item, "encrypted_content") != "gAAAAB:opaque==" {
		t.Errorf("echoed reasoning = %v", item)
	}
}

// TestResponsesStructuredOutputSchema normalizes optional output properties
// for strict JSON schema while retaining a nullable representation for absence.
func TestResponsesStructuredOutputSchema(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))
	driver := newDriver(t, server, openaillm.Responses)

	request := ask("Extract")
	request.Output = []byte(`{"type":"object","properties":{"name":{"type":"string"},"nick":{"type":"string"}},"required":["name"]}`)

	if _, err := driver.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	format := dig(t, rec.last(t).Body, "text", "format")

	if dig(t, format, "type") != "json_schema" || dig(t, format, "name") != "output" || dig(t, format, "strict") != true {
		t.Errorf("format = %v", format)
	}

	schema := dig(t, format, "schema")

	if dig(t, schema, "additionalProperties") != false || !slices.Equal(dig(t, schema, "required").([]any), []any{"name", "nick"}) {
		t.Errorf("schema not strict: %v", schema)
	}

	if !slices.Equal(dig(t, schema, "properties", "nick", "type").([]any), []any{"string", "null"}) {
		t.Errorf("optional property is not nullable: %v", schema)
	}
}

// TestResponsesStrictSchemasOff preserves an explicit false setting.
// The outgoing tool schema must remain untouched rather than using defaults.
func TestResponsesStrictSchemasOff(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))
	driver := newDriver(t, server, openaillm.Responses, func(driver *openaillm.Builder) {
		driver.StrictSchemas(false)
	})

	request := ask("Extract")
	request.Tools = []llm.ToolSpec{lookupTool}

	if _, err := driver.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	tool := dig(t, rec.last(t).Body, "tools", 0)

	if dig(t, tool, "strict") != false || has(dig(t, tool, "parameters"), "additionalProperties") {
		t.Errorf("tool = %v, want the schema untouched and strict false", tool)
	}
}

// TestResponsesStopReasons checks an incomplete reply reports token exhaustion.
// The provider's successful HTTP status must not imply normal completion.
func TestResponsesStopReasons(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "responses_incomplete.json"))

	response, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), ask("hi"))

	if err != nil {
		t.Fatal(err)
	}

	if response.StopReason != llm.StopMaxTokens {
		t.Errorf("%s: StopReason = %q, want %q", "responses_incomplete.json", response.StopReason, llm.StopMaxTokens)
	}
}

// TestResponsesRefusalStopReason preserves policy refusal as a response outcome.
// It must remain distinct from both token exhaustion and transport failures.
func TestResponsesRefusalStopReason(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "responses_refusal.json"))

	response, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), ask("hi"))

	if err != nil {
		t.Fatal(err)
	}

	if response.StopReason != llm.StopRefusal {
		t.Errorf("%s: StopReason = %q, want %q", "responses_refusal.json", response.StopReason, llm.StopRefusal)
	}
}

// TestResponsesFailedResponseIsError classifies an error embedded in a nominal
// HTTP success body instead of returning an empty successful response.
func TestResponsesFailedResponseIsError(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "responses_failed.json"))

	_, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), ask("hi"))

	if !errors.Is(err, llm.ErrRateLimited) {
		t.Errorf("error = %v, want llm.ErrRateLimited", err)
	}
}

// TestResponsesStructuredOutputRejectedIsUnsupported identifies compatible
// servers that reject structured-output fields despite accepting the endpoint.
func TestResponsesStructuredOutputRejectedIsUnsupported(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, apiError(http.StatusBadRequest, "", "Unknown parameter: 'text.format'."))

	request := ask("Extract")
	request.Output = []byte(`{"type":"object"}`)

	_, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), request)

	if !errors.Is(err, llm.ErrUnsupported) {
		t.Errorf("error = %v, want llm.ErrUnsupported", err)
	}
}

// TestResponsesHookEditsParams checks native parameter edits reach the wire.
// The hook must execute per request, never eagerly during construction.
func TestResponsesHookEditsParams(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))
	calls := 0
	driver := newDriver(t, server, openaillm.Responses, func(driver *openaillm.Builder) {
		driver.ResponsesHook(func(params *responses.ResponseNewParams) {
			calls++
			params.Temperature = openai.Float(0.25)
		})
	})

	if calls != 0 {
		t.Fatal("native hook ran during construction")
	}

	if _, err := driver.Generate(t.Context(), ask("hi")); err != nil {
		t.Fatal(err)
	}

	if got := dig(t, rec.last(t).Body, "temperature"); got != 0.25 || calls != 1 {
		t.Errorf("temperature = %v, want 0.25", got)
	}
}

// TestResponsesStreamText preserves SSE delta order and assembles final text.
// The completed event also retains provider identity and token usage.
func TestResponsesStreamText(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_stream_text.sse"))
	driver := newDriver(t, server, openaillm.Responses)

	events, err := collect(t, driver, ask("pineapple"))

	if err != nil {
		t.Fatal(err)
	}

	if len(events) != 3 || events[0] != (llm.TextDelta{
		Text: "pine",
	}) || events[1] != (llm.TextDelta{
		Text: "apple",
	}) {
		t.Fatalf("events = %+v", events)
	}

	done, ok := events[2].(llm.Done)

	if !ok || text(done.Response.Message) != "pineapple" || done.Response.StopReason != llm.StopEndTurn ||
		done.Response.Usage.InputTokens != 4 || done.Response.ID != "resp_s1" {
		t.Errorf("done = %+v", events[2])
	}

	if dig(t, rec.last(t).Body, "stream") != true {
		t.Errorf("stream = %v, want true", rec.last(t).Body["stream"])
	}
}

// TestResponsesHookCanOverrideHistoryStorage verifies the documented native
// escape hatch remains a caller-controlled override, not a storage guarantee.
func TestResponsesHookCanOverrideHistoryStorage(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))
	driver := newDriver(t, server, openaillm.Responses, func(driver *openaillm.Builder) {
		driver.ResponsesHook(func(params *responses.ResponseNewParams) {
			params.Store = openai.Bool(true)
		})
	})

	if _, err := driver.Generate(t.Context(), ask("hi")); err != nil {
		t.Fatal(err)
	}

	if got := dig(t, rec.last(t).Body, "store"); got != true {
		t.Fatalf("native store=%v; want explicit hook override true", got)
	}
}

// TestResponsesStreamToolCall checks incremental JSON argument delivery and
// reconstruction of the final executable tool call from streamed fragments.
func TestResponsesStreamToolCall(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "responses_stream_tool_call.sse"))
	driver := newDriver(t, server, openaillm.Responses)

	events, err := collect(t, driver, ask("Paris?"))

	if err != nil {
		t.Fatal(err)
	}

	if len(events) != 4 {
		t.Fatalf("events = %+v", events)
	}

	if events[0] != (llm.ToolCallStarted{
		ID:   "call_s1",
		Name: "lookup_code",
	}) ||
		events[1] != (llm.ToolCallDelta{
			ID:        "call_s1",
			Arguments: `{"city":`,
		}) ||
		events[2] != (llm.ToolCallDelta{
			ID:        "call_s1",
			Arguments: `"Paris"}`,
		}) {
		t.Errorf("events = %+v", events)
	}

	done := events[3].(llm.Done)
	call, ok := done.Response.Message.Parts[0].(llm.ToolCall)

	if !ok || call.ID != "call_s1" || string(call.Arguments) != `{"city":"Paris"}` || done.Response.StopReason != llm.StopToolUse {
		t.Errorf("done = %+v", done)
	}
}

// TestResponsesStreamTruncatedIsUnavailable retains delivered deltas while
// classifying premature EOF as unavailable rather than a completed response.
func TestResponsesStreamTruncatedIsUnavailable(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "responses_stream_truncated.sse"))

	events, err := collect(t, newDriver(t, server, openaillm.Responses), ask("hi"))

	if !errors.Is(err, llm.ErrUnavailable) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error = %v, want llm.ErrUnavailable wrapping io.ErrUnexpectedEOF", err)
	}

	if len(events) != 1 {
		t.Errorf("events = %+v, want the one delta before the cut", events)
	}
}

// TestResponsesStreamErrorEvent recognizes native failure events inside SSE.
// A successful HTTP stream setup must not hide a later provider failure.
func TestResponsesStreamErrorEvent(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, replay(t, http.StatusOK, "responses_stream_error.sse"))

	_, err := collect(t, newDriver(t, server, openaillm.Responses), ask("hi"))

	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("error = %v, want llm.ErrUnavailable", err)
	}
}

// TestResponsesStreamHTTPError applies ordinary provider error mapping before
// SSE delivery begins, preserving the missing-model sentinel.
func TestResponsesStreamHTTPError(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, apiError(http.StatusNotFound, "model_not_found", "The model `x` does not exist."))

	_, err := collect(t, newDriver(t, server, openaillm.Responses), ask("hi"))

	if !errors.Is(err, llm.ErrNotFound) {
		t.Errorf("error = %v, want llm.ErrNotFound", err)
	}
}

// TestResponsesStreamBreakCancelsRequest checks that leaving the loop closes
// the HTTP request: the server sees its context canceled.
func TestResponsesStreamBreakCancelsRequest(t *testing.T) {
	t.Parallel()

	canceled := make(chan struct{})
	server, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(fixture(t, "responses_stream_truncated.sse"))
		w.(http.Flusher).Flush()

		<-r.Context().Done()
		close(canceled)
	})
	driver := newDriver(t, server, openaillm.Responses)

	for _, err := range driver.Stream(t.Context(), ask("hi")) {
		if err != nil {
			t.Fatal(err)
		}

		break
	}

	<-canceled
}

// TestResponsesUnknownPartIsUnsupported rejects unrecognized content locally.
// The recorder proves an unsupported part cannot be silently sent or dropped.
func TestResponsesUnknownPartIsUnsupported(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))
	driver := newDriver(t, server, openaillm.Responses)

	_, err := driver.Generate(t.Context(), llm.Request{
		Model: "m",
		Messages: []llm.Message{llm.User(unknownPart{
			Text: llm.Text{
				Text: "x",
			},
		})},
	})

	if !errors.Is(err, llm.ErrUnsupported) {
		t.Errorf("Generate error = %v, want llm.ErrUnsupported", err)
	}

	if len(rec.all()) != 0 {
		t.Error("an unsupported request reached the server")
	}
}

// TestCanceledContext checks generation, embedding, and streaming cancellation
// across both APIs, with no request reaching the local endpoint.
func TestCanceledContext(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))

	for _, api := range apis {
		driver := newDriver(t, server, api)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		if _, err := driver.Generate(ctx, ask("hi")); !errors.Is(err, context.Canceled) {
			t.Errorf("api %q: Generate error = %v, want context.Canceled", api, err)
		}

		if _, err := driver.Embed(ctx, llm.EmbedRequest{
			Model:  "m",
			Inputs: []string{"a"},
		}); !errors.Is(err, context.Canceled) {
			t.Errorf("api %q: Embed error = %v, want context.Canceled", api, err)
		}

		for _, err := range driver.Stream(ctx, ask("hi")) {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("api %q: Stream error = %v, want context.Canceled", api, err)
			}

			break
		}
	}

	if len(rec.all()) != 0 {
		t.Error("a canceled call reached the server")
	}
}

// unknownPart is a part type outside the sealed set, built by embedding a
// known one to inherit its unexported method.
type unknownPart struct {
	// Text promotes the sealed Part method without making unknownPart a known
	// concrete content type in the driver's type switch.
	llm.Text
}

// TestNullOutputSchemaIsRejectedBeforeRequest checks JSON null is not accepted
// as an object schema and never reaches the provider's HTTP endpoint.
func TestNullOutputSchemaIsRejectedBeforeRequest(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))

	request := ask("hi")
	request.Output = []byte(`null`)

	_, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), request)

	if !errors.Is(err, llm.ErrInvalidRequest) || len(rec.all()) != 0 {
		t.Fatalf("Generate error=%v requests=%d; want invalid request before HTTP", err, len(rec.all()))
	}
}

// TestNullToolSchemaIsRejectedBeforeRequest checks the tool-schema path shares
// the same object-root boundary without issuing a native provider request.
func TestNullToolSchemaIsRejectedBeforeRequest(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))

	request := ask("hi")
	request.Tools = []llm.ToolSpec{{Name: "lookup", InputSchema: []byte(`null`)}}

	_, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), request)

	if !errors.Is(err, llm.ErrInvalidRequest) || len(rec.all()) != 0 {
		t.Fatalf("Generate error=%v requests=%d; want invalid request before HTTP", err, len(rec.all()))
	}
}

// strictBody sends schema as a tool schema through a strict Responses driver
// and returns the parameters schema that went on the wire.
func strictBody(t *testing.T, schema string) any {
	t.Helper()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))

	request := ask("hi")
	request.Tools = []llm.ToolSpec{{
		Name:        "t",
		InputSchema: []byte(schema),
	}}

	if _, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	return dig(t, rec.last(t).Body, "tools", 0, "parameters")
}

// TestStrictSchemaRequiresEveryPropertyAndForbidsExtras checks provider strict
// mode requires all properties while making originally optional values nullable.
func TestStrictSchemaRequiresEveryPropertyAndForbidsExtras(t *testing.T) {
	t.Parallel()

	schema := strictBody(t, `{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"integer"}},"required":["a"]}`)

	if !slices.Equal(dig(t, schema, "required").([]any), []any{"a", "b"}) || dig(t, schema, "additionalProperties") != false {
		t.Errorf("schema = %v", schema)
	}

	// Required properties keep their type; optional ones accept null.
	if dig(t, schema, "properties", "a", "type") != "integer" ||
		!slices.Equal(dig(t, schema, "properties", "b", "type").([]any), []any{"string", "null"}) {
		t.Errorf("properties = %v", schema)
	}
}

// TestStrictSchemaRewritesNestedObjectsAndItems applies strict normalization
// recursively to object properties and array items, not just the root schema.
func TestStrictSchemaRewritesNestedObjectsAndItems(t *testing.T) {
	t.Parallel()

	schema := strictBody(t, `{"type":"object","required":["list"],"properties":{
		"list":{"type":"array","items":{"type":"object","properties":{"x":{"type":"number"}}}},
		"inner":{"type":"object","properties":{"y":{"type":"string"}},"required":["y"]}}}`)

	item := dig(t, schema, "properties", "list", "items")

	if dig(t, item, "additionalProperties") != false || !slices.Equal(dig(t, item, "required").([]any), []any{"x"}) ||
		!slices.Equal(dig(t, item, "properties", "x", "type").([]any), []any{"number", "null"}) {
		t.Errorf("items = %v", item)
	}

	inner := dig(t, schema, "properties", "inner")

	if !slices.Equal(dig(t, inner, "type").([]any), []any{"object", "null"}) || dig(t, inner, "additionalProperties") != false {
		t.Errorf("optional nested object = %v", inner)
	}
}

// TestStrictSchemaOptionalEnumAndRefAcceptNull preserves optionality through
// enum and reference forms while also normalizing referenced definitions.
func TestStrictSchemaOptionalEnumAndRefAcceptNull(t *testing.T) {
	t.Parallel()

	schema := strictBody(t, `{"type":"object","properties":{
		"color":{"type":"string","enum":["red","blue"]},
		"ref":{"$ref":"#/$defs/thing"}},
		"$defs":{"thing":{"type":"object","properties":{"z":{"type":"string"}}}}}`)

	if !slices.Equal(dig(t, schema, "properties", "color", "enum").([]any), []any{"red", "blue", nil}) {
		t.Errorf("enum = %v", dig(t, schema, "properties", "color"))
	}

	if dig(t, schema, "properties", "ref", "anyOf", 0, "$ref") != "#/$defs/thing" || dig(t, schema, "properties", "ref", "anyOf", 1, "type") != "null" {
		t.Errorf("ref = %v", dig(t, schema, "properties", "ref"))
	}

	if dig(t, schema, "$defs", "thing", "additionalProperties") != false {
		t.Errorf("$defs were not rewritten: %v", schema)
	}
}

// TestSchemaThatIsNotAnObjectIsInvalid rejects invalid schema roots before I/O.
// No request should reach the provider when local schema decoding fails.
func TestSchemaThatIsNotAnObjectIsInvalid(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, replay(t, http.StatusOK, "responses_text.json"))

	request := ask("hi")
	request.Output = []byte(`[1,2]`)

	_, err := newDriver(t, server, openaillm.Responses).Generate(t.Context(), request)

	if err == nil || len(rec.all()) != 0 {
		t.Errorf("error = %v, requests = %d, want an error before any request", err, len(rec.all()))
	}
}
