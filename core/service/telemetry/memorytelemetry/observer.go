package memorytelemetry

import (
	"context"
	"log/slog"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry/internal/capture"
)

var _ telemetry.Observer = (*Driver)(nil)

// SpanEnded stores span in the ring of finished spans, dropping the oldest
// when it is full. It is how the driver keeps a passive copy of the spans of
// another telemetry backend, with that backend's IDs; see [Driver] and
// [telemetry.Telemetry.Observe]. The span is copied. Nothing is stored when
// the driver is not started or the [Builder.MaxSpans] capacity is not positive.
// With capture already started, a forwarding observer may submit a completed
// snapshot; ordinary facade users attach capture through Telemetry.Observe.
//
//	capture.SpanEnded(telemetry.FinishedSpan{
//		Name: "cache get", Kind: telemetry.Internal, Status: telemetry.Unset,
//		Context: telemetry.SpanContext{TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}},
//	})
//	fmt.Println(len(capture.Spans()))
func (driver *Driver) SpanEnded(span telemetry.FinishedSpan) {
	if driver.config.MaxSpans > 0 {
		driver.finish(capture.CloneSpan(span))
	}
}

// Logged stores record in the ring of logs exactly as [Driver.Export] does,
// with ctx supplying [Log.Context] and the next [Log.Seq]. The driver keeps
// every level of what it is handed and discards records when it is not
// started or the [Builder.MaxLogs] capacity is not positive.
// With capture already started, forwarded records retain their original context:
//
//	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "operation completed", 0)
//	capture.Logged(ctx, record)
//	fmt.Println(len(capture.Logs()))
func (driver *Driver) Logged(ctx context.Context, record slog.Record) {
	// Export never fails; its error result only satisfies the LogDriver
	// contract.
	_ = driver.Export(ctx, record)
}

// Measured aggregates one recording into the series of the instrument spec
// describes, creating the instrument on first use, exactly as a recorder
// from [Driver.Instrument] would. Recordings that would exceed
// the [Builder.MaxSeries] capacity are dropped, and nothing is kept when the driver is not
// started.
// With capture already started, raw observation supplies a complete schema and
// one label value per key; capture owns independent schema and value containers.
//
//	spec := telemetry.InstrumentSpec{Name: "requests", Kind: telemetry.CounterKind, Keys: []string{"result"}}
//	capture.Measured(spec, 1, []string{"hit"})
//	fmt.Println(len(capture.Metrics()))
func (driver *Driver) Measured(spec telemetry.InstrumentSpec, value float64, values []string) {
	if !driver.active.Load() {
		return
	}

	driver.record(driver.instrumentState(spec), value, values)
}
