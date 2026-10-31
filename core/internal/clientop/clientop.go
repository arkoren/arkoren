// Package clientop holds the telemetry shared by outbound I/O facades:
// one span per operation, a count and a duration histogram, and
// error classification. It is the same pattern as the cache and database
// telemetry and as [arkoren.dev/core/service/internal/messaging], for facades whose
// operations need no more than an operation name and a few low-cardinality
// labels.
//
// Disabled telemetry costs nothing: [Begin] returns a nil [Op] without
// allocating, and every method of a nil Op does nothing. Error text is never
// recorded and RecordError is never called, because error text can carry keys,
// addresses or payload data.
//
// Construct instruments once, then complete every admitted operation:
//
//	instruments := telemetry.NewInstruments(clientop.Build(names))
//	ctx, operation := clientop.Begin(ctx, handle, instruments, telemetry.Client, "storage", "put")
//	err := upload(ctx)
//	operation.End(ctx, err)
//	if err != nil {
//		return err
//	}
package clientop

import (
	"context"
	"slices"
	"time"

	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// Names describes one facade's counter and duration histogram. Build snapshots
// the slice fields immediately; the caller can reuse or modify its input slices.
// Keys should contain only bounded, nonsecret classifications.
//
//	names := clientop.Names{
//		Scope:    "example.com/billing",
//		Noun:     "billing operations",
//		Count:    "billing.operations",
//		Duration: "billing.duration",
//	}
//	instruments := telemetry.NewInstruments(clientop.Build(names))
type Names struct {
	// Scope is the instrumentation scope, such as "arkoren.dev/core/service/storage".
	// Example: names.Scope = "example.com/billing"
	Scope string

	// Noun is the plural thing being counted, such as "storage operations".
	// It completes the metric descriptions.
	// Example: names.Noun = "billing operations"
	Noun string

	// Count is the name of the counter metric.
	// Example: names.Count = "billing.operations"
	Count string

	// Duration is the name of the duration histogram metric, in seconds.
	// Example: names.Duration = "billing.duration"
	Duration string

	// Keys are the label keys of both metrics. error.type is added
	// automatically. Attributes passed to [Begin] and [Op.Label] whose key is
	// not listed appear on the span only.
	// Example: names.Keys = []string{"example.billing.operation"}
	Keys []string

	// Buckets are the duration histogram bounds, in seconds, suited to the
	// latency of the facade.
	// Example: names.Buckets = []float64{0.01, 0.1, 1}
	Buckets []float64
}

// Instruments are the tracer and metrics of one facade for one
// [telemetry.Telemetry].
// Construct this opaque bundle with Build and cache it through NewInstruments;
// Begin resolves the correct bundle for the operation's telemetry handle.
//
// Example: instruments := telemetry.NewInstruments(clientop.Build(names))
type Instruments struct {
	// tracer starts the spans.
	tracer telemetry.Tracer

	// count counts operations.
	count telemetry.Counter

	// duration records operation durations.
	duration telemetry.Histogram
}

// Build returns the function that creates [Instruments] from names, for use
// with [telemetry.NewInstruments]. It copies label keys and histogram bounds
// immediately, so later edits to the source slices cannot change lazy binding.
//
//	instruments := telemetry.NewInstruments(clientop.Build(names))
func Build(names Names) func(*telemetry.Telemetry) *Instruments {
	names.Keys = append(slices.Clone(names.Keys), semconv.ErrorType)
	names.Buckets = slices.Clone(names.Buckets)

	return func(t *telemetry.Telemetry) *Instruments {
		meter := t.Meter(names.Scope)

		return &Instruments{
			tracer: t.Tracer(names.Scope),
			count: meter.Counter(names.Count,
				telemetry.Unit("{operation}"), telemetry.Description("Number of "+names.Noun+"."),
				telemetry.Keys(names.Keys...)),
			duration: meter.Histogram(names.Duration,
				telemetry.Unit("s"), telemetry.Description("Duration of "+names.Noun+"."),
				telemetry.Keys(names.Keys...), telemetry.Buckets(names.Buckets...)),
		}
	}
}

// Op is one in-flight traced operation. A nil *Op is valid and does nothing,
// which is how disabled telemetry costs nothing.
// It is owned by one operation, not shared concurrently. Call End once to
// record its final outcome; Begin copies the caller's attribute slice.
//
//	ctx, operation := clientop.Begin(ctx, handle, instruments, telemetry.Client, "storage", "put")
//	err := upload(ctx)
//	operation.End(ctx, err)
//	if err != nil {
//		return err
//	}
type Op struct {
	// instruments record the result.
	instruments *Instruments

	// span is the operation span.
	span *telemetry.Span

	// startedAt is when the operation began, used for elapsed duration.
	startedAt time.Time

	// attrs are the metric labels.
	attrs []telemetry.Attr
}

// Begin starts a span named "{prefix} {suffix}" of the given kind with attrs,
// which are also the metric labels. The name is built in two parts so that
// disabled telemetry does not pay for the concatenation. A nil t follows [telemetry.Default], resolved on
// every call. With disabled telemetry Begin returns ctx unchanged and a nil
// Op, without allocating. attrs is copied, so the caller may reuse it.
//
//	put := func(ctx context.Context) (err error) {
//		ctx, operation := clientop.Begin(ctx, handle, instruments, telemetry.Client, "storage", "put")
//		defer func() {
//			operation.End(ctx, err)
//		}()
//
//		return upload(ctx)
//	}
//	if err := put(ctx); err != nil {
//		return err
//	}
func Begin(
	ctx context.Context,
	t *telemetry.Telemetry,
	in *telemetry.Instruments[Instruments],
	kind telemetry.SpanKind,
	prefix, suffix string,
	attrs ...telemetry.Attr,
) (context.Context, *Op) {
	if t == nil {
		t = telemetry.Default()
	}

	if !t.Enabled() {
		return ctx, nil
	}

	instruments := in.For(t)
	ctx, span := instruments.tracer.Start(ctx, prefix+" "+suffix, telemetry.Kind(kind), telemetry.Attrs(attrs...))

	return ctx, &Op{
		instruments: instruments,
		span:        span,
		startedAt:   time.Now(),
		attrs:       slices.Clone(attrs),
	}
}

// Label adds attrs to the span and to the metric labels, for outcomes known
// only after the operation ran. Keep their values low cardinality.
// Nil operations do nothing, preserving the disabled fast path.
//
// Example: operation.Label(telemetry.String("example.billing.outcome", "accepted"))
func (operation *Op) Label(attrs ...telemetry.Attr) {
	if operation == nil {
		return
	}

	operation.span.SetAttributes(attrs...)

	operation.attrs = append(operation.attrs, attrs...)
}

// SetAttrs adds attrs to the span only, for values such as sizes that are too
// detailed to be metric labels.
// Nil operations do nothing; these attributes do not alter metric cardinality.
//
// Example: operation.SetAttrs(telemetry.Int64("example.upload.bytes", 1024))
func (operation *Op) SetAttrs(attrs ...telemetry.Attr) {
	if operation == nil {
		return
	}

	operation.span.SetAttributes(attrs...)
}

// End finishes the operation and records the count and duration. A non-nil
// err sets the span status and error.type from the error's class only, using
// known to name package errors. Callers that treat an error as an outcome
// rather than a failure, such as a missing object, pass nil instead.
// The caller owns once-only completion; End does not deduplicate metric points.
//
//	err := upload(ctx)
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
	operation.instruments.count.Add(ctx, 1, attrs...)
	operation.instruments.duration.Record(ctx, time.Since(operation.startedAt).Seconds(), attrs...)
}
