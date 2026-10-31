package workflowtest

import (
	"context"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/workflow"
)

// traceStep returns the trace identity seen by an actual attempt. The suite
// injects only serialized fields, so a driver cannot pass by retaining ctx.
var traceStep = workflow.NewStep("conf-trace-step", func(ctx context.Context, _ string) (string, error) {
	return telemetry.SpanFromContext(ctx).Context().TraceID.String(), nil
})

// traceFlow reaches an actual step after orchestration has crossed the native
// submission boundary. No telemetry side effect occurs in this handler.
var traceFlow = workflow.New("conf-trace-flow", func(ctx *workflow.Context, in string) (string, error) {
	return ctx.Step(traceStep, in)
}, func(definition *workflow.Builder) {
	definition.MaxDuration(maxDuration)
})

// traceChildFlow starts a child whose actual step must retain the original
// serialized parent even though no orchestration context.Context exists.
var traceChildFlow = workflow.New("conf-trace-child", func(ctx *workflow.Context, id string) (string, error) {
	return ctx.Child(traceFlow, id, "input")
}, func(definition *workflow.Builder) {
	definition.MaxDuration(maxDuration)
})

// traceSpawnFlow leaves detached work behind; its carrier must survive without
// preserving the parent's cancellation context or waiting for its step.
var traceSpawnFlow = workflow.New("conf-trace-spawn", func(ctx *workflow.Context, id string) (string, error) {
	return ctx.Spawn(traceFlow, id, "input")
}, func(definition *workflow.Builder) {
	definition.MaxDuration(maxDuration)
})

// testTraceCarrier verifies the portable start-to-attempt trace contract with a
// known W3C carrier. User metadata deliberately contains a conflicting field.
func testTraceCarrier(s *suite) {
	carrier := map[string]string{"traceparent": "00-11111111111111111111111111111111-2222222222222222-01"}

	err := s.client.Driver().Start(s.ctx, traceFlow.Name(), s.id, []byte(`{"v":"input"}`), workflow.StartOptions{
		Trace:    carrier,
		Metadata: map[string]string{"traceparent": "user-metadata"},
	})

	if err != nil {
		s.t.Fatal(err)
	}

	carrier["traceparent"] = "caller-mutated-after-start"

	trace, err := s.client.Run(traceFlow, s.id).Result(s.ctx)

	if err != nil {
		s.t.Fatal(err)
	}

	if trace != "11111111111111111111111111111111" {
		s.t.Fatalf("step trace = %q, want injected trace", trace)
	}
}

// testChildTraceCarrier checks deterministic forwarding to a newly created
// child. Unsupported Child is already checked for pre-submission rejection.
func testChildTraceCarrier(s *suite) {
	if s.opts.withoutChild {
		s.t.Skip("Child unsupported; rejection is checked separately")
	}

	input, err := workflow.EncodeValue(nil, s.id+"-child")

	if err != nil {
		s.t.Fatal(err)
	}

	err = s.client.Driver().Start(s.ctx, traceChildFlow.Name(), s.id, input, workflow.StartOptions{
		Trace: map[string]string{"traceparent": "00-11111111111111111111111111111111-2222222222222222-01"},
	})

	if err != nil {
		s.t.Fatal(err)
	}

	trace, err := s.client.Run(traceChildFlow, s.id).Result(s.ctx)

	if err != nil {
		s.t.Fatal(err)
	}

	if trace != "11111111111111111111111111111111" {
		s.t.Fatalf("child trace = %q, want original start trace", trace)
	}
}

// testSpawnTraceCarrier verifies detached work receives serialized propagation
// after the spawning orchestration has returned, without creating a run span.
func testSpawnTraceCarrier(s *suite) {
	if s.opts.withoutSpawn {
		s.t.Skip("Spawn unsupported; rejection is checked separately")
	}

	input, err := workflow.EncodeValue(nil, s.id+"-spawn")

	if err != nil {
		s.t.Fatal(err)
	}

	err = s.client.Driver().Start(s.ctx, traceSpawnFlow.Name(), s.id, input, workflow.StartOptions{
		Trace: map[string]string{"traceparent": "00-11111111111111111111111111111111-2222222222222222-01"},
	})

	if err != nil {
		s.t.Fatal(err)
	}

	id, err := s.client.Run(traceSpawnFlow, s.id).Result(s.ctx)

	if err != nil {
		s.t.Fatal(err)
	}

	trace, err := s.client.Run(traceFlow, id).Result(s.ctx)

	if err != nil {
		s.t.Fatal(err)
	}

	if trace != "11111111111111111111111111111111" {
		s.t.Fatalf("spawn trace = %q, want original start trace", trace)
	}
}
