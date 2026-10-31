// Package memorytelemetry provides an in-process [telemetry.TraceDriver],
// [telemetry.MetricDriver], and [telemetry.LogDriver] for tests, local
// development, and applications that want trace IDs in logs and responses
// without a backend.
//
// The driver generates W3C trace and span IDs, propagates them with the
// traceparent and tracestate headers, keeps a bounded ring of finished spans
// and another of log records, and aggregates metrics in memory. It exports
// nothing. [Driver.Spans], [Driver.Logs], and [Driver.Metrics] return
// detached eager collection snapshots. Filtering and grouping operate on those
// snapshots, not live rings: later recordings, eviction, and Reset do not change
// them. The snapshot methods copy owned nested containers before wrapping;
// collection wrappers themselves add no ownership. Records reach the ring through
// [telemetry.Telemetry.Handler], which also adds trace_id and span_id to the
// wrapped logger's output; a [Log] carries the span identity of its context,
// so a record joins its span by span ID.
// Recorders returned by [Driver.StartSpan] expose the actual selected parent
// through [telemetry.SpanRecorder.Parent], including in IDs-only mode. Inactive
// recorders and roots return zero. Passive capture stores the backend parent
// reported by the facade; it does not reconstruct parentage from a context slot.
//
// Every [Log] carries a [Log.Seq], increasing from 1 and never reused, even
// across [Driver.Reset]. A follower tails the log ring without polling by
// waiting on [Driver.LogsChanged] and reading [Driver.LogsAfter] with the
// last Seq it handled; a first Seq above last+1 tells it records were
// dropped.
//
// [Driver.MCPTools] publishes typed, SDK-independent diagnostic tools for
// explicit composition. Log calls support bounded cursor polling: omitted
// cursor returns recent history, tail returns an immediate empty anchor, and
// continuation calls wait at most 25 seconds. Each response reports lost
// records and a new opaque cursor. Reset changes the capture generation even
// on an empty ring; a stale cursor returns instance_changed with a fresh tail.
//
//	tools := driver.MCPTools("memory")
//	for _, tool := range tools {
//		if err := tool.Err(); err != nil {
//			return err
//		}
//	}
//
// Set [Builder.MaxSpans] to 0 to generate IDs only and keep no spans, and
// [Builder.MaxLogs] to 0 to keep no log records. Each ring holds 1000 entries
// by default.
//
//	driver := memorytelemetry.New(func(store *memorytelemetry.Builder) {
//		store.MaxSpans(100)
//		store.MaxLogs(500)
//	})
//	app := application.New(func(app *application.Builder) {
//		app.Telemetry(telemetry.New(driver))
//	})
//
// New applies callbacks directly once, synchronously in order on one fresh
// Builder. Capacity settings return that same draft and replace earlier
// values. Publication copies the root, so retained drafts cannot resize a
// store. Omitted callbacks and nil/empty expanded slices keep defaults;
// supplied nil entries and callback panics propagate. telemetrytest.New
// forwards this construction contract once, separately from its suite options.
package memorytelemetry
