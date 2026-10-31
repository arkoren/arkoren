package feature

import (
	"context"
	"time"

	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// telemetryScope is the instrumentation scope of feature flag telemetry.
const telemetryScope = "arkoren.dev/core/service/feature"

// durationBuckets are the bucket bounds, in seconds, suited to flag
// evaluation latency: in-process evaluation takes microseconds, remote
// providers milliseconds.
var durationBuckets = []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1}

// errorSentinels name the package errors reported as error.type.
var errorSentinels = []errtype.Sentinel{
	{Err: ErrInvalidFlag, Name: "feature.ErrInvalidFlag"},
	{Err: ErrNotFound, Name: "feature.ErrNotFound"},
	{Err: ErrTypeMismatch, Name: "feature.ErrTypeMismatch"},
	{Err: ErrNotReady, Name: "feature.ErrNotReady"},
	{Err: ErrInvalidSubject, Name: "feature.ErrInvalidSubject"},
}

// instruments are the tracer and metrics of one [telemetry.Telemetry].
type instruments struct {
	// tracer starts the evaluation spans.
	tracer telemetry.Tracer

	// evaluations counts evaluations.
	evaluations telemetry.Counter

	// duration records arkoren.feature.evaluation.duration.
	duration telemetry.Histogram
}

// newInstruments builds the feature flag instruments for t.
func newInstruments(t *telemetry.Telemetry) *instruments {
	meter := t.Meter(telemetryScope)

	return &instruments{
		tracer: t.Tracer(telemetryScope),
		evaluations: meter.Counter(semconv.FeatureEvaluations,
			telemetry.Unit("{evaluation}"), telemetry.Description("Number of feature flag evaluations."),
			telemetry.Keys(semconv.FeatureFlagKey, semconv.FeatureFlagProviderName, semconv.FeatureFlagResultReason, semconv.ErrorType)),
		duration: meter.Histogram(semconv.FeatureEvaluationDuration,
			telemetry.Unit("s"), telemetry.Description("Duration of feature flag evaluations."),
			telemetry.Keys(semconv.FeatureFlagKey, semconv.FeatureFlagProviderName, semconv.ErrorType),
			telemetry.Buckets(durationBuckets...)),
	}
}

// operation is one in-flight traced evaluation. A nil *operation is valid
// and does nothing, which is how disabled telemetry costs nothing.
type operation struct {
	// instruments record the result.
	instruments *instruments

	// span is the evaluation span.
	span *telemetry.Span

	// start is when the evaluation began.
	start time.Time

	// key is the flag key.
	key string

	// provider is the provider name.
	provider string
}

// begin starts a traced evaluation of the flag key. It returns a nil
// operation, without allocating, when telemetry is disabled. The span never
// carries subject data.
//
// The span is Internal, not Client: it is a client span only when the driver
// calls a remote service, which the facade cannot know, and many drivers
// evaluate from memory.
func (flags *Flags) begin(ctx context.Context, key string) (context.Context, *operation) {
	t := flags.config.Telemetry

	if t == nil {
		t = telemetry.Default()
	}

	if !t.Enabled() {
		return ctx, nil
	}

	provider := flags.config.Name

	if provider == "" {
		provider = DefaultName
	}

	instruments := flags.instruments.For(t)
	ctx, span := instruments.tracer.Start(ctx, "feature evaluate", telemetry.Kind(telemetry.Internal),
		telemetry.Attrs(telemetry.String(semconv.FeatureFlagKey, key), telemetry.String(semconv.FeatureFlagProviderName, provider)))

	return ctx, &operation{
		instruments: instruments,
		span:        span,
		start:       time.Now(),
		key:         key,
		provider:    provider,
	}
}

// end finishes the evaluation with err, the reason and the variant, and
// records the metrics. On failure the reason is [ReasonError], and the span
// status and error.type come from the error's class only: the error text
// names the flag key and drivers may add subject details, so it is never
// recorded and RecordError is never called. The variant is recorded on the
// span only, and only when not empty.
func (operation *operation) end(ctx context.Context, err error, reason Reason, variant string) {
	if operation == nil {
		return
	}

	durationAttrs := []telemetry.Attr{
		telemetry.String(semconv.FeatureFlagKey, operation.key),
		telemetry.String(semconv.FeatureFlagProviderName, operation.provider),
	}

	if err != nil {
		reason = ReasonError
		variant = ""
	}

	countAttrs := append(durationAttrs[:2:2], telemetry.String(semconv.FeatureFlagResultReason, reason.String()))
	operation.span.SetAttributes(telemetry.String(semconv.FeatureFlagResultReason, reason.String()))

	if variant != "" {
		operation.span.SetAttributes(telemetry.String(semconv.FeatureFlagResultVariant, variant))
	}

	if err != nil {
		name := errtype.Of(err, errorSentinels...)
		errorType := telemetry.String(semconv.ErrorType, name)
		operation.span.SetAttributes(errorType)
		operation.span.SetStatus(telemetry.Error, name)

		durationAttrs = append(durationAttrs, errorType)
		countAttrs = append(countAttrs, errorType)
	}

	operation.span.End()
	operation.instruments.evaluations.Add(ctx, 1, countAttrs...)
	operation.instruments.duration.Record(ctx, time.Since(operation.start).Seconds(), durationAttrs...)
}
