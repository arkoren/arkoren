package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"arkoren.dev/core/internal/semconv"
)

// Observer is a passive receiver of everything a [Telemetry] produces. It is
// how a component that is not the telemetry backend, such as the inspector,
// keeps its own copy of spans, logs, and measurements: whatever the real
// driver records, the observer sees too, with the same trace and span IDs,
// because the facade hands it the calls made through Arkoren instrumentation.
// Finished-span timing and backend-selected parent metadata are described by
// [FinishedSpan]; observation is not a native SDK export stream.
//
// Attach an observer with [Telemetry.Observe]. An observer is a single
// interface on purpose: one that wants one signal ignores the other two
// methods. Every method is called synchronously on the goroutine that
// produced the signal, possibly from many goroutines at once, so
// implementations must be safe for concurrent use, fast, and must neither
// block nor panic. They must not call back into the observed [Telemetry] in a
// way that would produce another signal from the same method.
//
// An observer that also implements [TraceDriver] or [MetricDriver] can fill
// in for a signal the telemetry has no driver for; see [Telemetry.Observe].
//
//	capture := memorytelemetry.New()
//	if err := capture.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
//	unsubscribe := service.Observe(capture)
//	_, span := service.Tracer("example.com/cache").Start(ctx, "cache get")
//	span.End()
//	unsubscribe()
//	if err := capture.Stop(ctx); err != nil {
//		return err
//	}
type Observer interface {
	// SpanEnded is called once for every span at the moment it ends, after
	// the driver's recorder ended it. span and its slices are never touched
	// again by the facade and are shared by all observers: keep them as they
	// are, but do not modify them.
	// With capture already started, an observer can be called at this seam:
	//
	//	var observer telemetry.Observer = capture
	//	observer.SpanEnded(telemetry.FinishedSpan{
	//		Name: "cache get", Kind: telemetry.Internal, Status: telemetry.Unset,
	//		Context: telemetry.SpanContext{TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}},
	//	})
	SpanEnded(span FinishedSpan)

	// Logged is called for every record that passes through the handler
	// returned by [Telemetry.Handler] and that the handler's logger emits,
	// that is, when the wrapped handler or the log driver wants its level,
	// whether or not a [LogDriver] is configured. ctx is the context the
	// record was logged with, so [SpanFromContext](ctx).Context() gives the
	// trace and span IDs; the record itself carries no [LogTraceID] or
	// [LogSpanID] attributes and is self-contained (attributes and groups
	// added through WithAttrs and WithGroup are already part of it), exactly
	// what [LogDriver.Export] receives. The observer must not retain record
	// without cloning it.
	// With a started capture, forward one self-contained record:
	//
	//	var observer telemetry.Observer = capture
	//	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "operation completed", 0)
	//	observer.Logged(ctx, record)
	Logged(ctx context.Context, record slog.Record)

	// Measured is called for every recording of every enabled instrument,
	// after the metric driver recorded it. spec is the instrument as the
	// driver sees it and is shared: do not modify it. values holds one label
	// value per declared key, in order, and is only valid during the call.
	// With a started capture, every declared key needs one value:
	//
	//	var observer telemetry.Observer = capture
	//	spec := telemetry.InstrumentSpec{Name: "requests", Kind: telemetry.CounterKind, Keys: []string{"result"}}
	//	observer.Measured(spec, 1, []string{"hit"})
	Measured(spec InstrumentSpec, value float64, values []string)
}

