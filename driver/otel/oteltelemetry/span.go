package oteltelemetry

import (
	"context"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/otel/oteltelemetry/internal/sdk"

	"go.opentelemetry.io/otel/trace"
)

// spanKinds translates Arkoren's configuration names to the SDK's numeric span
// kinds. Unknown and empty names are handled as Internal by StartSpan.
var spanKinds = map[telemetry.SpanKind]trace.SpanKind{
	telemetry.Internal: trace.SpanKindInternal,
	telemetry.Server:   trace.SpanKindServer,
	telemetry.Client:   trace.SpanKindClient,
	telemetry.Producer: trace.SpanKindProducer,
	telemetry.Consumer: trace.SpanKindConsumer,
}

// StartSpan starts a span under the valid OpenTelemetry parent in ctx, even if
// that parent is non-recording or conflicts with the facade's current span. If
// no valid native parent exists, it adopts a valid facade parent, including a
// memory-backed or extracted span. The returned context carries the native child
// so later OpenTelemetry instrumentation and injection still use that child.
//
// The recorder's [telemetry.SpanRecorder.Parent] is an immutable snapshot of the
// selected parent before SDK Start, preserving its IDs, Sampled, Remote, and
// TraceState. This lets facade observers report the SDK's actual parent rather
// than infer it from a potentially different facade context. Roots report zero,
// including when incoming invalid native IDs carry other metadata.
//
// Before Start and after Stop it returns ctx and a non-recording recorder whose
// Context and Parent are zero. Empty or unknown span kinds use OpenTelemetry's
// Internal kind. Native provider ownership and sampling remain unchanged.
//
// With a started driver and an operation context:
//
//	ctx, span := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
//	if parent := span.Parent(); parent.IsValid() {
//		fmt.Println(parent.TraceID.String(), parent.SpanID.String())
//	}
//	span.End()
//	_ = ctx // Carries the native child, not its parent.
func (driver *Driver) StartSpan(ctx context.Context, start telemetry.SpanStart) (context.Context, telemetry.SpanRecorder) {
	run := driver.state.Load()

	if run == nil || run.TracerProvider == nil {
		return ctx, noopRecorder{}
	}

	kind := trace.SpanKindInternal

	if native, ok := spanKinds[start.Kind]; ok {
		kind = native
	}

	ctx = nativeContext(ctx)
	// parent snapshots only valid input before Start installs the native child.
	// Invalid IDs mean a root even when flags or tracestate are present.
	var parent telemetry.SpanContext

	if native := trace.SpanContextFromContext(ctx); native.IsValid() {
		parent = fromOTelSpanContext(native)
	}

	ctx, span := run.Tracer(start.Scope).Start(ctx, start.Name,
		trace.WithSpanKind(kind),
		trace.WithAttributes(sdk.Attrs(start.Attrs)...),
	)

	return ctx, &spanRecorder{
		span:          span,
		spanContext:   fromOTelSpanContext(span.SpanContext()),
		parentContext: parent,
	}
}
