package memorytelemetry

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry/internal/capture"
	"arkoren.dev/foundation/collection"
)

// lifecycleState is the position of a [Driver] in its single lifecycle. The
// zero value means Start has not been called. It stays numeric because it is
// private process state, never an API, configuration, or storage value.
type lifecycleState uint8

const (
	// stateStarted means the driver records.
	stateStarted lifecycleState = iota + 1

	// stateStopped means Stop was called.
	stateStopped
)

// Driver implements [telemetry.TraceDriver], [telemetry.MetricDriver], and
// [telemetry.LogDriver] in process memory. It is safe for concurrent use.
//
// It is also a [telemetry.Observer]: attached with
// [telemetry.Telemetry.Observe] it keeps a passive copy of what another
// backend, such as OpenTelemetry, receives, with the same trace and span IDs,
// in the same rings and series as when it is the driver. Where the telemetry
// has no trace or metric driver of its own, the attached driver fills in for
// it (the first attached wins) and then records each span and measurement
// once. Start the driver yourself: Observe does not, and a driver that is not
// started discards everything.
//
// Span and trace IDs come from the runtime's randomly seeded generator, which
// is cheap and sufficient for uniqueness; they are not secrets. A span
// inherits the sampled flag of its parent and is sampled when it has none.
//
// Example:
//
//	store := memorytelemetry.New()
//	if err := store.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
//	tools := store.MCPTools("memory")
//	_ = tools
type Driver struct {
	// config owns completed capacities, detached from the construction draft.
	config settings

	// active is true between Start and Stop and gates the hot path.
	active atomic.Bool

	// lifecycle serializes Start and Stop and guards state.
	lifecycle sync.Mutex

	// state is the lifecycle position.
	state lifecycleState

	// mu guards every field below.
	mu sync.Mutex

	// resource is the resource received by Start.
	resource telemetry.Resource

	// ring holds finished spans; once it reaches MaxSpans, oldest is the
	// index of the next slot to overwrite.
	ring []Span
	// oldest is the next span slot to overwrite once the ring reaches capacity.
	oldest int

	// logRing holds exported log records; once it reaches MaxLogs, logOldest
	// is the index of the next slot to overwrite.
	logRing []Log
	// logOldest is the next log slot to overwrite once the ring reaches capacity.
	logOldest int

	// logSeq is the Seq of the newest exported record, 0 before the first.
	// Reset keeps it so Seq never goes backwards.
	logSeq uint64

	// logInstance binds MCP cursors to this store generation. Reset changes it
	// even when the ring is empty, so lossless polling notices capture resets.
	logInstance string

	// logChanged is closed and cleared by the next export or Reset; nil
	// until LogsChanged is called.
	logChanged chan struct{}

	// instruments holds the instrument states by scope and name, and order
	// the same states in creation order.
	instruments map[string]*instrumentState
	// order preserves instrument registration order in metric snapshots.
	order []*instrumentState
}

var (
	_ telemetry.TraceDriver  = (*Driver)(nil)
	_ telemetry.MetricDriver = (*Driver)(nil)
	_ telemetry.LogDriver    = (*Driver)(nil)
)

// New returns a Driver with default recording capacities and applies callbacks
// directly once, synchronously in order on one fresh draft. Supplied nil
// callbacks and callback panics propagate; omitted callbacks and expanded
// nil/empty slices make no edits. Publication copies the completed settings.
// It performs no I/O; call Start before recording.
//
//	store := memorytelemetry.New(func(store *memorytelemetry.Builder) {
//		store.MaxSpans(500)
//	})
func New(configure ...BuilderFunc) *Driver {
	store := &Builder{settings: defaultConfig()}

	for _, configure := range configure {
		configure(store)
	}

	return &Driver{
		config:      store.settings,
		logInstance: diagnostic.NewInstance(),
		instruments: make(map[string]*instrumentState),
	}
}

