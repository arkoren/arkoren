// Package messaging holds the telemetry shared by the queue and broadcast
// facades: producer and consumer spans, trace propagation through message
// metadata, and the messaging.* metrics of the OpenTelemetry semantic
// conventions. It never records payloads or metadata values.
// Callers resolve the configured/default telemetry handle before sending or
// processing; a nil handle is disabled, not a request for the process default.
//
//	instruments := telemetry.NewInstruments(messaging.Build("example.com/jobs"))
//	ctx, carrier, operation := messaging.Send(ctx, handle, instruments, target)
//	err := publish(ctx, carrier)
//	operation.End(ctx, err)
//	if err != nil {
//		return err
//	}
package messaging

import (
	"cmp"
	"context"
	"time"

	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

// Instruments are the tracer and metrics of one component for one
// [telemetry.Telemetry].
// Build supplies this opaque bundle; cache it through NewInstruments so Send
// and Process can resolve the instruments for the current telemetry handle.
//
// Example: instruments := telemetry.NewInstruments(messaging.Build("example.com/jobs"))
type Instruments struct {
	// tracer starts the producer and consumer spans.
	tracer telemetry.Tracer

	// sent counts produced messages.
	sent telemetry.Counter

	// consumed counts consumed messages.
	consumed telemetry.Counter

	// operation records messaging.client.operation.duration.
	operation telemetry.Histogram

	// process records messaging.process.duration.
	process telemetry.Histogram
}

// Build returns the function that creates [Instruments] in the
// instrumentation scope, for use with [telemetry.NewInstruments].
// Instrument keys and bucket policies are fixed by the messaging conventions;
// constructing the callback does not start telemetry or perform transport I/O.
//
// Example: instruments := telemetry.NewInstruments(messaging.Build("example.com/jobs"))
func Build(scope string) func(*telemetry.Telemetry) *Instruments {
	// durationBuckets is this factory's fixed policy in seconds, shared by
	// operation and processing histograms without mutable package state.
	durationBuckets := []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}
	// metricKeys is the bounded label set copied into every instrument.
	metricKeys := []string{
		semconv.MessagingSystem, semconv.MessagingOperationName,
		semconv.MessagingDestinationName, semconv.ErrorType,
	}

	return func(t *telemetry.Telemetry) *Instruments {
		meter := t.Meter(scope)

		return &Instruments{
			tracer: t.Tracer(scope),
			sent: meter.Counter(semconv.MessagingClientSentMessages,
				telemetry.Unit("{message}"), telemetry.Description("Number of messages producer attempted to send."),
				telemetry.Keys(metricKeys...)),
			consumed: meter.Counter(semconv.MessagingClientConsumedMessages,
				telemetry.Unit("{message}"), telemetry.Description("Number of messages consumed."),
				telemetry.Keys(metricKeys...)),
			operation: meter.Histogram(semconv.MessagingClientOperationDuration,
				telemetry.Unit("s"), telemetry.Description("Duration of messaging operations initiated by a producer."),
				telemetry.Keys(metricKeys...), telemetry.Buckets(durationBuckets...)),
			process: meter.Histogram(semconv.MessagingProcessDuration,
				telemetry.Unit("s"), telemetry.Description("Duration of processing operations."),
				telemetry.Keys(metricKeys...), telemetry.Buckets(durationBuckets...)),
		}
	}
}

// Target describes the message an [Op] sends or processes.
// It supplies bounded transport classifications, never the message payload.
// Process reads Metadata synchronously without retaining or changing its map.
//
//	target := messaging.Target{
//		System: "rabbitmq",
//		Destination: "billing",
//	}
//	ctx, operation := messaging.Process(ctx, handle, instruments, target)
//	operation.End(ctx, nil)
type Target struct {
	// Driver is the driver the message passes through. When System is empty
	// the messaging system comes from the driver's MessagingSystem method.
	// Example: target.Driver = broker
	Driver any

	// System overrides the messaging system. Empty derives it from Driver.
	// Example: target.System = "rabbitmq"
	System string

	// Destination is the queue or broadcast name.
	// Example: target.Destination = "billing"
	Destination string

	// ID is the message ID. Empty omits messaging.message.id.
	// Example: target.ID = delivery.ID
	ID string

	// Job is the queue job name. Empty omits arkoren.queue.job.
	// Example: target.Job = "send-receipt"
	Job string

	// Attempt is the delivery attempt. Zero omits arkoren.queue.attempt.
	// Example: target.Attempt = 1
	Attempt int

	// Metadata carries the propagation headers a consumer extracts.
	// Example: target.Metadata = carrier
	Metadata map[string]string
}

// systemNamer is implemented by drivers that name their messaging system.
type systemNamer interface {
	// MessagingSystem identifies the transport for messaging telemetry.
	MessagingSystem() string
}

// systemOf returns the messaging system of target.
func systemOf(target Target) string {
	if target.System != "" {
		return target.System
	}

	namer, ok := unwrap.As[systemNamer](target.Driver)

	if !ok {
		return semconv.MessagingSystemOther
	}

	return cmp.Or(namer.MessagingSystem(), semconv.MessagingSystemOther)
}

