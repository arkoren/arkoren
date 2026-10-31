package oteltelemetry

import (
	"context"
	"fmt"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/otel/oteltelemetry/internal/sdk"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Inject writes the propagation headers of the current span of ctx into
// carrier. The native propagator is shared and defaults to W3C Trace Context
// plus Baggage. Baggage is caller-controlled data, not automatically redacted.
//
// With a started driver and a context carrying the outgoing span:
//
//	carrier := telemetry.MapCarrier{}
//	driver.Inject(ctx, carrier)
//	_ = carrier["traceparent"]
func (driver *Driver) Inject(ctx context.Context, carrier telemetry.Carrier) {
	run := driver.state.Load()

	if run == nil {
		return
	}

	run.Propagator.Inject(nativeContext(ctx), carrier)
}

// Extract reads propagation headers from carrier. It returns a context
// holding the remote span and baggage, and the remote span context. Absent or
// malformed headers return ctx and the zero span context. This low-level method
// performs no peer authentication; apply the application's trust policy before
// extracting untrusted inbound headers.
//
//	ctx, parent := driver.Extract(ctx, carrier)
//	if parent.IsValid() {
//		ctx, span := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
//		span.End()
//		_ = ctx
//	}
func (driver *Driver) Extract(ctx context.Context, carrier telemetry.Carrier) (context.Context, telemetry.SpanContext) {
	run := driver.state.Load()

	if run == nil {
		return ctx, telemetry.SpanContext{}
	}

	// Extracting over an invalid span context makes a stale span in ctx
	// distinguishable from a freshly extracted one.
	extracted := run.Propagator.Extract(trace.ContextWithSpanContext(ctx, trace.SpanContext{}), carrier)

	spanContext := trace.SpanContextFromContext(extracted)

	if !spanContext.IsValid() {
		return ctx, telemetry.SpanContext{}
	}

	return extracted, fromOTelSpanContext(spanContext)
}

// nativeContext returns ctx with an OpenTelemetry span context in it. A native
// span in ctx wins; otherwise the telemetry span of ctx is adopted as a
// non-recording parent. It returns ctx unchanged when neither exists.
func nativeContext(ctx context.Context) context.Context {
	if trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}

	spanContext := telemetry.SpanFromContext(ctx).Context()

	if !spanContext.IsValid() {
		return ctx
	}

	return trace.ContextWithSpanContext(ctx, toOTelSpanContext(spanContext))
}

// toOTelSpanContext converts a telemetry span context. An unparsable trace
// state is dropped.
func toOTelSpanContext(spanContext telemetry.SpanContext) trace.SpanContext {
	flags := trace.TraceFlags(0)

	if spanContext.Sampled {
		flags = trace.FlagsSampled
	}

	state, _ := trace.ParseTraceState(spanContext.TraceState)

	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID(spanContext.TraceID),
		SpanID:     trace.SpanID(spanContext.SpanID),
		TraceFlags: flags,
		TraceState: state,
		Remote:     spanContext.Remote,
	})
}

// fromOTelSpanContext converts an OpenTelemetry span context.
func fromOTelSpanContext(spanContext trace.SpanContext) telemetry.SpanContext {
	return telemetry.SpanContext{
		TraceID:    telemetry.TraceID(spanContext.TraceID()),
		SpanID:     telemetry.SpanID(spanContext.SpanID()),
		Sampled:    spanContext.IsSampled(),
		Remote:     spanContext.IsRemote(),
		TraceState: spanContext.TraceState().String(),
	}
}

// spanRecorder adapts an OpenTelemetry span to [telemetry.SpanRecorder]. Its
// immutable identity snapshots let observers report the same child and chosen
// parent as the native SDK without depending on native recording state.
type spanRecorder struct {
	// span is the native span.
	span trace.Span

	// spanContext is the identity of span, converted once.
	spanContext telemetry.SpanContext

	// parentContext is the valid effective parent before SDK Start, or zero for
	// a root. It is independent of the child and preserves propagation metadata.
	parentContext telemetry.SpanContext
}

// Context returns the identity of the span.
func (spanRecorder *spanRecorder) Context() telemetry.SpanContext {
	return spanRecorder.spanContext
}

// Parent returns the immutable parent selected at StartSpan, not the child now
// stored in the returned context. A valid native parent takes precedence over a
// facade parent, even when non-recording; roots report the zero SpanContext.
//
// With a started driver and an operation context:
//
//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
//	if parent := recorder.Parent(); parent.IsValid() {
//		fmt.Println(parent.SpanID.String())
//	}
//	recorder.End()
func (spanRecorder *spanRecorder) Parent() telemetry.SpanContext {
	return spanRecorder.parentContext
}

// Recording reports whether the span keeps attributes and events.
func (spanRecorder *spanRecorder) Recording() bool {
	return spanRecorder.span.IsRecording()
}

// SetAttributes adds attributes to the span.
func (spanRecorder *spanRecorder) SetAttributes(attrs []telemetry.Attr) {
	spanRecorder.span.SetAttributes(sdk.Attrs(attrs)...)
}

// AddEvent records an event on the span.
func (spanRecorder *spanRecorder) AddEvent(name string, attrs []telemetry.Attr) {
	spanRecorder.span.AddEvent(name, trace.WithAttributes(sdk.Attrs(attrs)...))
}

// RecordError records err as an "exception" event with the Go type of err and
// its message, without changing the status.
func (spanRecorder *spanRecorder) RecordError(err error) {
	spanRecorder.span.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.type", fmt.Sprintf("%T", err)),
		attribute.String("exception.message", err.Error()),
	))
}

// SetStatus sets the span status. Unset is ignored and the description is only
// kept for Error; the SDK enforces that OK is final.
func (spanRecorder *spanRecorder) SetStatus(code telemetry.StatusCode, description string) {
	switch code {
	case telemetry.OK:
		spanRecorder.span.SetStatus(codes.Ok, "")
	case telemetry.Error:
		spanRecorder.span.SetStatus(codes.Error, description)
	}
}

// End finishes the span.
func (spanRecorder *spanRecorder) End() {
	spanRecorder.span.End()
}

// noopRecorder is the recorder of spans started while the driver is inactive.
type noopRecorder struct{}

// Context returns the invalid span context.
func (noopRecorder) Context() telemetry.SpanContext {
	return telemetry.SpanContext{}
}

// Parent returns zero because an inactive driver starts no native span. It does
// not adopt a parent from the unchanged incoming context or report that parent
// as though it belonged to a newly created child.
//
//	driver := oteltelemetry.New()
//	_, recorder := driver.StartSpan(context.Background(), telemetry.SpanStart{Name: "cache get"})
//	fmt.Println(recorder.Parent().IsValid()) // false before Start
//	recorder.End()
func (noopRecorder) Parent() telemetry.SpanContext {
	return telemetry.SpanContext{}
}

// Recording reports false.
func (noopRecorder) Recording() bool {
	return false
}

// SetAttributes discards attrs.
func (noopRecorder) SetAttributes([]telemetry.Attr) {
}

// AddEvent discards the event.
func (noopRecorder) AddEvent(string, []telemetry.Attr) {
}

// RecordError discards err.
func (noopRecorder) RecordError(error) {
}

// SetStatus discards the status.
func (noopRecorder) SetStatus(telemetry.StatusCode, string) {
}

// End does nothing.
func (noopRecorder) End() {
}
