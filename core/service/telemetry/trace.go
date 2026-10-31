package telemetry

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SpanKind describes the role of a span in a trace.
// Empty kinds select Internal when starting a span; strings do not encode SDK
// ordinals and must be mapped explicitly by native drivers.
//
//	option := telemetry.Kind(telemetry.Server)
type SpanKind string

const (
	// Internal is an operation inside the application. It is the default.
	//
	//	option := telemetry.Kind(telemetry.Internal)
	Internal SpanKind = "internal"

	// Server handles an incoming remote request.
	//
	//	option := telemetry.Kind(telemetry.Server)
	Server SpanKind = "server"

	// Client makes an outgoing remote request.
	//
	//	option := telemetry.Kind(telemetry.Client)
	Client SpanKind = "client"

	// Producer hands a message to a broker for later processing.
	//
	//	option := telemetry.Kind(telemetry.Producer)
	Producer SpanKind = "producer"

	// Consumer processes a message received from a broker.
	//
	//	option := telemetry.Kind(telemetry.Consumer)
	Consumer SpanKind = "consumer"
)

// StatusCode is the outcome of a span.
// Recorded spans start with Unset. Empty or unknown codes passed to SetStatus
// have no effect, like Unset, and cannot clear an existing verdict.
//
//	span.SetStatus(telemetry.Error, "backend unavailable")
type StatusCode string

const (
	// Unset is the default status: the span has no verdict.
	//
	//	pending := recorded.Status == telemetry.Unset
	Unset StatusCode = "unset"

	// OK marks the operation as successful. It is final.
	//
	//	span.SetStatus(telemetry.OK, "")
	OK StatusCode = "ok"

	// Error marks the operation as failed.
	//
	//	span.SetStatus(telemetry.Error, "backend unavailable")
	Error StatusCode = "error"
)

// StartOption configures a span started with [Tracer.Start]. It owns its
// attribute containers and is safe to reuse concurrently. Passing an existing
// option to a disabled tracer does not allocate; constructing [Attrs] with more
// than one attribute copies its slice.
//
//	option := telemetry.Attrs(telemetry.String("region", "eu"))
//	_, span := tracer.Start(ctx, "operation", option)
//	span.End()
type StartOption struct {
	// kind is the requested span kind, applied when setKind is true.
	kind SpanKind

	// setKind distinguishes an explicit Internal from "not set".
	setKind bool

	// attrs are the initial attributes.
	attrs []Attr

	// attr holds a sole initial attribute inline. This preserves allocation-free
	// construction of a single-attribute option, including disabled tracing.
	attr Attr
}

// Kind sets the span kind. The default is [Internal].
//
//	tracer.Start(ctx, "GET /users", telemetry.Kind(telemetry.Server))
func Kind(kind SpanKind) StartOption {
	return StartOption{
		kind:    kind,
		setKind: true,
	}
}

// Attrs sets the initial span attributes. Prefer it to calling
// [Span.SetAttributes] right after Start: samplers see initial attributes.
// The option copies the slice, so later caller edits do not change it. Attr
// values themselves are immutable and can be shared. Empty input adds nothing;
// invalid attributes are filtered when the span starts.
//
//	tracer.Start(ctx, "charge", telemetry.Attrs(telemetry.String("currency", "EUR")))
func Attrs(attrs ...Attr) StartOption {
	if len(attrs) == 1 {
		return StartOption{attr: attrs[0]}
	}

	return StartOption{attrs: slices.Clone(attrs)}
}

// Tracer starts spans for one instrumentation scope. It is a cheap value;
// create one per use with [Telemetry.Tracer]. The zero Tracer is a no-op.
// Starting a span does not start its owning telemetry lifecycle.
//
//	tracer := service.Tracer("example.com/cache")
//	_, span := tracer.Start(ctx, "cache get")
//	span.End()
type Tracer struct {
	// telemetry owns the driver. Nil means no-op.
	telemetry *Telemetry

	// scope is the instrumentation scope name.
	scope string
}

