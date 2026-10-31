package llm_test

import (
	"context"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestGeneratePanicClosesObservation preserves the exact application panic
// while recording a bounded interruption class instead of the panic's contents.
func TestGeneratePanicClosesObservation(t *testing.T) {
	t.Parallel()

	cause := &struct{ Secret string }{Secret: "private panic value"}
	fake := llmtest.New()
	fake.ReplyWith(func(llm.Request) (llm.Response, error) {
		panic(cause)
	})

	observations := telemetrytest.New()
	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Telemetry(observations.Telemetry(t))
	})
	defer func() {
		if recovered := recover(); recovered != cause {
			t.Errorf("panic was replaced: %v", recovered)
		}

		observations.AssertSpan(t, "chat model", telemetry.String("error.type", "llm.interrupted"))
	}()

	if _, err := model.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{llm.UserText("Help.")},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestStreamConsumerPanicClosesObservation covers the iterator callback boundary
// without recovering or rewriting consumer panics inside the production stream.
func TestStreamConsumerPanicClosesObservation(t *testing.T) {
	t.Parallel()

	cause := &struct{ Secret string }{Secret: "private consumer panic"}
	fake := llmtest.New()
	fake.Reply("reply")

	observations := telemetrytest.New()
	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Telemetry(observations.Telemetry(t))
	})
	defer func() {
		if recovered := recover(); recovered != cause {
			t.Errorf("panic was replaced: %v", recovered)
		}

		observations.AssertSpan(t, "chat model", telemetry.String("error.type", "llm.interrupted"))
	}()

	for range model.Stream(t.Context(), llm.Request{Messages: []llm.Message{llm.UserText("Help.")}}) {
		panic(cause)
	}
}

// TestStreamBreakAtDoneDoesNotPublish treats every consumer break consistently,
// including a break after reading the final response rather than a text delta.
func TestStreamBreakAtDoneDoesNotPublish(t *testing.T) {
	t.Parallel()

	bus := event.New()
	published := 0

	if _, err := bus.Listen(llm.ResponseGenerated, func(context.Context, llm.ResponseGeneratedEvent) error {
		published++

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fake := llmtest.New()
	fake.Reply("reply")

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	for event, err := range model.Stream(t.Context(), llm.Request{Messages: []llm.Message{llm.UserText("Help.")}}) {
		if err != nil {
			t.Fatal(err)
		}

		if _, ok := event.(llm.Done); ok {
			break
		}
	}

	if published != 0 {
		t.Fatalf("abandoned stream published %d response events", published)
	}
}

// TestEmbedPanicClosesObservation preserves the embedding callback's panic
// identity while completing its observation without exposing the panic value.
func TestEmbedPanicClosesObservation(t *testing.T) {
	t.Parallel()

	cause := &struct{ Secret string }{Secret: "private embedding panic"}
	fake := llmtest.New()
	fake.EmbedWith(func(llm.EmbedRequest) (llm.Embeddings, error) {
		panic(cause)
	})

	observations := telemetrytest.New()
	embedder := llm.NewEmbedder(fake, "embedding-model", func(embedder *llm.EmbedderBuilder) {
		embedder.Telemetry(observations.Telemetry(t))
	})
	defer func() {
		if recovered := recover(); recovered != cause {
			t.Errorf("panic was replaced: %v", recovered)
		}

		observations.AssertSpan(t, "embeddings embedding-model", telemetry.String("error.type", "llm.interrupted"))
	}()

	if _, err := embedder.Embed(t.Context(), "Document."); err != nil {
		t.Fatal(err)
	}
}

// TestRunStreamConsumerPanicClosesAgentObservation verifies both the turn and
// enclosing agent span end when a synchronous stream consumer panics.
func TestRunStreamConsumerPanicClosesAgentObservation(t *testing.T) {
	t.Parallel()

	cause := &struct{ Secret string }{Secret: "private run consumer panic"}
	fake := llmtest.New()
	fake.Reply("reply")

	observations := telemetrytest.New()
	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Telemetry(observations.Telemetry(t))
	})

	agent := llm.NewAgent[string]("support")
	defer func() {
		if recovered := recover(); recovered != cause {
			t.Errorf("panic was replaced: %v", recovered)
		}

		observations.AssertSpan(t, "chat model", telemetry.String("error.type", "llm.interrupted"))
		observations.AssertSpan(t, "invoke_agent support", telemetry.String("error.type", "llm.interrupted"))
	}()

	for range model.RunStream(t.Context(), agent, []llm.Message{llm.UserText("Help.")}) {
		panic(cause)
	}
}
