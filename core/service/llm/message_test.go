package llm_test

import (
	"encoding/json/v2"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
)

// TestUserSnapshotsPartsBeforeGeneration lets callers reuse their parts slice
// without replacing the content of an already constructed user message.
func TestUserSnapshotsPartsBeforeGeneration(t *testing.T) {
	t.Parallel()

	parts := []llm.Part{llm.Text{Text: "original"}}
	message := llm.User(parts...)
	parts[0] = llm.Text{Text: "replacement"}
	fake := llmtest.New()
	fake.Reply("received")

	if _, err := llm.New(fake, "model").Generate(t.Context(), llm.Request{
		Messages: []llm.Message{message},
	}); err != nil {
		t.Fatal(err)
	}

	requests := fake.Generated()

	if got := requests[0].Messages[0].Parts[0]; got != (llm.Text{Text: "original"}) {
		t.Fatalf("constructed message changed: %#v", got)
	}
}

// TestMessageRoundTripsEveryPartType preserves each concrete part through JSON.
// The tagged representation must reconstruct the original message contents.
func TestMessageRoundTripsEveryPartType(t *testing.T) {
	t.Parallel()

	want := llm.Message{
		Role: llm.RoleAssistant,
		Parts: []llm.Part{
			llm.Text{Text: "hello"},
			llm.Image{
				Data:      []byte{1, 2, 3},
				MediaType: "image/png",
			},
			llm.Image{URL: "https://example.com/a.png"},
			llm.Document{
				Data:      []byte("%PDF"),
				MediaType: "application/pdf",
			},
			llm.ToolCall{
				ID:        "call_1",
				Name:      "lookup-order",
				Arguments: []byte(`{"id":7}`),
			},
			llm.ToolResult{
				CallID:  "call_1",
				Content: "shipped",
				IsError: true,
			},
			llm.Reasoning{
				Text:      "thinking",
				Signature: "sig==",
			},
		},
	}

	data, err := json.Marshal(want)

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), `"arguments":{"id":7}`) || !strings.Contains(string(data), `"type":"tool_call"`) {
		t.Fatalf("encoded = %s, want raw arguments and type tags", data)
	}

	var got llm.Message

	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

// TestMessageRejectsUnknownPartType covers an unrecognized wire discriminator.
// Decoding must report an error rather than discard unsupported content.
func TestMessageRejectsUnknownPartType(t *testing.T) {
	t.Parallel()

	var message llm.Message

	if err := json.Unmarshal([]byte(`{"role":"user","parts":[{"type":"hologram"}]}`), &message); err == nil {
		t.Fatal("want an error for an unknown part type")
	}
}

// TestUserConstructors checks speaker roles and parts from the user helpers.
// Text and multipart construction must produce equivalent user messages.
func TestUserConstructors(t *testing.T) {
	t.Parallel()

	if message := llm.UserText("x"); message.Role != llm.RoleUser || len(message.Parts) != 1 {
		t.Fatalf("UserText = %#v", message)
	}

	if message := llm.User(llm.Text{Text: "a"}, llm.Image{URL: "u"}); len(message.Parts) != 2 {
		t.Fatalf("User = %#v", message)
	}
}
