package llm_test

import (
	"encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"arkoren.dev/core/service/llm"
)

// TestEventDecodingCharacterizesToolCallFragment uses literal wire JSON rather
// than the encoder to preserve partial, not-yet-valid argument text and call ID.
func TestEventDecodingCharacterizesToolCallFragment(t *testing.T) {
	t.Parallel()

	item, err := llm.UnmarshalEvent([]byte(`{"type":"tool_call_delta","id":"call-42","arguments":"{\"number\":"}`))

	if err != nil {
		t.Fatal(err)
	}

	if item != (llm.ToolCallDelta{ID: "call-42", Arguments: `{"number":`}) {
		t.Fatalf("argument fragment changed: %#v", item)
	}
}

// TestEventDecodingCharacterizesCompletedTurn preserves every usage field and
// unknown stop-reason strings instead of normalizing raw provider observations.
func TestEventDecodingCharacterizesCompletedTurn(t *testing.T) {
	t.Parallel()

	item, err := llm.UnmarshalEvent([]byte(`{"type":"step_completed","step":2,"stop_reason":"vendor-state","usage":{"input_tokens":1,"output_tokens":2,"cache_read_tokens":3,"cache_write_tokens":4,"reasoning_tokens":5}}`))

	if err != nil {
		t.Fatal(err)
	}

	want := llm.StepCompleted{
		Step: 2, StopReason: llm.StopReason("vendor-state"),
		Usage: llm.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4, ReasoningTokens: 5},
	}

	if !reflect.DeepEqual(item, want) {
		t.Fatalf("completed-turn fields changed: %#v", item)
	}
}

// TestEventDecodingCharacterizesToolFailure preserves the error flag and ignores
// future extension members without interpreting the event as a model result.
func TestEventDecodingCharacterizesToolFailure(t *testing.T) {
	t.Parallel()

	item, err := llm.UnmarshalEvent([]byte(`{"type":"tool_finished","id":"call-42","name":"lookup","is_error":true,"future":{"version":2}}`))

	if err != nil {
		t.Fatal(err)
	}

	if item != (llm.ToolFinished{ID: "call-42", Name: "lookup", IsError: true}) {
		t.Fatalf("tool failure fields changed: %#v", item)
	}
}

// TestEventDecodingCharacterizesMissingDoneResponse distinguishes a missing
// final response from the valid zero-valued response supplied by an empty object.
func TestEventDecodingCharacterizesMissingDoneResponse(t *testing.T) {
	t.Parallel()

	if item, err := llm.UnmarshalEvent([]byte(`{"type":"done"}`)); err == nil || item != nil {
		t.Fatalf("missing final response accepted: %#v, %v", item, err)
	}
}

// TestEventDecodingCharacterizesNullDoneResponse retains explicit null's invalid
// meaning for the one event that requires a nonnull response payload.
func TestEventDecodingCharacterizesNullDoneResponse(t *testing.T) {
	t.Parallel()

	if item, err := llm.UnmarshalEvent([]byte(`{"type":"done","response":null}`)); err == nil || item != nil {
		t.Fatalf("null final response accepted: %#v, %v", item, err)
	}
}

// TestEventDecodingCharacterizesEmptyDoneResponse preserves acceptance of a
// present response object even when the provider supplied no response fields.
func TestEventDecodingCharacterizesEmptyDoneResponse(t *testing.T) {
	t.Parallel()

	item, err := llm.UnmarshalEvent([]byte(`{"type":"done","response":{}}`))

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(item, llm.Done{Response: llm.Response{}}) {
		t.Fatalf("zero response object changed: %#v", item)
	}
}

// TestEventDecodingCharacterizesIncidentalNullResponse scopes required payload
// validation to Done rather than rejecting unrelated event records containing null.
func TestEventDecodingCharacterizesIncidentalNullResponse(t *testing.T) {
	t.Parallel()

	item, err := llm.UnmarshalEvent([]byte(`{"type":"text_delta","text":"hello","response":null}`))

	if err != nil {
		t.Fatal(err)
	}

	if item != (llm.TextDelta{Text: "hello"}) {
		t.Fatalf("incidental response changed text event: %#v", item)
	}
}

// TestEventDecodingCharacterizesFieldTypeFailure preserves JSON error identity
// rather than reclassifying malformed typed fields as unknown event tags.
func TestEventDecodingCharacterizesFieldTypeFailure(t *testing.T) {
	t.Parallel()

	item, err := llm.UnmarshalEvent([]byte(`{"type":"future-event","step":"invalid"}`))

	if _, ok := errors.AsType[*json.SemanticError](err); !ok || item != nil {
		t.Fatalf("typed JSON failure changed: %#v, %v", item, err)
	}
}

// TestEventRoundTripsEveryType checks the tagged JSON of every event variant.
// Reconstructed events must retain their payloads and concrete types.
func TestEventRoundTripsEveryType(t *testing.T) {
	t.Parallel()

	response := llm.Response{
		ID:         "r1",
		Model:      "m",
		StopReason: llm.StopToolUse,
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Parts: []llm.Part{llm.ToolCall{
				ID:        "c",
				Name:      "n",
				Arguments: []byte(`{}`),
			}},
		},
		Usage: llm.Usage{
			InputTokens:  1,
			OutputTokens: 2,
		},
	}

	for _, want := range []llm.Event{
		llm.TextDelta{Text: "a"},
		llm.ReasoningDelta{Text: "b"},
		llm.ToolCallStarted{
			ID:   "c",
			Name: "n",
		},
		llm.ToolCallDelta{
			ID:        "c",
			Arguments: `{"x"`,
		},
		llm.Done{Response: response},
	} {
		data, err := json.Marshal(want)

		if err != nil {
			t.Fatal(err)
		}

		got, err := llm.UnmarshalEvent(data)

		if err != nil {
			t.Fatalf("%s: %v", data, err)
		}

		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip = %#v, want %#v", got, want)
		}
	}
}

// TestUnmarshalEventRejectsUnknownType covers an unsupported event discriminator.
// The decoder must reject it instead of inventing a zero event.
func TestUnmarshalEventRejectsUnknownType(t *testing.T) {
	t.Parallel()

	if _, err := llm.UnmarshalEvent([]byte(`{"type":"nope"}`)); err == nil {
		t.Fatal("want an error for an unknown event type")
	}
}
