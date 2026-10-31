package temporalworkflow

import (
	"context"
	"encoding/json/v2"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/core/service/workflow"
)

// TestSerializedStartCarrierReachesChildActivity exercises the real SDK argument
// converters and scheduling boundary with distinct client and worker services.
func TestSerializedStartCarrierReachesChildActivity(t *testing.T) {
	t.Parallel()

	producer, worker := telemetrytest.New(), telemetrytest.New()
	workerService := worker.Telemetry(t)

	step := workflow.NewStep("trace-step", func(ctx context.Context, _ string) (string, error) {
		return telemetry.SpanFromContext(ctx).Context().TraceID.String(), nil
	})

	child := workflow.New("trace-child", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	parent := workflow.New("trace-parent", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Child(child, "child", in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.NewRegistry([]workflow.Definition{parent, child, step}, func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(workerService)
	})

	if err != nil {
		t.Fatal(err)
	}

	backend := &fakeBackend{}
	driver := newTestDriver(t, backend)
	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Telemetry(producer.Telemetry(t))
	})

	if _, err := client.Start(t.Context(), parent, "parent", "input", workflow.Metadata(map[string]string{"arkoren_trace": "user-owned"})); err != nil {
		t.Fatal(err)
	}

	// Recreate the worker from serialized bytes, with no caller context retained.
	serialized, err := json.Marshal(backend.started.input)

	if err != nil {
		t.Fatal(err)
	}

	var received payload

	if err := json.Unmarshal(serialized, &received); err != nil {
		t.Fatal(err)
	}

	environment := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	runner := newRunner(registry, nil, driverSettings())
	runner.register(environment)

	environment.ExecuteWorkflow("arkoren-wf-trace-parent", received)

	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}

	start, ok := producer.Span("workflow start")

	if !ok {
		t.Fatal("missing start span")
	}

	attempt, ok := worker.Span("workflow step_attempt")

	if !ok {
		t.Fatal("missing attempt span")
	}

	if attempt.Parent.SpanID != start.Context.SpanID || attempt.Context.TraceID != start.Context.TraceID {
		t.Fatal("child activity lost the serialized start parent")
	}

	worker.AssertSpanCount(t, 1)
}

// TestTraceEncodingIsDeterministic protects replayed child/activity scheduling
// from Go map iteration order. Legacy payloads remain unchanged without tracing.
func TestTraceEncodingIsDeterministic(t *testing.T) {
	t.Parallel()

	input := payload(`{"v":"input"}`)
	carrier := map[string]string{"tracestate": "vendor=value", "traceparent": "parent"}

	encoded, err := encodeInput(input, carrier)

	if err != nil {
		t.Fatal(err)
	}

	for range 20 {
		next, err := encodeInput(input, carrier)

		if err != nil {
			t.Fatal(err)
		}

		if string(next) != string(encoded) {
			t.Fatal("non-deterministic input")
		}
	}

	legacy, trace, err := decodeInput(input)

	if err != nil || trace != nil || string(legacy) != string(input) {
		t.Fatalf("legacy: %s %v %v", legacy, trace, err)
	}
}
