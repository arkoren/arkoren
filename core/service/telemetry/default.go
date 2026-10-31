package telemetry

import "sync/atomic"

// defaultTelemetry holds the process-wide Telemetry. It is nil until
// SetDefault is called.
var defaultTelemetry atomic.Pointer[Telemetry]

// noopTelemetry is returned by [Default] until [SetDefault] is called. It has
// no driver, so everything built from it records nothing.
var noopTelemetry = New(nil)

// Default returns the process-wide Telemetry, in the manner of
// [log/slog.Default]. Until [SetDefault] is called it is a no-op: spans are
// non-recording, no IDs are generated, and the hot path does not allocate.
//
// Components resolve the default when they use it, not when they are built,
// so calling [SetDefault] after building them still works.
//
//	ctx, span := telemetry.Default().
//		Tracer("example.com/billing").
//		Start(ctx, "charge")
//	defer span.End()
func Default() *Telemetry {
	if t := defaultTelemetry.Load(); t != nil {
		return t
	}

	return noopTelemetry
}

// SetDefault makes t the process-wide Telemetry. A nil t restores the no-op.
// It is safe for concurrent use. SetDefault does not start t.
//
//	telemetry.SetDefault(app.Telemetry())
func SetDefault(t *Telemetry) {
	defaultTelemetry.Store(t)
}
