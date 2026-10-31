package workflow

import (
	"context"
	"errors"

	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// operationInstruments caches instruments by telemetry service, including late
// changes to the process default. Only bounded operation/outcome labels are used.
var operationInstruments = telemetry.NewInstruments(clientop.Build(clientop.Names{
	Scope:    "arkoren.dev/core/service/workflow",
	Noun:     "workflow observations",
	Count:    semconv.WorkflowOperationCount,
	Duration: semconv.WorkflowOperationDuration,
	Keys:     []string{semconv.WorkflowOperation, semconv.WorkflowOutcome},
	Buckets:  []float64{0.001, 0.01, 0.1, 1, 10, 60, 300, 900, 3600},
}))

// errObservationPanic represents an interrupted client call without retaining
// the panic value, whose text and concrete type can contain application data.
var errObservationPanic = errors.New("workflow observation interrupted by panic")

// finishClientObservation must be deferred directly by a client operation.
// A panic closes the observation with a bounded failure class, then propagates
// the exact recovered value. Normal returns use the operation's own finalizer
// so Start retains its special expected duplicate-start outcome.
func finishClientObservation(ctx context.Context, observation *clientop.Op, err *error, finish func(context.Context, *clientop.Op, error)) {
	if value := recover(); value != nil {
		observation.Label(telemetry.String(semconv.WorkflowOutcome, "error"))
		observation.End(ctx, errObservationPanic, errtype.Sentinel{
			Err:  errObservationPanic,
			Name: "workflow.panic",
		})
		panic(value)
	}

	finish(ctx, observation, *err)
}

// beginObservation observes one facade call or actual worker attempt. Handler
// execution receives its returned context so dependencies become child spans.
func beginObservation(ctx context.Context, service *telemetry.Telemetry, operation string, kind telemetry.SpanKind) (context.Context, *clientop.Op) {
	return clientop.Begin(ctx, service, operationInstruments, kind, "workflow", operation,
		telemetry.String(semconv.WorkflowOperation, operation))
}

// endObservation records only safe outcome categories, never error messages.
func endObservation(ctx context.Context, observation *clientop.Op, err error) {
	if err != nil {
		observation.Label(telemetry.String(semconv.WorkflowOutcome, "error"))
		observation.End(ctx, err)

		return
	}

	observation.Label(telemetry.String(semconv.WorkflowOutcome, "success"))
	observation.End(ctx, nil)
}

// endStartObservation treats duplicate starts as an expected idempotency
// outcome while returning the driver's original error to the caller.
func endStartObservation(ctx context.Context, observation *clientop.Op, err error) {
	if errors.Is(err, ErrAlreadyStarted) {
		observation.Label(telemetry.String(semconv.WorkflowOutcome, "already_started"))
		observation.End(ctx, nil)

		return
	}

	endObservation(ctx, observation, err)
}

// telemetryService resolves nil at the observation boundary, not construction,
// so both propagation and instruments use the current process default.
func telemetryService(service *telemetry.Telemetry) *telemetry.Telemetry {
	if service == nil {
		return telemetry.Default()
	}

	return service
}

// NewTraceContext extracts a serialized start carrier onto a native attempt
// context, preserving its cancellation/deadline. Drivers call this only at the
// worker-to-step boundary; extraction must never run in replayed orchestration.
// The registry's worker telemetry service performs extraction. Nil/empty
// carriers are valid; no caller context is transported or retained.
//
//	ctx = registry.NewTraceContext(ctx, options.Trace)
//	output, err := handler.Handle(ctx, input)
//	if err != nil {
//		return err
//	}
//	_ = output
func (registry *Registry) NewTraceContext(ctx context.Context, carrier map[string]string) context.Context {
	return telemetryService(registry.telemetry).Extract(ctx, telemetry.MapCarrier(carrier))
}