// Op is one in-flight traced messaging operation. A nil *Op is valid and does
// nothing, which is how disabled telemetry costs nothing.
// The facade owns one operation and calls End once after transport completion.
// It is not safe for concurrent completion or mutation.
//
//	ctx, operation := messaging.Process(ctx, handle, instruments, target)
//	err := consume(ctx)
//	operation.End(ctx, err)
//	if err != nil {
//		return err
//	}
type Op struct {
	// instruments record the result.
	instruments *Instruments

	// span is the producer or consumer span.
	span *telemetry.Span

	// startedAt is when the operation began, used for elapsed duration.
	startedAt time.Time

	// attrs are the metric attributes common to every outcome.
	attrs []telemetry.Attr

	// processing reports a consumer operation.
	processing bool
}

// Send starts a producer span "send {destination}" and returns the metadata
// holding the injected trace context, nil when nothing was injected. The
// metadata is always a new map. With disabled telemetry it returns ctx
// unchanged, nil metadata, and a nil Op, without allocating.
// A nil telemetry handle is disabled; callers resolve Default explicitly.
//
//	ctx, carrier, operation := messaging.Send(ctx, handle, instruments, target)
//	err := publish(ctx, carrier)
//	operation.End(ctx, err)
//	if err != nil {
//		return err
//	}
func Send(
	ctx context.Context,
	t *telemetry.Telemetry,
	in *telemetry.Instruments[Instruments],
	target Target,
) (context.Context, map[string]string, *Op) {
	if !t.Enabled() {
		return ctx, nil, nil
	}

	ctx, op := begin(ctx, t, in, target, semconv.MessagingOperationSend, telemetry.Producer)

	carrier := telemetry.MapCarrier{}
	t.Inject(ctx, carrier)

	if len(carrier) == 0 {
		return ctx, nil, op
	}

	return ctx, carrier, op
}

// Process extracts the trace context from target.Metadata and starts a
// consumer span "process {destination}" parented to it. With disabled
// telemetry it returns ctx unchanged and a nil Op, without allocating.
// Metadata is read synchronously and never added as telemetry attributes.
//
//	ctx, operation := messaging.Process(ctx, handle, instruments, target)
//	err := consume(ctx)
//	operation.End(ctx, err)
//	if err != nil {
//		return err
//	}
func Process(
	ctx context.Context,
	t *telemetry.Telemetry,
	in *telemetry.Instruments[Instruments],
	target Target,
) (context.Context, *Op) {
	if !t.Enabled() {
		return ctx, nil
	}

	ctx = t.Extract(ctx, telemetry.MapCarrier(target.Metadata))

	return begin(ctx, t, in, target, semconv.MessagingOperationProcess, telemetry.Consumer)
}

// begin starts the span of an operation.
func begin(
	ctx context.Context,
	t *telemetry.Telemetry,
	in *telemetry.Instruments[Instruments],
	target Target,
	name string,
	kind telemetry.SpanKind,
) (context.Context, *Op) {
	instruments := in.For(t)
	attrs := []telemetry.Attr{
		telemetry.String(semconv.MessagingSystem, systemOf(target)),
		telemetry.String(semconv.MessagingOperationName, name),
		telemetry.String(semconv.MessagingDestinationName, target.Destination),
	}
	spanAttrs := appendOptional(append(attrs[:3:3], telemetry.String(semconv.MessagingOperationType, name)), target)

	ctx, span := instruments.tracer.Start(ctx, name+" "+target.Destination,
		telemetry.Kind(kind), telemetry.Attrs(spanAttrs...))
	op := &Op{
		instruments: instruments,
		span:        span,
		startedAt:   time.Now(),
		attrs:       attrs,
		processing:  kind == telemetry.Consumer,
	}

	return ctx, op
}

// appendOptional adds the span attributes target defines.
func appendOptional(attrs []telemetry.Attr, target Target) []telemetry.Attr {
	if target.ID != "" {
		attrs = append(attrs, telemetry.String(semconv.MessagingMessageID, target.ID))
	}

	if target.Job != "" {
		attrs = append(attrs, telemetry.String(semconv.QueueJob, target.Job))
	}

	if target.Attempt > 0 {
		attrs = append(attrs, telemetry.Int(semconv.QueueAttempt, target.Attempt))
	}

	return attrs
}

// End finishes the operation and records its metrics. A non-nil err sets the
// span status and error.type from the error's class only, using known to name
// package errors: the error text can carry payload data, so it is never
// recorded and RecordError is never called.
// Completion records one point per metric; the caller must not call End twice.
//
//	err := publish(ctx, carrier)
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

		attrs = append(attrs[:3:3], errorType)
	}

	operation.span.End()

	count, duration := operation.instruments.sent, operation.instruments.operation

	if operation.processing {
		count, duration = operation.instruments.consumed, operation.instruments.process
	}

	count.Add(ctx, 1, attrs...)
	duration.Record(ctx, time.Since(operation.startedAt).Seconds(), attrs...)
}