// FinishedSpan is the snapshot of a span handed to [Observer.SpanEnded]. It is
// the same shape the [memory driver] stores. Containers are shared read-only
// across observers; copy them before editing an application-owned view.
//
// Context and Parent come from the recorder after backend StartSpan. Parent is
// the actual selected parent, including when a native parent takes precedence
// over an adopted facade identity; zero identifies a backend-selected root.
// Attributes, events, and status reflect calls through the facade. Start, End,
// and event times are taken by the facade and can differ from backend timestamps.
//
//	span := telemetry.FinishedSpan{Attrs: []telemetry.Attr{telemetry.String("region", "eu")}}
//	if attr, ok := span.Attr("region"); ok {
//		fmt.Println(attr.AsString())
//	}
//
// [memory driver]: https://pkg.go.dev/arkoren.dev/core/service/telemetry/memorytelemetry#Driver
type FinishedSpan struct {
	// Scope is the instrumentation scope of the tracer that started it.
	// Use it to distinguish instrumentation libraries that reuse span names.
	//
	//	span := telemetry.FinishedSpan{Scope: "example.com/cache", Name: "cache get"}
	//	fmt.Println(span.Scope, span.Name)
	Scope string

	// Name is the span name.
	// It is a low-cardinality operation name, not a request URL or identifier.
	//
	//	span := telemetry.FinishedSpan{Name: "cache get"}
	//	fmt.Println(span.Name)
	Name string

	// Kind is the span kind.
	// It describes the operation's role rather than its success or failure.
	//
	//	span := telemetry.FinishedSpan{Name: "GET /users/{id}", Kind: telemetry.Server}
	//	fmt.Println(span.Kind == telemetry.Server)
	Kind SpanKind

	// Context is the identity of the span, as the driver's recorder reports
	// it.
	//
	//	span := telemetry.FinishedSpan{Context: telemetry.SpanContext{
	//		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2},
	//	}}
	//	fmt.Println(span.Context.IsValid())
	Context SpanContext

	// Parent is the actual parent selected by the backend, local or remote,
	// or zero for a root. It is captured from SpanRecorder.Parent after the
	// child starts, without changing backend parent selection or context.
	//
	//	if span.Parent.IsValid() {
	//		fmt.Println(span.Parent.SpanID.String())
	//	}
	Parent SpanContext

	// Start and End are the wall-clock times of Start and End.
	// They are facade observations and may differ from native SDK timestamps.
	//
	//	start := time.Unix(0, 0)
	//	span := telemetry.FinishedSpan{Start: start, End: start.Add(250 * time.Millisecond)}
	//	fmt.Println(span.End.Sub(span.Start)) // 250ms
	Start, End time.Time

	// Attrs are the span attributes in the order they were first set; a
	// later attribute with the same key replaces the value in place.
	// The container is shared read-only with other observers.
	//
	//	span := telemetry.FinishedSpan{Attrs: []telemetry.Attr{telemetry.String("region", "eu")}}
	//	if attr, ok := span.Attr("region"); ok {
	//		fmt.Println(attr.AsString())
	//	}
	Attrs []Attr

	// Events are the recorded events in order. Recorded errors are events
	// named "exception" with the attributes exception.type and
	// exception.message.
	//
	//	span := telemetry.FinishedSpan{Events: []telemetry.SpanEvent{{Name: "retry"}}}
	//	for _, event := range span.Events {
	//		fmt.Println(event.Name)
	//	}
	Events []SpanEvent

	// Status is the final status.
	// Unset does not imply failure; normal successful HTTP spans leave it unset.
	//
	//	span := telemetry.FinishedSpan{Status: telemetry.Error, StatusDescription: "backend unavailable"}
	//	if span.Status == telemetry.Error {
	//		fmt.Println(span.StatusDescription)
	//	}
	Status StatusCode

	// StatusDescription is the description given with an [Error] status.
	// It is empty for other statuses and must not contain sensitive error text.
	//
	//	span := telemetry.FinishedSpan{Status: telemetry.Error, StatusDescription: "backend unavailable"}
	//	fmt.Println(span.StatusDescription)
	StatusDescription string
}

// Attr returns the span attribute with the given key.
// The returned Attr is immutable; absence is reported by false, not an error.
//
//	span := telemetry.FinishedSpan{Attrs: []telemetry.Attr{telemetry.Int("attempt", 2)}}
//	if attr, ok := span.Attr("attempt"); ok {
//		fmt.Println(attr.AsInt64())
//	}
func (span FinishedSpan) Attr(key string) (Attr, bool) {
	index := slices.IndexFunc(span.Attrs, func(attr Attr) bool {
		return attr.Key() == key
	})

	if index < 0 {
		return Attr{}, false
	}

	return span.Attrs[index], true
}

