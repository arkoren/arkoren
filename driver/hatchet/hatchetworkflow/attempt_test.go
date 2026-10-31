package hatchetworkflow

import (
	"context"
	"encoding/json/v2"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/core/service/workflow"
)

// TestSerializedCarrierReachesNativeTask uses separate driver/runner instances
// and capture services. Only serialized wire data reaches the worker context.
func TestSerializedCarrierReachesNativeTask(t *testing.T) {
	t.Parallel()

	producer, worker := telemetrytest.New(), telemetrytest.New()
	workerService := worker.Telemetry(t)

	step := workflow.NewStep("trace-step", func(ctx context.Context, _ string) (string, error) {
		return telemetry.SpanFromContext(ctx).Context().TraceID.String(), nil
	})
	registry, err := workflow.NewRegistry([]workflow.Definition{step}, func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(workerService)
	})

	if err != nil {
		t.Fatal(err)
	}

	backend := &fake{}
	driver := newTestDriver(backend)

	flow := workflow.New("flow", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})
	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Telemetry(producer.Telemetry(t))
	})

	if _, err := client.Start(t.Context(), flow, "run", "input", workflow.Metadata(map[string]string{"traceparent": "user-owned"})); err != nil {
		t.Fatal(err)
	}

	serialized, err := json.Marshal(backend.triggered[0])

	if err != nil {
		t.Fatal(err)
	}

	var received wire

	if err := json.Unmarshal(serialized, &received); err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Step("trace-step")

	runner := &Runner{registry: registry}

	if _, err := runner.stepFunc(handler)(&signalContext{ctx: t.Context()}, received); err != nil {
		t.Fatal(err)
	}

	start, _ := producer.Span("workflow start")
	attempt, ok := worker.Span("workflow step_attempt")

	if !ok {
		t.Fatal("missing worker span")
	}

	if attempt.Parent.SpanID != start.Context.SpanID {
		t.Fatal("native task lost carrier parent")
	}

	worker.AssertSpanCount(t, 1)
}
