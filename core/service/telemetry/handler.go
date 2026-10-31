package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"slices"
)

const (
	// LogTraceID is the log attribute that carries the trace ID.
	// Consumers can use the key without duplicating its spelling.
	//
	//	record.Attrs(func(attr slog.Attr) bool {
	//		if attr.Key == telemetry.LogTraceID {
	//			fmt.Println(attr.Value.String())
	//		}
	//		return true
	//	})
	LogTraceID = "trace_id"

	// LogSpanID is the log attribute that carries the span ID.
	// It identifies the span within the correlated trace, not a request ID.
	//
	//	record.Attrs(func(attr slog.Attr) bool {
	//		if attr.Key == telemetry.LogSpanID {
	//			fmt.Println(attr.Value.String())
	//		}
	//		return true
	//	})
	LogSpanID = "span_id"
)

// Handler wraps next so that records logged with a context holding a valid
// span get [LogTraceID] and [LogSpanID] attributes, and, when the driver
// implements [LogDriver], are also exported to it. Every record the logger
// emits, meaning next or the log driver wants its level, is also handed to
// the attached [Observer]s (see [Telemetry.Observe]) with the logging
// context, whether or not a log driver is configured; they get the same
// self-contained record as the driver, without the correlation attributes.
// Observers only see records while the telemetry is enabled.
//
// Correlation needs the context-aware slog methods (InfoContext and so on);
// plain Info passes context.Background(). The attributes are added to the
// record, so under an open group (WithGroup) they appear inside it.
//
// The returned handler follows the [slog.Handler] contract: Enabled is true
// when next or the exporter wants the level, and WithAttrs and WithGroup
// derive handlers that keep the correlation and export behavior. A nil next
// discards records locally. The handler resolves nothing at construction, so
// it may be built before Start; export begins when the Telemetry starts.
//
//	logger := slog.New(t.Handler(slog.NewJSONHandler(os.Stderr, nil)))
//	logger.InfoContext(ctx, "invoice sent") // ... "trace_id":"4bf9...","span_id":"00f0..."
func (telemetry *Telemetry) Handler(next slog.Handler) slog.Handler {
	if next == nil {
		next = slog.DiscardHandler
	}

	return &logHandler{
		telemetry: telemetry,
		next:      next,
	}
}

// logStep is one WithAttrs or WithGroup call, kept so exported records are
// self-contained.
type logStep struct {
	// group is the group name for a WithGroup step and "" for WithAttrs.
	group string

	// attrs are the attributes of a WithAttrs step.
	attrs []slog.Attr
}

// logHandler is the handler returned by [Telemetry.Handler].
type logHandler struct {
	// telemetry supplies the exporter and the started state.
	telemetry *Telemetry

	// next is the wrapped handler, already derived with the same steps.
	next slog.Handler

	// steps are the WithAttrs and WithGroup calls in order, replayed into
	// exported records.
	steps []logStep
}

// exporter returns the log driver when it is running, else nil.
func (handler *logHandler) exporter() LogDriver {
	if handler.telemetry.logs == nil || !handler.telemetry.active.Load() {
		return nil
	}

	return handler.telemetry.logs
}

// Enabled reports whether next or the exporter wants level.
func (handler *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if handler.next.Enabled(ctx, level) {
		return true
	}

	exporter := handler.exporter()

	return exporter != nil && exporter.Enabled(ctx, level)
}

// Handle logs record to next with correlation attributes, then exports it.
// Passive callbacks are admitted individually while telemetry remains active;
// a backend or observer that stops telemetry suppresses subsequent delivery,
// without interrupting a callback that already started. Independent local and
// exporter errors are joined after both wanted sinks have been attempted.
//
//	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "operation", 0)
//	if err := service.Handler(local).Handle(ctx, record); err != nil {
//		return err
//	}
func (handler *logHandler) Handle(ctx context.Context, record slog.Record) error {
	var logErr, exportErr error

	logWanted := handler.next.Enabled(ctx, record.Level)

	if logWanted {
		logErr = handler.next.Handle(ctx, correlate(ctx, record))
	}

	exporter := handler.exporter()
	exportWanted := exporter != nil && exporter.Enabled(ctx, record.Level)

	if !logWanted && !exportWanted {
		return logErr
	}

	observers := handler.telemetry.signals.Load().logs

	if !exportWanted && len(observers) == 0 {
		return logErr
	}

	folded := handler.selfContained(record)

	if exportWanted {
		exportErr = exporter.Export(ctx, folded)
	}

	for _, observer := range observers {
		if !handler.telemetry.active.Load() {
			break
		}

		observer.Logged(ctx, folded)
	}

	return errors.Join(logErr, exportErr)
}

// WithAttrs derives a handler with attrs added. The exporter metadata is copied
// before transferring the original slice to next, whose slog.Handler contract
// permits it to retain or rewrite that owned input.
//
//	handler := service.Handler(local).WithAttrs([]slog.Attr{slog.String("region", "eu")})
//	slog.New(handler).InfoContext(ctx, "operation")
func (handler *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return handler
	}

	step := logStep{attrs: slices.Clone(attrs)}

	return handler.derive(handler.next.WithAttrs(attrs), step)
}

// WithGroup derives a handler that nests following attributes under name.
func (handler *logHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return handler
	}

	return handler.derive(handler.next.WithGroup(name), logStep{group: name})
}

// derive returns a copy of handler over next with step appended.
func (handler *logHandler) derive(next slog.Handler, step logStep) *logHandler {
	return &logHandler{
		telemetry: handler.telemetry,
		next:      next,
		steps:     append(slices.Clone(handler.steps), step),
	}
}

// selfContained returns record with the handler's WithAttrs and WithGroup
// steps folded into its attributes, so a driver needs no handler state.
func (handler *logHandler) selfContained(record slog.Record) slog.Record {
	if len(handler.steps) == 0 {
		return record
	}

	current := make([]slog.Attr, 0, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		current = append(current, attr)

		return true
	})

	for _, step := range slices.Backward(handler.steps) {
		if step.group == "" {
			current = append(slices.Clone(step.attrs), current...)

			continue
		}

		if len(current) > 0 {
			current = []slog.Attr{{
				Key:   step.group,
				Value: slog.GroupValue(current...),
			}}
		}
	}

	folded := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	folded.AddAttrs(current...)

	return folded
}

// correlate returns record with trace and span IDs added when ctx has a valid
// span. The original record is not modified.
func correlate(ctx context.Context, record slog.Record) slog.Record {
	spanContext := SpanFromContext(ctx).Context()

	if !spanContext.IsValid() {
		return record
	}

	record = record.Clone()
	record.AddAttrs(
		slog.String(LogTraceID, spanContext.TraceID.String()),
		slog.String(LogSpanID, spanContext.SpanID.String()),
	)

	return record
}
