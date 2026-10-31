package execution

import (
	"context"
	"log/slog"

	tlog "go.temporal.io/sdk/log"
)

// logHandler is a [slog.Handler] that writes to a Temporal logger, which
// drops lines while the workflow replays.
type logHandler struct {
	// logger is the Temporal logger.
	logger tlog.Logger

	// attrs are the attributes added with WithAttrs, as key-value pairs.
	attrs []any

	// group is the key prefix added with WithGroup.
	group string
}

// newLogger returns a [slog.Logger] over logger.
func newLogger(logger tlog.Logger) *slog.Logger {
	return slog.New(logHandler{
		logger: logger,
	})
}

// Enabled delegates level filtering to the native Temporal logger by always
// accepting the slog record. It does not bypass the native logger's filtering.
//
// Example:
//
//	enabled := handler.Enabled(context.Background(), slog.LevelInfo)
//	_ = enabled // True; native filtering still applies when Handle forwards.
func (logHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

// Handle resolves LogValuers and nested groups before forwarding a record to
// the native logger at its matching level. Native loggers need not understand
// slog values, so forwarding unresolved values could bypass secret redaction.
//
// Example:
//
//	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "step finished", 0)
//	record.Add("outcome", "ok")
//	if err := handler.Handle(context.Background(), record); err != nil {
//		return err
//	}
func (handler logHandler) Handle(_ context.Context, record slog.Record) error {
	keyvals := append([]any(nil), handler.attrs...)

	record.Attrs(func(attr slog.Attr) bool {
		keyvals = appendLogAttrs(keyvals, handler.group, []slog.Attr{attr})

		return true
	})

	switch {
	case record.Level >= slog.LevelError:
		handler.logger.Error(record.Message, keyvals...)
	case record.Level >= slog.LevelWarn:
		handler.logger.Warn(record.Message, keyvals...)
	case record.Level >= slog.LevelInfo:
		handler.logger.Info(record.Message, keyvals...)
	default:
		handler.logger.Debug(record.Message, keyvals...)
	}

	return nil
}

// WithAttrs resolves and snapshots bound attributes for every later record.
// The returned handler owns its outer key/value slice; logger objects and
// arbitrary non-LogValuer values retain their native ownership.
//
// Example:
//
//	bound := handler.WithAttrs([]slog.Attr{slog.String("phase", "payment")})
//	slog.New(bound).InfoContext(context.Background(), "step finished")
func (handler logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := handler
	next.attrs = append([]any(nil), handler.attrs...)

	next.attrs = appendLogAttrs(next.attrs, handler.group, attrs)

	return next
}

// appendLogAttrs resolves safe slog values and recursively flattens groups into
// the native logger's dotted key/value convention. Empty group names inline
// their members; empty groups emit nothing. Resolve also bounds LogValuer cycles
// and converts LogValue panics according to slog's standard behavior.
func appendLogAttrs(values []any, prefix string, attrs []slog.Attr) []any {
	for _, attr := range attrs {
		value := attr.Value.Resolve()

		if value.Kind() != slog.KindGroup {
			values = append(values, prefix+attr.Key, value.Any())
			continue
		}

		group := prefix

		if attr.Key != "" {
			group += attr.Key + "."
		}

		values = appendLogAttrs(values, group, value.Group())
	}

	return values
}

// WithGroup returns a handler that prefixes later keys with name and a dot.
// Empty names leave the handler unchanged; previously bound attributes retain
// the group in which they were captured, matching slog's binding order.
//
// Example:
//
//	grouped := handler.WithGroup("billing")
//	slog.New(grouped).InfoContext(context.Background(), "step finished", "outcome", "ok")
func (handler logHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return handler
	}

	next := handler
	next.group = handler.group + name + "."

	return next
}
