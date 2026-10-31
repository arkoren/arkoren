// Package genai holds the telemetry of the llm facades: one span per model
// call, embeddings request, agent run or tool call, the
// gen_ai.client.operation.duration histogram and the gen_ai.client.token.usage
// histogram, all named by [arkoren.dev/core/internal/semconv] after the
// OpenTelemetry generative AI conventions. It plays the role that
// [arkoren.dev/core/internal/clientop] plays for storage and mail, for
// operations that also report token usage.
//
// Disabled telemetry costs nothing: [Begin] returns a nil [Op] without
// allocating, and every method of a nil Op does nothing. Prompts, outputs,
// tool arguments, tool results and error text are never recorded.
//
//	instruments := telemetry.NewInstruments(genai.Build("example.com/assistant"))
//	ctx, operation := genai.Begin(ctx, handle, instruments, telemetry.Client, "chat", "example-model")
//	err := requestCompletion(ctx)
//	operation.End(ctx, err)
//	if err != nil {
//		return err
//	}
package genai

import (
	"context"
	"slices"
	"time"

	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// Instruments are the tracer and the two histograms of one facade for one
// [telemetry.Telemetry].
// The opaque bundle is constructed by Build and resolved by Begin for the
// operation's handle; callers retain the NewInstruments cache, not its internals.
//
// Example: instruments := telemetry.NewInstruments(genai.Build("example.com/assistant"))
type Instruments struct {
	// tracer starts the spans.
	tracer telemetry.Tracer

	// duration records operation durations in seconds.
	duration telemetry.Histogram

	// tokens records token counts.
	tokens telemetry.Histogram
}

// Build returns the function that creates [Instruments] in the given
// instrumentation scope, for use with [telemetry.NewInstruments].
//
//	instruments := telemetry.NewInstruments(genai.Build("arkoren.dev/core/service/llm"))
func Build(scope string) func(*telemetry.Telemetry) *Instruments {
	// metricKeys belongs to this binding factory, not mutable package state.
	// The token histogram adds GenAITokenType through a detached slice.
	metricKeys := []string{
		semconv.GenAIOperationName,
		semconv.GenAIProviderName,
		semconv.GenAIRequestModel,
		semconv.GenAIResponseModel,
		semconv.ErrorType,
	}

	return func(t *telemetry.Telemetry) *Instruments {
		meter := t.Meter(scope)

		return &Instruments{
			tracer: t.Tracer(scope),
			duration: meter.Histogram(semconv.GenAIClientOperationDuration,
				telemetry.Unit("s"), telemetry.Description("Duration of generative AI client operations."),
				telemetry.Keys(metricKeys...),
				telemetry.Buckets(0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92)),
			tokens: meter.Histogram(semconv.GenAIClientTokenUsage,
				telemetry.Unit("{token}"), telemetry.Description("Number of input and output tokens used."),
				telemetry.Keys(append(slices.Clone(metricKeys), semconv.GenAITokenType)...),
				telemetry.Buckets(1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864)),
		}
	}
}

// Op is one in-flight traced operation. A nil *Op is valid and does nothing,
// which is how disabled telemetry costs nothing.
// It belongs to one operation and is not safe for concurrent mutation. Usage
// replaces any previously supplied token counts; End must be called once.
//
//	ctx, operation := genai.Begin(ctx, handle, instruments, telemetry.Client, "chat", "example-model")
//	operation.Usage(120, 24)
//	operation.End(ctx, nil)
type Op struct {
	// instruments record the result.
	instruments *Instruments

	// span is the operation span.
	span *telemetry.Span

	// startedAt is when the operation began, used for elapsed duration.
	startedAt time.Time

	// attrs are the metric labels.
	attrs []telemetry.Attr

	// input and output are the token counts reported by [Op.Usage].
	input, output int

	// usageRecorded reports whether [Op.Usage] ran, so operations without tokens
	// record no token histogram points.
	usageRecorded bool
}

// Begin starts a span named "{operation} {name}" of the given kind, as the
// gen-ai conventions define it: name is the model, agent or tool. attrs are
// set on the span and become the metric labels; their keys should come from
// the gen_ai.* names, and keys the histograms do not list appear on the span
// only. The name is built from two parts so that disabled telemetry does not
// pay for the concatenation. A nil t follows [telemetry.Default], resolved on
// every call. With disabled telemetry Begin returns ctx unchanged and a nil
// Op. attrs is copied, so the caller may reuse it.
//
//	generate := func(ctx context.Context) (err error) {
//		ctx, operation := genai.Begin(ctx, handle, instruments, telemetry.Client,
//			semconv.GenAIOperationChat, "example-model")
//		defer func() {
//			operation.End(ctx, err)
//		}()
//
//		return requestCompletion(ctx)
//	}
//	if err := generate(ctx); err != nil {
//		return err
//	}
func Begin(
	ctx context.Context,
	t *telemetry.Telemetry,
	in *telemetry.Instruments[Instruments],
	kind telemetry.SpanKind,
	operation, name string,
	attrs ...telemetry.Attr,
) (context.Context, *Op) {
	if t == nil {
		t = telemetry.Default()
	}

	if !t.Enabled() {
		return ctx, nil
	}

	instruments := in.For(t)
	attrs = append(slices.Clone(attrs), telemetry.String(semconv.GenAIOperationName, operation))
	ctx, span := instruments.tracer.Start(ctx, operation+" "+name, telemetry.Kind(kind), telemetry.Attrs(attrs...))

	return ctx, &Op{
		instruments: instruments,
		span:        span,
		startedAt:   time.Now(),
		attrs:       attrs,
	}
}

// Label adds attrs to the span and to the metric labels, for values known
// only after the operation ran, such as the response model.
// Nil operations do nothing; only declared keys enter metric labels.
//
// Example: operation.Label(telemetry.String(semconv.GenAIResponseModel, "example-model"))
func (operation *Op) Label(attrs ...telemetry.Attr) {
	if operation == nil {
		return
	}

	operation.span.SetAttributes(attrs...)

	operation.attrs = append(operation.attrs, attrs...)
}

// SetAttrs adds attrs to the span only, for values that are too detailed to
// be metric labels, such as the response ID.
// Nil operations do nothing. Do not pass prompt, result, or tool argument data.
//
// Example: operation.SetAttrs(telemetry.Int(semconv.LLMEmbeddingInputs, 2))
func (operation *Op) SetAttrs(attrs ...telemetry.Attr) {
	if operation == nil {
		return
	}

	operation.span.SetAttributes(attrs...)
}

// Usage records the token counts of the operation as span attributes and,
// when it ends, as two points of the token usage histogram.
// Counts are provider-reported nonnegative totals; later calls replace them.
// A call with zero counts still records two zero-valued points at End.
//
// Example: operation.Usage(120, 24)
func (operation *Op) Usage(input, output int) {
	if operation == nil {
		return
	}

	operation.input, operation.output, operation.usageRecorded = input, output, true
	operation.span.SetAttributes(
		telemetry.Int(semconv.GenAIUsageInputTokens, input),
		telemetry.Int(semconv.GenAIUsageOutputTokens, output))
}

// End finishes the operation and records the duration and any usage. A
// non-nil err sets the span status and error.type from the error's class
// only, using known to name package errors; error text is never recorded.
// The caller owns once-only completion; End does not deduplicate metric points.
//
//	err := requestCompletion(ctx)
//	operation.End(ctx, err)
//	if err != nil {
//		return err
//	}
func (operation *Op) End(ctx context.Context, err error, known ...errtype.Sentinel) {
	if operation == nil {
		return
	}

	attrs := operation.attrs

	if err != nil {
		name := errtype.Of(err, known...)
		errorType := telemetry.String(semconv.ErrorType, name)
		operation.span.SetAttributes(errorType)
		operation.span.SetStatus(telemetry.Error, name)

		attrs = append(attrs[:len(attrs):len(attrs)], errorType)
	}

	operation.span.End()
	operation.instruments.duration.Record(ctx, time.Since(operation.startedAt).Seconds(), attrs...)

	if operation.usageRecorded {
		input := append(attrs[:len(attrs):len(attrs)], telemetry.String(semconv.GenAITokenType, semconv.GenAITokenTypeInput))
		operation.instruments.tokens.Record(ctx, float64(operation.input), input...)

		output := append(attrs[:len(attrs):len(attrs)],
			telemetry.String(semconv.GenAITokenType, semconv.GenAITokenTypeOutput))
		operation.instruments.tokens.Record(ctx, float64(operation.output), output...)
	}
}