// Start begins recording. It returns an error wrapping
// [telemetry.ErrStarted] on a second call and [telemetry.ErrStopped] after
// Stop.
//
//	store := memorytelemetry.New()
//	if err := store.Start(ctx, telemetry.Setup{Resource: telemetry.Resource{Service: "billing"}}); err != nil {
//		return err
//	}
//	if err := store.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(_ context.Context, setup telemetry.Setup) error {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	switch driver.state {
	case stateStarted:
		return fmt.Errorf("start memory telemetry: %w", telemetry.ErrStarted)
	case stateStopped:
		return fmt.Errorf("start memory telemetry: %w", telemetry.ErrStopped)
	}

	driver.mu.Lock()

	driver.resource = setup.Resource
	driver.resource.Attrs = slices.Clone(setup.Resource.Attrs)
	driver.mu.Unlock()

	driver.state = stateStarted
	driver.active.Store(true)

	return nil
}

// Stop stops recording. Recorded data stays readable through
// [Driver.Spans], [Driver.Logs], and [Driver.Metrics]. It is idempotent and never fails.
// It synchronizes with aggregate and ring commits, but does not wait for caller
// LogValuer or error-formatting callbacks. Pending recordings are dropped after
// their callbacks return. Even an expired context releases the recording state;
// this in-process driver owns no external resources to flush.
//
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
//	logs := driver.Logs() // Still readable; no pending export can append now.
//	_ = logs
func (driver *Driver) Stop(context.Context) error {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	driver.state = stateStopped
	driver.mu.Lock()
	driver.active.Store(false)
	driver.mu.Unlock()

	return nil
}

// Resource returns the resource the driver was started with.
// Its attribute container is copied; editing that container does not change
// future snapshots. Before Start the resource is the zero value.
//
//	store := memorytelemetry.New()
//	if err := store.Start(ctx, telemetry.Setup{Resource: telemetry.Resource{Service: "billing"}}); err != nil {
//		return err
//	}
//	fmt.Println(store.Resource().Service) // billing
//	if err := store.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Resource() telemetry.Resource {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	resource := driver.resource
	resource.Attrs = slices.Clone(resource.Attrs)

	return resource
}

// Reset discards recorded spans, logs, and metric values. Instruments stay
// registered, so live instruments keep recording. It wakes
// [Driver.LogsChanged] waiters but does not restart [Log.Seq], so a log
// follower sees a gap, not a repeat, after a Reset.
// MCP continuation cursors instead observe a changed store generation and
// receive an empty instance_changed result anchored at the fresh tail.
//
// Example:
//
//	store := memorytelemetry.New()
//	store.Reset()
//	if len(store.Logs()) != 0 {
//		panic("capture was not cleared")
//	}
func (driver *Driver) Reset() {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	driver.ring, driver.oldest = nil, 0
	driver.logRing, driver.logOldest = nil, 0
	driver.logInstance = diagnostic.NewInstance()
	driver.notifyLogsLocked()

	for _, state := range driver.order {
		state.series.Reset()
	}
}

// StartSpan starts a span as a child of the current span of ctx. It returns
// ctx unchanged. The span records attributes and events only when
// the capacity set by [Builder.MaxSpans] is positive.
// With store already started, the raw recorder exposes selected parent metadata:
//
//	_, recorder := store.StartSpan(ctx, telemetry.SpanStart{Name: "cache get", Kind: telemetry.Client})
//	fmt.Println(recorder.Context().IsValid(), recorder.Parent().IsValid())
//	recorder.End()
func (driver *Driver) StartSpan(ctx context.Context, start telemetry.SpanStart) (context.Context, telemetry.SpanRecorder) {
	if !driver.active.Load() {
		return ctx, inactiveSpan{}
	}

	parent := telemetry.SpanFromContext(ctx).Context()
	spanContext := telemetry.SpanContext{
		TraceID: parent.TraceID,
		SpanID:  newSpanID(),
		Sampled: true,
	}

	if parent.IsValid() {
		spanContext.Sampled = parent.Sampled
		spanContext.TraceState = parent.TraceState
	} else {
		parent = telemetry.SpanContext{}
		spanContext.TraceID = newTraceID()
	}

	recorder := &spanRecorder{
		driver:    driver,
		recording: driver.config.MaxSpans > 0,
		span: Span{
			Scope:   start.Scope,
			Name:    start.Name,
			Kind:    cmp.Or(start.Kind, telemetry.Internal),
			Status:  telemetry.Unset,
			Context: spanContext,
			Parent:  parent,
			Start:   time.Now(),
		},
	}

	if recorder.recording {
		recorder.setAttributes(start.Attrs)
	}

	return ctx, recorder
}