// SpanEvent is a timestamped annotation on a [FinishedSpan].
// Attribute containers in observer notifications are shared read-only, like
// the containing finished span; copy containers before constructing an edited view.
//
//	event := telemetry.SpanEvent{
//		Name: "retry", Time: time.Unix(0, 0), Attrs: []telemetry.Attr{telemetry.Int("attempt", 2)},
//	}
//	fmt.Println(event.Name, event.Attrs[0].AsInt64())
type SpanEvent struct {
	// Name is the event name.
	// Use an operation-level label, not payload text.
	//
	//	event := telemetry.SpanEvent{Name: "retry"}
	//	fmt.Println(event.Name)
	Name string

	// Time is when the event was recorded.
	// It is an observed timestamp rather than the time a backend exported it.
	//
	//	event := telemetry.SpanEvent{Name: "retry", Time: time.Unix(0, 0)}
	//	fmt.Println(event.Time.UTC().Format(time.RFC3339))
	Time time.Time

	// Attrs are the event attributes.
	// The Attr values are immutable; observer code must not edit the shared slice.
	//
	//	event := telemetry.SpanEvent{Name: "retry", Attrs: []telemetry.Attr{telemetry.Int("attempt", 2)}}
	//	fmt.Println(event.Attrs[0].AsInt64())
	Attrs []Attr
}

// subscription is one [Telemetry.Observe] call. It is a pointer so the same
// observer attached twice unsubscribes independently.
type subscription struct {
	// observer is the subscribed observer.
	observer Observer
}

// signals is the immutable routing table the hot paths read with one atomic
// load. It is rebuilt, never modified, when observers come and go, so a
// span or recording that already took a table keeps using it.
type signals struct {
	// traces and metrics are the drivers in effect: the configured one, or
	// else the first attached observer that implements the capability. Nil
	// when the signal has no driver at all.
	traces TraceDriver
	// metrics is the metric driver selected for this immutable routing snapshot.
	metrics MetricDriver

	// metricSource is the binding identity originating from the subscription
	// supplying metrics. It remains stable while the backend is unchanged,
	// even if another subscription to the same comparable value takes over.
	// Noncomparable values retain identity only within their own subscription.
	metricSource *subscription

	// spans, measures, and logs are the observers that receive each signal.
	// An observer that is itself the driver in effect for a signal is left
	// out of that signal's list, because the driver already records it.
	spans []Observer
	// measures receives measurements unless an observer is already the metric driver.
	measures []Observer
	// logs receives log records unless an observer is already the log exporter.
	logs []Observer
}