// Start starts a span named name as a child of the current span of ctx and
// returns a context carrying it. The caller must call [Span.End].
//
// Without a started trace driver (or a filling-in observer, see
// [Telemetry.Observe]) it allocates nothing and returns ctx and a
// shared non-recording span, so instrumentation is safe to leave in hot
// paths. Invalid attributes are dropped. The span name should have low
// cardinality: "GET /users/{id}", not "GET /users/42".
//
//	ctx, span := tracer.Start(ctx, "invoice.send", telemetry.Kind(telemetry.Producer))
//	defer span.End()
func (tracer Tracer) Start(ctx context.Context, name string, opts ...StartOption) (context.Context, *Span) {
	telemetry := tracer.telemetry

	if telemetry == nil || !telemetry.active.Load() {
		return ctx, noopSpan
	}

	table := telemetry.signals.Load()

	if table.traces == nil {
		return ctx, noopSpan
	}

	start := newSpanStart(tracer.scope, name, opts)

	observed := len(table.spans) > 0

	var snapshot FinishedSpan

	if observed {
		// The driver owns start.Attrs, so the snapshot takes its own copy
		// first. Nothing below allocates for the observers when there are
		// none.
		snapshot = FinishedSpan{
			Scope:  start.Scope,
			Name:   start.Name,
			Kind:   start.Kind,
			Status: Unset,
			Start:  time.Now(),
			Attrs:  mergeAttrs(nil, start.Attrs),
		}

	}

	ctx, recorder := table.traces.StartSpan(ctx, start)

	if recorder == nil {
		return ctx, noopSpan
	}

	if observed {
		snapshot.Context = recorder.Context()
		snapshot.Parent = recorder.Parent()
		recorder = &observedSpan{
			telemetry:      telemetry,
			inner:          recorder,
			innerRecording: recorder.Recording(),
			observers:      table.spans,
			span:           snapshot,
		}
	}

	span := &Span{
		telemetry: telemetry,
		recorder:  recorder,
		context:   recorder.Context(),
	}

	return contextWithSpan(ctx, span), span
}

// newSpanStart normalizes span options into driver-owned metadata. The single
// inline attribute and copied attribute slices preserve option order; invalid
// attributes are omitted. Each call allocates independent output containers so
// concurrent starts cannot mutate an option or another span's initial data.
func newSpanStart(scope, name string, opts []StartOption) SpanStart {
	start := SpanStart{Scope: scope, Name: name, Kind: Internal}

	for _, opt := range opts {
		if opt.setKind {
			start.Kind = normalizeSpanKind(opt.kind)
		}

		if opt.attr.Valid() {
			start.Attrs = append(start.Attrs, opt.attr)
		}

		start.Attrs = append(start.Attrs, validAttrs(opt.attrs)...)
	}

	return start
}

// normalizeSpanKind resolves known input to canonical constants and empty input
// to Internal. Unknown kinds remain unchanged in value, with detached storage.
// This prevents retained kind strings from making the caller's entire
// StartOption (including attribute slices) escape on no-op paths.
func normalizeSpanKind(kind SpanKind) SpanKind {
	switch kind {
	case Server:
		return Server
	case Client:
		return Client
	case Producer:
		return Producer
	case Consumer:
		return Consumer
	case "", Internal:
		return Internal
	default:
		return SpanKind(strings.Clone(string(kind)))
	}
}

// Span is one timed operation in a trace. A nil *Span and the span of a
// disabled telemetry are valid and do nothing. A Span is safe for concurrent
// use and must not be copied after first use. End waits for admitted recorder
// calls before ending the backend; later calls are no-ops.
//
//	_, span := tracer.Start(ctx, "operation")
//	span.SetAttributes(telemetry.String("region", "eu"))
//	span.End()
type Span struct {
	// telemetry gates recordings when the owning facade stops. It is non-nil
	// whenever recorder is set; adopted and no-op spans have no recorder.
	telemetry *Telemetry

	// recorder is the driver side. Nil for non-recording spans.
	recorder SpanRecorder

	// context is the span identity, fixed at creation.
	context SpanContext

	// ended is set by the first End call.
	ended atomic.Bool

	// callsMu serializes recorder-call registration with the first End.
	// No driver or observer callback runs while this lock is held.
	callsMu sync.Mutex

	// calls tracks admitted recorder operations. Once ended is set and End
	// has synchronized with callsMu, no new operations can be registered.
	calls sync.WaitGroup
}

// noopSpan is the shared span returned when telemetry is disabled.
var noopSpan = &Span{}

// active reports whether calls reach the driver.
func (span *Span) active() bool {
	if span == nil || span.recorder == nil {
		return false
	}

	return !span.ended.Load() && span.telemetry.active.Load()
}