// Inject writes traceparent, and tracestate when present, for the current
// span of ctx.
// With a started store and a context carrying a span, propagate into an owned map:
//
//	carrier := telemetry.MapCarrier{}
//	store.Inject(ctx, carrier)
//	fmt.Println(carrier.Get("traceparent"))
func (driver *Driver) Inject(ctx context.Context, carrier telemetry.Carrier) {
	if !driver.active.Load() {
		return
	}

	spanContext := telemetry.SpanFromContext(ctx).Context()

	if !spanContext.IsValid() {
		return
	}

	carrier.Set(traceparentKey, formatTraceparent(spanContext))

	if spanContext.TraceState != "" {
		carrier.Set(tracestateKey, spanContext.TraceState)
	}
}

// Extract reads traceparent and tracestate. Malformed headers yield the zero
// span context.
// This raw operation does not authenticate the sender; the caller applies trust.
//
//	ctx, parent := store.Extract(ctx, carrier)
//	if parent.IsValid() {
//		fmt.Println(parent.TraceID.String())
//	}
//	_ = ctx
func (driver *Driver) Extract(ctx context.Context, carrier telemetry.Carrier) (context.Context, telemetry.SpanContext) {
	if !driver.active.Load() {
		return ctx, telemetry.SpanContext{}
	}

	spanContext, ok := parseTraceparent(carrier.Get(traceparentKey))

	if !ok {
		return ctx, telemetry.SpanContext{}
	}

	spanContext.Remote = true
	spanContext.TraceState = carrier.Get(tracestateKey)

	return ctx, spanContext
}

// Spans returns a snapshot of the finished spans, oldest first. Mutating it
// does not affect the driver.
// Only finished, retained spans appear; ongoing and evicted spans are absent.
// The eager snapshot remains stable across ring eviction and Reset. The
// collection wrapper adds operations; cloning here provides its ownership.
//
//	failed := store.Spans().Filter(func(_ int, span memorytelemetry.Span) bool {
//		return span.Status == telemetry.Error
//	})
//	for _, span := range failed {
//		fmt.Println(span.Name, span.Parent.IsValid(), span.End.Sub(span.Start))
//	}
func (driver *Driver) Spans() collection.Slice[Span] {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	spans := make([]Span, 0, len(driver.ring))

	for _, span := range slices.Concat(driver.ring[driver.oldest:], driver.ring[:driver.oldest]) {
		spans = append(spans, capture.CloneSpan(span))
	}

	return collection.NewSlice(spans)
}

// finish stores a finished span in the ring, dropping the oldest when full.
// The under-lock active check serializes its commit with Stop; the initial
// check only avoids lock acquisition on the usual disabled path.
func (driver *Driver) finish(span Span) {
	if !driver.active.Load() {
		return
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if !driver.active.Load() {
		return
	}

	if len(driver.ring) < driver.config.MaxSpans {
		driver.ring = append(driver.ring, span)

		return
	}

	driver.ring[driver.oldest] = span
	driver.oldest = (driver.oldest + 1) % len(driver.ring)
}

// newTraceID returns a random non-zero trace ID.
func newTraceID() telemetry.TraceID {
	var id telemetry.TraceID

	for id.IsZero() {
		high, low := rand.Uint64(), rand.Uint64()

		for index := range 8 {
			id[index] = byte(high >> (8 * index))
			id[index+8] = byte(low >> (8 * index))
		}
	}

	return id
}

// newSpanID returns a random non-zero span ID.
func newSpanID() telemetry.SpanID {
	var id telemetry.SpanID

	for id.IsZero() {
		value := rand.Uint64()

		for index := range 8 {
			id[index] = byte(value >> (8 * index))
		}
	}

	return id
}
