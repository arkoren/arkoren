package telemetry

import (
	"context"
	"encoding/hex"
)

// TraceID identifies a trace as 16 bytes. The all-zero value is invalid; this
// type represents an identity and does not generate randomness itself.
//
//	id := telemetry.TraceID{1}
//	fmt.Println(id.IsZero()) // false
type TraceID [16]byte

// SpanID identifies a span within a trace as 8 bytes. The all-zero value is
// invalid; span IDs are meaningful together with their owning TraceID.
//
//	id := telemetry.SpanID{1}
//	fmt.Println(id.IsZero()) // false
type SpanID [8]byte

// String returns all 16 bytes as 32 lowercase hexadecimal characters, including
// leading zeros. It also formats an invalid zero identity without failing.
//
//	id := telemetry.TraceID{1}
//	fmt.Println(id.String()) // 01000000000000000000000000000000
func (traceID TraceID) String() string {
	return hex.EncodeToString(traceID[:])
}

// IsZero reports whether every byte is zero. It validates absence only, not
// whether an identity is trusted or was issued by a particular backend.
//
//	fmt.Println((telemetry.TraceID{}).IsZero()) // true
func (traceID TraceID) IsZero() bool {
	return traceID == TraceID{}
}

// String returns all 8 bytes as 16 lowercase hexadecimal characters, including
// leading zeros. It also formats an invalid zero identity without failing.
//
//	id := telemetry.SpanID{1}
//	fmt.Println(id.String()) // 0100000000000000
func (spanID SpanID) String() string {
	return hex.EncodeToString(spanID[:])
}

// IsZero reports whether every byte is zero. A nonzero span ID still needs a
// nonzero trace ID before its containing SpanContext is valid.
//
//	fmt.Println((telemetry.SpanID{}).IsZero()) // true
func (spanID SpanID) IsZero() bool {
	return spanID == SpanID{}
}

// SpanContext identifies a span and carries the propagated trace flags. It is
// an immutable value that is safe to copy. Validity requires both IDs; Remote
// and Sampled do not establish trust or turn an adopted identity into a recorder.
//
//	identity := telemetry.SpanContext{
//		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}, Remote: true,
//	}
//	fmt.Println(identity.IsValid()) // true
type SpanContext struct {
	// TraceID is the trace the span belongs to.
	// Both this ID and SpanID must be nonzero for IsValid to return true.
	//
	//	identity := telemetry.SpanContext{TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}}
	//	fmt.Println(identity.TraceID.String())
	TraceID TraceID

	// SpanID is the span itself.
	// It identifies one operation within TraceID, not a globally unique trace.
	//
	//	identity := telemetry.SpanContext{TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}}
	//	fmt.Println(identity.SpanID.String())
	SpanID SpanID

	// Sampled reports whether the trace is being recorded.
	// It is a propagation flag, not a promise that a particular recorder keeps data.
	//
	//	identity := telemetry.SpanContext{TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}, Sampled: true}
	//	fmt.Println(identity.Sampled)
	Sampled bool

	// Remote reports that the context was extracted from a carrier, so the
	// span lives in another process.
	// It conveys origin, not authorization to trust the sender.
	//
	//	identity := telemetry.SpanContext{TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}, Remote: true}
	//	ctx := telemetry.ContextWithSpanContext(context.Background(), identity)
	//	fmt.Println(telemetry.SpanFromContext(ctx).Context().Remote)
	Remote bool

	// TraceState is the opaque W3C tracestate that was propagated with the
	// context. Drivers that do not use it pass it through unchanged.
	//
	//	identity := telemetry.SpanContext{
	//		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}, TraceState: "vendor=value",
	//	}
	//	fmt.Println(identity.TraceState)
	TraceState string
}

// IsValid reports whether both IDs are non-zero. It does not validate a remote
// sender, sampling decision, or the opaque vendor-specific TraceState text.
//
//	identity := telemetry.SpanContext{TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}}
//	fmt.Println(identity.IsValid()) // true
func (spanContext SpanContext) IsValid() bool {
	return !spanContext.TraceID.IsZero() && !spanContext.SpanID.IsZero()
}

// spanKey is the context key under which the current [Span] is stored.
type spanKey struct{}

// ContextWithSpanContext returns a context whose current span is a
// non-recording span with the given identity. Use it to adopt a span
// identity that did not come from [Telemetry.Extract], for example one read
// from a workflow run.
// The supplied flags are preserved without validation or normalization; an
// invalid identity remains invalid. Adoption stores no recorder, so span mutation
// and End are no-ops, but a later real child can use the adopted parent.
//
//	ctx = telemetry.ContextWithSpanContext(ctx, spanContext)
func ContextWithSpanContext(ctx context.Context, spanContext SpanContext) context.Context {
	return contextWithSpan(ctx, &Span{context: spanContext})
}

// contextWithSpan returns a context whose current span is span.
func contextWithSpan(ctx context.Context, span *Span) context.Context {
	return context.WithValue(ctx, spanKey{}, span)
}

// SpanFromContext returns the current span of ctx. It never returns nil: when
// ctx has no span it returns a non-recording span with an invalid
// [SpanContext], on which every method is a no-op.
//
//	telemetry.SpanFromContext(ctx).SetAttributes(telemetry.String("tenant", id))
func SpanFromContext(ctx context.Context) *Span {
	if span, ok := ctx.Value(spanKey{}).(*Span); ok {
		return span
	}

	return noopSpan
}
