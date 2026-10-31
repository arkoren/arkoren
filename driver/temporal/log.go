package temporal

import (
	"context"
	"log/slog"

	tlog "go.temporal.io/sdk/log"
)

// logAdapter adapts a [slog.Logger] to the Temporal SDK logger.
type logAdapter struct {
	// logger receives operational records without changing the engine's execution
	// state.
	logger *slog.Logger
}

// newLogAdapter returns a Temporal logger that writes to logger.
func newLogAdapter(logger *slog.Logger) tlog.Logger {
	return logAdapter{
		logger: logger,
	}
}

// Debug forwards SDK diagnostics at slog's debug level. The SDK logger API has
// no context, so the host adapter uses Background rather than retaining a caller.
//
// Example:
//
//	adapter := newLogAdapter(slog.New(slog.DiscardHandler))
//	adapter.Debug("poll started", "queue", "orders")
func (adapter logAdapter) Debug(msg string, keyvals ...any) {
	adapter.logger.Log(context.Background(), slog.LevelDebug, msg, keyvals...)
}

// Info forwards SDK operational records at slog's info level. Native key/value
// arguments keep their slog interpretation and configured handler filtering.
//
// Example:
//
//	adapter := newLogAdapter(slog.New(slog.DiscardHandler))
//	adapter.Info("worker started", "queue", "orders")
func (adapter logAdapter) Info(msg string, keyvals ...any) {
	adapter.logger.Log(context.Background(), slog.LevelInfo, msg, keyvals...)
}

// Warn forwards SDK warnings at slog's warning level without changing the
// worker's control flow or claiming the warning is a terminal failure.
//
// Example:
//
//	adapter := newLogAdapter(slog.New(slog.DiscardHandler))
//	adapter.Warn("poll delayed", "queue", "orders")
func (adapter logAdapter) Warn(msg string, keyvals ...any) {
	adapter.logger.Log(context.Background(), slog.LevelWarn, msg, keyvals...)
}

// Error forwards SDK failures at slog's error level. This is a diagnostic
// observation; SDK retry and lifecycle policy still control the worker outcome.
//
// Example:
//
//	adapter := newLogAdapter(slog.New(slog.DiscardHandler))
//	adapter.Error("activity failed", "activity", "charge")
func (adapter logAdapter) Error(msg string, keyvals ...any) {
	adapter.logger.Log(context.Background(), slog.LevelError, msg, keyvals...)
}
