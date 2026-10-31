package genai_test

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/llm/internal/genai"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestOpRecordsSpanDurationAndTokens checks model-call instrumentation.
// Response labels, span-only IDs, and separate token histograms must be retained.
func TestOpRecordsSpanDurationAndTokens(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	in := telemetry.NewInstruments(genai.Build("arkoren.dev/test"))

	ctx, op := genai.Begin(t.Context(), fake.Telemetry(t), in, telemetry.Client, "chat", "m1",
		telemetry.String("gen_ai.request.model", "m1"))
	_ = ctx
	op.Label(telemetry.String("gen_ai.response.model", "m1-2026"))
	op.SetAttrs(telemetry.String("gen_ai.response.id", "resp_1"))
	op.Usage(10, 4)
	op.End(t.Context(), nil)

	fake.AssertSpan(t, "chat m1",
		telemetry.String("gen_ai.operation.name", "chat"),
		telemetry.String("gen_ai.response.id", "resp_1"),
		telemetry.Int("gen_ai.usage.input_tokens", 10))
	fake.AssertHistogram(t, "gen_ai.client.operation.duration", 1, telemetry.String("gen_ai.response.model", "m1-2026"))
	fake.AssertHistogram(t, "gen_ai.client.token.usage", 1, telemetry.String("gen_ai.token.type", "input"))
	fake.AssertHistogram(t, "gen_ai.client.token.usage", 1, telemetry.String("gen_ai.token.type", "output"))
}

// TestEndClassifiesErrorWithoutText checks the model-error privacy boundary.
// A failed span must not retain prompt-bearing messages or exception events.
func TestEndClassifiesErrorWithoutText(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	in := telemetry.NewInstruments(genai.Build("arkoren.dev/test"))

	_, op := genai.Begin(t.Context(), fake.Telemetry(t), in, telemetry.Client, "chat", "m1")
	op.End(t.Context(), errors.New("secret prompt text"))

	span, _ := fake.Span("chat m1")

	if span.Status != telemetry.Error || len(span.Events) != 0 {
		t.Fatalf("status = %v, events = %d, want error and no events", span.Status, len(span.Events))
	}

	for _, attr := range span.Attrs {
		if attr.Text() == "secret prompt text" {
			t.Fatalf("attribute %q carries the error text", attr.Key())
		}
	}
}

// TestNilOpDoesNothing checks nil-safe labeling, usage, and completion.
// Disabled instrumentation must not require guards at each call site.
func TestNilOpDoesNothing(t *testing.T) {
	t.Parallel()

	var op *genai.Op

	op.Label(telemetry.String("a", "b"))
	op.SetAttrs(telemetry.String("a", "b"))
	op.Usage(1, 2)
	op.End(t.Context(), errors.New("x"))
}

// TestDisabledTelemetryReturnsNilOp checks the fast path before span creation.
// An explicitly disabled service must not create operation state.
func TestDisabledTelemetryReturnsNilOp(t *testing.T) {
	t.Parallel()

	in := telemetry.NewInstruments(genai.Build("arkoren.dev/test"))

	_, op := genai.Begin(t.Context(), telemetry.New(nil), in, telemetry.Client, "chat", "m1")

	if op != nil {
		t.Fatal("disabled telemetry returned an op")
	}
}
