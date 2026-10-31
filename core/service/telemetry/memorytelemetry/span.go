package memorytelemetry

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry/internal/capture"
)

// Span is the snapshot of a finished span: an alias of
// [telemetry.FinishedSpan], so the spans the driver stores as a trace driver
// and the ones it receives as an [telemetry.Observer] have one shape. Its
// Attrs are in the order they were first set and Events holds recorded errors
// as events named "exception".
// Parent is the actual backend-selected parent supplied by SpanRecorder.Parent,
// not a reconstruction from the context seen by a passive observer.
//
//	span := memorytelemetry.Span{Name: "cache get", Attrs: []telemetry.Attr{telemetry.Bool("cached", true)}}
//	if attr, ok := span.Attr("cached"); ok {
//		fmt.Println(attr.AsBool())
//	}
type Span = telemetry.FinishedSpan

// Event is a timestamped annotation on a [Span]: an alias of
// [telemetry.SpanEvent].
// The alias lets capture readers consume the contract's event shape unchanged.
//
//	event := memorytelemetry.Event{Name: "retry", Attrs: []telemetry.Attr{telemetry.Int("attempt", 2)}}
//	fmt.Println(event.Name, event.Attrs[0].AsInt64())
type Event = telemetry.SpanEvent

// inactiveSpan is the recorder returned before Start and after Stop.
type inactiveSpan struct{}

// Context returns the invalid span context.
func (inactiveSpan) Context() telemetry.SpanContext {
	return telemetry.SpanContext{}
}

// Parent returns zero because an inactive recorder creates no child and selects
// no parent, even if the incoming context contains an otherwise valid identity.
func (inactiveSpan) Parent() telemetry.SpanContext {
	return telemetry.SpanContext{}
}

// Recording reports false.
func (inactiveSpan) Recording() bool {
	return false
}

// SetAttributes does nothing.
func (inactiveSpan) SetAttributes([]telemetry.Attr) {
}

// AddEvent does nothing.
func (inactiveSpan) AddEvent(string, []telemetry.Attr) {
}

// RecordError does nothing.
func (inactiveSpan) RecordError(error) {
}

// SetStatus does nothing.
func (inactiveSpan) SetStatus(telemetry.StatusCode, string) {
}

// End does nothing.
func (inactiveSpan) End() {
}

// spanRecorder is the [telemetry.SpanRecorder] of one span.
type spanRecorder struct {
	// driver receives the span when it ends.
	driver *Driver

	// recording is false when the driver keeps IDs only.
	recording bool

	// mu guards span and ended.
	mu sync.Mutex

	// span accumulates the span data.
	span Span

	// ended is set by End.
	ended bool
}

// Context returns the span identity.
func (recorder *spanRecorder) Context() telemetry.SpanContext {
	return recorder.span.Context
}

// Parent returns the effective parent selected at creation, or zero for a root.
// This field never changes when attributes, status, or completion data change,
// so the metadata can be read concurrently with recording or End.
func (recorder *spanRecorder) Parent() telemetry.SpanContext {
	return recorder.span.Parent
}

// Recording reports whether the span keeps data.
func (recorder *spanRecorder) Recording() bool {
	return recorder.recording
}

// SetAttributes adds attributes, replacing values of existing keys.
func (recorder *spanRecorder) SetAttributes(attrs []telemetry.Attr) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if !recorder.ended && recorder.recording {
		recorder.setAttributes(attrs)
	}
}

// setAttributes merges attrs into the span. The caller holds mu or owns the
// recorder exclusively.
func (recorder *spanRecorder) setAttributes(attrs []telemetry.Attr) {
	for _, attr := range attrs {
		index := slices.IndexFunc(recorder.span.Attrs, func(existing telemetry.Attr) bool {
			return existing.Key() == attr.Key()
		})

		if index >= 0 {
			recorder.span.Attrs[index] = attr

			continue
		}

		recorder.span.Attrs = append(recorder.span.Attrs, attr)
	}
}

// AddEvent records an event.
func (recorder *spanRecorder) AddEvent(name string, attrs []telemetry.Attr) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if recorder.ended || !recorder.recording {
		return
	}

	recorder.span.Events = append(recorder.span.Events, Event{
		Name:  name,
		Time:  time.Now(),
		Attrs: slices.Clone(attrs),
	})
}

// RecordError records err as an exception event.
func (recorder *spanRecorder) RecordError(err error) {
	recorder.AddEvent(semconv.ExceptionEventName, []telemetry.Attr{
		telemetry.String(semconv.ExceptionType, fmt.Sprintf("%T", err)),
		telemetry.String(semconv.ExceptionMessage, err.Error()),
	})
}

// SetStatus applies the status rules of [telemetry.SpanRecorder].
func (recorder *spanRecorder) SetStatus(code telemetry.StatusCode, description string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if recorder.ended || !recorder.recording || recorder.span.Status == telemetry.OK {
		return
	}

	switch code {
	case telemetry.OK:
		recorder.span.Status, recorder.span.StatusDescription = telemetry.OK, ""
	case telemetry.Error:
		recorder.span.Status, recorder.span.StatusDescription = telemetry.Error, description
	}
}

// End finishes the span and stores it in the driver's ring.
func (recorder *spanRecorder) End() {
	recorder.mu.Lock()

	if recorder.ended {
		recorder.mu.Unlock()

		return
	}

	recorder.ended = true
	recorder.span.End = time.Now()
	span := capture.CloneSpan(recorder.span)
	recorder.mu.Unlock()

	if recorder.recording {
		recorder.driver.finish(span)
	}
}
