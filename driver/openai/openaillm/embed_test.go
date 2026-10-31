package openaillm_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/driver/openai/openaillm"
)

// embedHandler answers an embeddings request with one vector per input whose
// first component is the input's number ("item 7" gives 7), returned in
// reverse order to prove the driver sorts by index.
func embedHandler(t *testing.T) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			// Input is the batch whose fixture vectors encode the original indexes.
			Input []string `json:"input"`
		}

		if err := json.UnmarshalRead(r.Body, &request); err != nil {
			t.Error(err)
		}

		data := make([]map[string]any, len(request.Input))

		for i, input := range request.Input {
			var number float64
			_, _ = fmt.Sscanf(input, "item %f", &number)

			data[len(request.Input)-1-i] = map[string]any{"object": "embedding", "index": i, "embedding": []float64{number, 0.5}}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"object": "list", "model": "text-embedding-3-small", "data": data,
			"usage": map[string]any{"prompt_tokens": len(request.Input), "total_tokens": len(request.Input)},
		})
	}
}

// inputs creates n numbered prompts matching embedHandler's fixture format.
// The numbers make ordering assertions independent of provider reply order.
func inputs(n int) []string {
	out := make([]string, n)

	for i := range out {
		out[i] = fmt.Sprintf("item %d", i)
	}

	return out
}

// TestEmbedBatchesKeepOrderAndSumUsage combines out-of-order batch replies into
// input order and aggregates token usage across the native requests.
func TestEmbedBatchesKeepOrderAndSumUsage(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, embedHandler(t))
	driver := newDriver(t, server, openaillm.Responses, func(driver *openaillm.Builder) {
		driver.EmbedBatchSize(2)
	})

	result, err := driver.Embed(t.Context(), llm.EmbedRequest{
		Model:      "text-embedding-3-small",
		Inputs:     inputs(5),
		Dimensions: 64,
	})

	if err != nil {
		t.Fatal(err)
	}

	requests := rec.all()

	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3 batches of 2, 2 and 1", len(requests))
	}

	if requests[0].Path != "/embeddings" || dig(t, requests[0].Body, "dimensions") != 64.0 || dig(t, requests[0].Body, "model") != "text-embedding-3-small" {
		t.Errorf("first request = %+v", requests[0])
	}

	if len(result.Vectors) != 5 || result.Usage.InputTokens != 5 {
		t.Fatalf("result = %+v", result)
	}

	for i, vector := range result.Vectors {
		if !slices.Equal(vector, []float32{float32(i), 0.5}) {
			t.Errorf("vector %d = %v, want it to belong to input %d", i, vector, i)
		}
	}
}

// TestEmbedDefaultBatchSplitsAbove2048 protects the provider's input-count
// boundary without requiring a caller-supplied batch-size override.
func TestEmbedDefaultBatchSplitsAbove2048(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, embedHandler(t))

	result, err := newDriver(t, server, openaillm.ChatCompletions).Embed(t.Context(), llm.EmbedRequest{
		Model:  "m",
		Inputs: inputs(2100),
	})

	if err != nil {
		t.Fatal(err)
	}

	requests := rec.all()

	if len(requests) != 2 || len(dig(t, requests[0].Body, "input").([]any)) != 2048 || len(dig(t, requests[1].Body, "input").([]any)) != 52 {
		t.Errorf("requests = %d, want batches of 2048 and 52", len(requests))
	}

	if len(result.Vectors) != 2100 || result.Vectors[2099][0] != 2099 {
		t.Errorf("got %d vectors", len(result.Vectors))
	}

	if has(requests[0].Body, "dimensions") {
		t.Error("dimensions was sent although none was requested")
	}
}

// TestEmbedShortReplyIsAnError rejects incomplete embedding batches rather
// than returning vectors that no longer correspond one-to-one with inputs.
func TestEmbedShortReplyIsAnError(t *testing.T) {
	t.Parallel()

	server, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"m","data":[],"usage":{"prompt_tokens":1,"total_tokens":1}}`))
	})

	_, err := newDriver(t, server, openaillm.Responses).Embed(t.Context(), llm.EmbedRequest{
		Model:  "m",
		Inputs: []string{"a"},
	})

	if err == nil {
		t.Error("Embed succeeded although the server returned no vector")
	}
}

// TestEmbedRejectsEmptyRequest validates embedding input before native I/O.
// The request recorder ensures an empty batch is never sent to the provider.
func TestEmbedRejectsEmptyRequest(t *testing.T) {
	t.Parallel()

	server, rec := serve(t, embedHandler(t))
	driver := newDriver(t, server, openaillm.Responses)

	if _, err := driver.Embed(t.Context(), llm.EmbedRequest{
		Model: "m",
	}); err == nil {
		t.Error("Embed succeeded without inputs")
	}

	if _, err := driver.Embed(t.Context(), llm.EmbedRequest{
		Inputs: []string{"a"},
	}); err == nil {
		t.Error("Embed succeeded without a model")
	}

	if len(rec.all()) != 0 {
		t.Error("an invalid request reached the server")
	}
}