// Observe attaches observer for as long as the returned function has not been
// called. It is a runtime subscription, like [event listeners], not a setter:
// any number of observers can be attached and detached while traffic flows,
// and the observed [Telemetry] and its configured drivers are unchanged. It is
// safe for concurrent use. The returned unsubscribe function is idempotent and
// safe to call concurrently; a nil observer attaches nothing.
//
// Notifications are admitted only while telemetry is enabled (see
// [Telemetry.Enabled]). An admitted callback may complete after Stop; Stop
// neither waits for it nor cancels it.
// Spans that started before Observe are not delivered, since their capture
// begins at start; spans that started while observed are delivered even if
// the observer detached meanwhile, provided telemetry is still enabled when
// completion delivery is admitted. Stop suppresses deliveries not yet admitted.
// Noncomparable driver values in separate registrations have
// no shared value identity; use pointers to share one backend across roles.
// With no observer attached the cost on every hot path is one atomic load.
//
// # Filling in missing drivers
//
// When telemetry has no [TraceDriver] (or no [MetricDriver]) and an attached observer
// also implements it, that observer acts as the driver for the signal while it
// stays attached: spans get IDs and a recorder, [Telemetry.Inject] and
// [Telemetry.Extract] propagate, trace IDs appear in logs and in
// [Middleware] responses, all with no other telemetry configured. If several
// observers qualify, the first one attached wins; others still observe. The
// observer in the driver role is not also called through [Observer], so
// the [memory driver] records every span and measurement exactly once.
// When the last qualifying observer detaches the signal reverts to no-op;
// spans already started finish on the recorder they started with. A
// configured driver is never replaced.
//
// Observe neither starts nor stops observers: a driver-capable observer
// (such as the [memory driver]) must be started by its owner to record.
// The facade itself must be started, which also means a no-op [New](nil) must be
// started to be observed. The initial shared [Default] begins unstarted;
// Observe alone does not enable it. Prefer an explicitly started [New](nil)
// facade for observer-only telemetry, then publish it with [SetDefault] if needed.
//
// With service already started, the observer's owner starts and stops its own
// capture independently:
//
//	memory := memorytelemetry.New()
//	if err := memory.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
//	unsubscribe := service.Observe(memory)
//	_, span := service.Tracer("example.com/cache").Start(ctx, "get")
//	span.End()
//	unsubscribe()
//	if err := memory.Stop(ctx); err != nil {
//		return err
//	}
//
// [event listeners]: https://pkg.go.dev/arkoren.dev/core/event#Bus.Listen
// [memory driver]: https://pkg.go.dev/arkoren.dev/core/service/telemetry/memorytelemetry#Driver
func (telemetry *Telemetry) Observe(observer Observer) (unsubscribe func()) {
	if observer == nil {
		return func() {
		}
	}

	attached := &subscription{observer: observer}

	telemetry.observeMu.Lock()

	telemetry.subscriptions = append(slices.Clone(telemetry.subscriptions), attached)
	telemetry.publishSignals()
	telemetry.observeMu.Unlock()

	var once sync.Once

	return func() {
		once.Do(func() {
			telemetry.observeMu.Lock()
			defer telemetry.observeMu.Unlock()

			telemetry.subscriptions = slices.DeleteFunc(slices.Clone(telemetry.subscriptions), func(other *subscription) bool {
				return other == attached
			})
			telemetry.publishSignals()
		})
	}
}

// publishSignals rebuilds the routing table from the configured drivers and
// the current subscriptions and publishes it. The caller holds observeMu.
func (telemetry *Telemetry) publishSignals() {
	table := &signals{
		traces:  telemetry.traces,
		metrics: telemetry.metrics,
	}
	// traceSource is needed only while routing this snapshot. Metric source
	// identity also remains in the published table for instrument bindings.
	var traceSource *subscription

	for _, sub := range telemetry.subscriptions {
		if driver, ok := sub.observer.(TraceDriver); ok && table.traces == nil {
			table.traces, traceSource = driver, sub
		}

		if driver, ok := sub.observer.(MetricDriver); ok && table.metrics == nil {
			table.metrics, table.metricSource = driver, sub
		}
	}

	for _, sub := range telemetry.subscriptions {
		table.addObserver(sub, telemetry.logs, traceSource)
	}

	if previous := telemetry.signals.Load(); previous != nil && sameDriver(previous.metrics, table.metrics) {
		table.metricSource = previous.metricSource
	}

	telemetry.signals.Store(table)
}

// addObserver adds passive deliveries except where this subscription already
// supplies the signal as its driver. Comparable shared driver values are also
// excluded; noncomparable values in distinct registrations have no shared value
// identity, so callers use pointers when they want one driver to be shared.
func (table *signals) addObserver(sub *subscription, logs LogDriver, traceSource *subscription) {
	if sub != traceSource && !sameDriver(sub.observer, table.traces) {
		table.spans = append(table.spans, sub.observer)
	}

	if sub != table.metricSource && !sameDriver(sub.observer, table.metrics) {
		table.measures = append(table.measures, sub.observer)
	}

	if !sameDriver(sub.observer, logs) {
		table.logs = append(table.logs, sub.observer)
	}
}

// observedSpan wraps the driver's [SpanRecorder] of a span started while
// observers were attached, collecting what the observers receive when the
// span ends. It snapshots attribute containers before forwarding ownership to
// the backend, and ends the backend before notifying observers. Facade span
// admission ensures no recorder operation can race past backend End.
type observedSpan struct {
	// telemetry gates passive completion after Stop, independently of the
	// backend recorder, which still receives its required End call.
	telemetry *Telemetry

	// inner is the driver recorder.
	inner SpanRecorder

	// innerRecording is inner.Recording() at start, so a driver that keeps
	// no data is not asked to.
	innerRecording bool

	// observers receive the span at End. It is the list in effect at start.
	observers []Observer

	// mu guards span and ended.
	mu sync.Mutex

	// span accumulates the data.
	span FinishedSpan

	// ended is set by End.
	ended bool
}