// Context returns the span identity. It is invalid for a no-op span. For a
// span adopted with [ContextWithSpanContext] or [Telemetry.Extract] it is
// the adopted identity.
// Reading the identity does not record attributes or reactivate an ended span.
//
//	identity := telemetry.SpanContext{TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}}
//	ctx := telemetry.ContextWithSpanContext(context.Background(), identity)
//	fmt.Println(telemetry.SpanFromContext(ctx).Context().SpanID.String())
func (span *Span) Context() SpanContext {
	if span == nil {
		return SpanContext{}
	}

	return span.context
}

// IsRecording reports whether the span keeps attributes and events. Check it
// before computing expensive attribute values. It is false after End is
// requested or the owning telemetry stops; no recorder call follows backend End.
//
//	if span.IsRecording() {
//		span.SetAttributes(telemetry.String("region", "eu"))
//	}
func (span *Span) IsRecording() bool {
	if !span.beginCall() {
		return false
	}

	defer span.calls.Done()

	return span.recorder.Recording() && span.active()
}

// beginCall registers an operation before it can invoke the recorder. End
// closes admission atomically, then waits for registrations protected by
// callsMu. A successful caller must defer calls.Done before invoking callbacks.
func (span *Span) beginCall() bool {
	if !span.active() {
		return false
	}

	span.callsMu.Lock()
	defer span.callsMu.Unlock()

	if !span.active() {
		return false
	}

	span.calls.Add(1)

	return true
}

// record runs an attribute, event, error, or status operation only while the
// recorder accepts data. The callback runs without facade locks, but remains
// admitted until it returns so End cannot overtake it.
func (span *Span) record(record func(SpanRecorder)) {
	if !span.beginCall() {
		return
	}

	defer span.calls.Done()

	if span.recorder.Recording() && span.active() {
		record(span.recorder)
	}
}

// SetAttributes adds attributes to the span. Invalid attributes are dropped
// and a later attribute with the same key replaces the earlier one. The input
// container is copied before a recorder may retain it. End and stopped telemetry
// suppress new operations; concurrent admitted calls finish before backend End.
//
//	span.SetAttributes(telemetry.String("region", "eu"))
func (span *Span) SetAttributes(attrs ...Attr) {
	span.record(func(recorder SpanRecorder) {
		if valid := validAttrs(attrs); len(valid) > 0 {
			recorder.SetAttributes(valid)
		}
	})
}

// AddEvent records a timestamped event with attrs. Invalid attributes are
// dropped and the input container is copied for the recorder. It is a no-op
// after End or when the owning telemetry is stopped.
//
//	span.AddEvent("retry", telemetry.Int("attempt", 2))
func (span *Span) AddEvent(name string, attrs ...Attr) {
	span.record(func(recorder SpanRecorder) {
		recorder.AddEvent(name, validAttrs(attrs))
	})
}

// RecordError records err on the span as an exception event. It does not
// change the status; call [Span.SetStatus] for that. A nil err is ignored.
//
// Record only unexpected errors, and never put secrets or personal data in
// error text: the text is exported. Error formatting may record additional
// attributes, but must not synchronously end the same span from its recorder
// callback. End waits for admitted error recording to return.
//
//	if err != nil {
//		span.RecordError(err)
//		span.SetStatus(telemetry.Error, "operation failed")
//	}
func (span *Span) RecordError(err error) {
	if err == nil {
		return
	}

	span.record(func(recorder SpanRecorder) {
		recorder.RecordError(err)
	})
}

// SetStatus sets the span status. [Unset] is ignored, [OK] is final, and
// [Error] replaces [Unset] and an earlier [Error]. The description is kept
// only for [Error]; keep it short and free of personal data. Status updates are
// ignored after End or after the owning telemetry stops.
//
//	span.SetStatus(telemetry.Error, "backend unavailable")
func (span *Span) SetStatus(code StatusCode, description string) {
	span.record(func(recorder SpanRecorder) {
		recorder.SetStatus(code, description)
	})
}

// End finishes the span. Only the first call has an effect, and the span
// ignores every call after it. The first call waits for recorder operations
// already in progress, without holding facade locks across backend or observer
// callbacks. Backends must return from their callbacks; a recorder callback
// must not synchronously call End on its own facade span.
//
//	_, span := tracer.Start(ctx, "operation")
//	span.End()
func (span *Span) End() {
	if span == nil || span.recorder == nil {
		return
	}

	span.callsMu.Lock()

	if span.ended.Swap(true) {
		span.callsMu.Unlock()

		return
	}

	span.callsMu.Unlock()
	span.calls.Wait()
	span.recorder.End()
}
