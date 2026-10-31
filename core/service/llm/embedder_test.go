package llm_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// Embed captures the request and returns the scripted vectors with fixed usage.
func (driver *stubDriver) Embed(_ context.Context, request llm.EmbedRequest) (llm.Embeddings, error) {
	driver.embedded = request

	return llm.Embeddings{
		Vectors: driver.vectors,
		Usage:   llm.Usage{InputTokens: 3},
	}, driver.err
}

// TestEmbedAppliesConfigAndEmitsTelemetryAndEvent covers a successful embedding call.
// The driver request, measurements, and event must agree on the completed operation.
func TestEmbedAppliesConfigAndEmitsTelemetryAndEvent(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	bus := event.New()
	var got llm.EmbeddingsGeneratedEvent

	if _, err := bus.Listen(llm.EmbeddingsGenerated, func(_ context.Context, payload llm.EmbeddingsGeneratedEvent) error {
		got = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	driver := newStub()
	embedder := llm.NewEmbedder(driver, "embed-1", func(embedder *llm.EmbedderBuilder) {
		embedder.Dimensions(2)
		embedder.Purpose(llm.PurposeQuery)
		embedder.Telemetry(fake.Telemetry(t))
		embedder.Events(bus)
	})

	vectors, err := embedder.Embed(t.Context(), secretPrompt, "b")

	if err != nil {
		t.Fatal(err)
	}

	if len(vectors) != 2 || driver.embedded.Model != "embed-1" || driver.embedded.Dimensions != 2 || driver.embedded.Purpose != llm.PurposeQuery {
		t.Fatalf("vectors = %v, request = %+v", vectors, driver.embedded)
	}

	if got.Count != 2 || got.Usage.InputTokens != 3 || got.Model != "embed-1" {
		t.Fatalf("event = %+v", got)
	}

	fake.AssertSpan(t, "embeddings embed-1",
		telemetry.String("gen_ai.operation.name", "embeddings"),
		telemetry.Int("gen_ai.usage.input_tokens", 3))
	fake.AssertHistogram(t, "gen_ai.client.token.usage", 1, telemetry.String("gen_ai.token.type", "input"))
}

// TestEmbedRejectsInvalidCallsBeforeIO exercises embedding boundary validation.
// Invalid inputs must fail without reaching the embedding driver.
func TestEmbedRejectsInvalidCallsBeforeIO(t *testing.T) {
	t.Parallel()

	driver := newStub()

	if _, err := llm.NewEmbedder(driver, "m").Embed(t.Context()); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("no texts error = %v, want ErrInvalidRequest", err)
	}

	if _, err := llm.NewEmbedder(nil, "m").Embed(t.Context(), "x"); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("nil driver error = %v, want ErrInvalidRequest", err)
	}

	//nolint:staticcheck // a nil context is the case under test.
	if _, err := llm.NewEmbedder(driver, "m").Embed(nil, "x"); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("nil context error = %v, want ErrInvalidRequest", err)
	}

	if len(driver.embedded.Inputs) != 0 {
		t.Fatal("driver was called")
	}
}

// TestEmbedFailsOnVectorCountMismatch rejects an incomplete driver response.
// Each input must have one corresponding vector before a result is returned.
func TestEmbedFailsOnVectorCountMismatch(t *testing.T) {
	t.Parallel()

	driver := newStub()
	driver.vectors = driver.vectors[:1]

	bus := event.New()
	failed := false

	if _, err := bus.Listen(llm.EmbeddingsFailed, func(context.Context, llm.EmbeddingsFailedEvent) error {
		failed = true

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := llm.NewEmbedder(driver, "m", func(embedder *llm.EmbedderBuilder) {
		embedder.Events(bus)
	}).Embed(t.Context(), "a", "b"); err == nil || !failed {
		t.Fatalf("error = %v, failed event = %v, want both", err, failed)
	}
}