// Context returns the identity the driver gave the span.
func (recorder *observedSpan) Context() SpanContext {
	return recorder.span.Context
}

// Parent returns the backend-selected parent captured at StartSpan. The
// snapshot metadata is immutable, so decoration never reselects a parent or
// depends on a context slot that native instrumentation may have superseded.
func (recorder *observedSpan) Parent() SpanContext {
	return recorder.span.Parent
}

// Recording is always true: observers want the data even when the driver
// keeps none, such as an unsampled span.
func (recorder *observedSpan) Recording() bool {
	return true
}

// SetAttributes merges attrs into the snapshot, replacing values of existing
// keys, then forwards them. The snapshot is updated first because the driver
// owns attrs once it has them.
func (recorder *observedSpan) SetAttributes(attrs []Attr) {
	recorder.mu.Lock()

	if !recorder.ended {
		recorder.span.Attrs = mergeAttrs(recorder.span.Attrs, attrs)
	}

	recorder.mu.Unlock()

	if recorder.innerRecording {
		recorder.inner.SetAttributes(attrs)
	}
}

// AddEvent keeps a copy of the event, then forwards it.
func (recorder *observedSpan) AddEvent(name string, attrs []Attr) {
	recorder.mu.Lock()

	if !recorder.ended {
		recorder.span.Events = append(recorder.span.Events, SpanEvent{
			Name:  name,
			Time:  time.Now(),
			Attrs: slices.Clone(attrs),
		})
	}

	recorder.mu.Unlock()

	if recorder.innerRecording {
		recorder.inner.AddEvent(name, attrs)
	}
}

// RecordError forwards err and keeps the exception event the way
// [SpanRecorder.RecordError] describes it. Error formatting runs outside mu:
// caller-provided Error methods may record additional span metadata.
func (recorder *observedSpan) RecordError(err error) {
	if recorder.innerRecording {
		recorder.inner.RecordError(err)
	}

	event := SpanEvent{
		Name: semconv.ExceptionEventName,
		Time: time.Now(),
		Attrs: []Attr{
			String(semconv.ExceptionType, fmt.Sprintf("%T", err)),
			String(semconv.ExceptionMessage, err.Error()),
		},
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if !recorder.ended {
		recorder.span.Events = append(recorder.span.Events, event)
	}
}

// SetStatus forwards the status and applies the rules of
// [SpanRecorder.SetStatus] to the snapshot.
func (recorder *observedSpan) SetStatus(code StatusCode, description string) {
	if recorder.innerRecording {
		recorder.inner.SetStatus(code, description)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if recorder.ended || recorder.span.Status == OK {
		return
	}

	switch code {
	case OK:
		recorder.span.Status, recorder.span.StatusDescription = OK, ""
	case Error:
		recorder.span.Status, recorder.span.StatusDescription = Error, description
	}
}

// End ends the driver recorder, then delivers the span to the observers.
func (recorder *observedSpan) End() {
	recorder.mu.Lock()

	if recorder.ended {
		recorder.mu.Unlock()

		return
	}

	recorder.ended = true
	recorder.span.End = time.Now()
	span := recorder.span
	recorder.mu.Unlock()

	recorder.inner.End()

	for _, observer := range recorder.observers {
		if !recorder.telemetry.active.Load() {
			return
		}

		observer.SpanEnded(span)
	}
}

// mergeAttrs adds attrs to existing, replacing the value of a key that is
// already there and appending new keys in order.
func mergeAttrs(existing, attrs []Attr) []Attr {
	for _, attr := range attrs {
		index := slices.IndexFunc(existing, func(other Attr) bool {
			return other.Key() == attr.Key()
		})

		if index >= 0 {
			existing[index] = attr

			continue
		}

		existing = append(existing, attr)
	}

	return existing
}
