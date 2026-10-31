package memorytelemetry

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry/internal/capture"
	"arkoren.dev/foundation/collection"
)

// Log is the snapshot of a log record exported to the driver.
// Obtain detached snapshots from Driver.Logs or Driver.LogsAfter; their order is
// export order, and Seq provides a continuation position across ring eviction.
//
//	for _, entry := range store.LogsAfter(0) {
//		fmt.Println(entry.Seq, entry.Level, entry.Message)
//	}
type Log struct {
	// Seq is the position of the record in the driver's stream of exported
	// records: assigned by [Driver.Export], 1 for the first record and
	// strictly increasing by one for each later one, and never reused. It
	// does not restart at [Driver.Reset], so a follower that remembers the
	// last Seq it saw never sees it go backwards. A gap between two
	// consecutive Seq values a reader sees means the ring evicted records
	// in between; see [Driver.LogsAfter].
	//
	//	lastSeq := uint64(0)
	//	for _, entry := range store.LogsAfter(lastSeq) {
	//		lastSeq = entry.Seq
	//	}
	//	fmt.Println(lastSeq)
	Seq uint64

	// Time is the time of the record. It is zero when the logger set none.
	// It is the emitted timestamp, not the time a snapshot was retrieved.
	//
	//	for _, entry := range store.Logs() {
	//		fmt.Println(entry.Time.UTC().Format(time.RFC3339Nano))
	//	}
	Time time.Time

	// Level is the severity of the record.
	// It retains slog's numeric level, including application-defined levels.
	//
	//	for _, entry := range store.Logs() {
	//		if entry.Level >= slog.LevelWarn {
	//			fmt.Println(entry.Message)
	//		}
	//	}
	Level slog.Level

	// Message is the log message.
	// It is application-supplied text, so producers must exclude secrets.
	//
	//	for _, entry := range store.Logs() {
	//		fmt.Println(entry.Message)
	//	}
	Message string

	// Context is the identity of the span in the context the record was
	// logged with, so a record joins its span by Context.SpanID. It is the
	// zero value, and not valid, when the context held no span.
	//
	//	for _, entry := range store.Logs() {
	//		if entry.Context.IsValid() {
	//			fmt.Println(entry.Context.TraceID.String())
	//		}
	//	}
	Context telemetry.SpanContext

	// Attrs are the record attributes in logging order, including those added
	// by WithAttrs and WithGroup. Attributes inside groups are flattened with
	// dotted keys, such as "http.method". Strings, integers, floats, and
	// booleans keep their type; every other value, such as a time, a duration,
	// or an error, is kept in its text form. The correlation attributes
	// trace_id and span_id are not included; use Context.
	//
	//	for _, entry := range store.Logs() {
	//		for _, attr := range entry.Attrs {
	//			fmt.Println(attr.Key(), attr.Value())
	//		}
	//	}
	Attrs []telemetry.Attr
}

// Enabled reports whether Export keeps a record: the driver is started and
// the [Builder.MaxLogs] capacity is positive. The driver keeps every level.
//
//	if store.Enabled(ctx, slog.LevelInfo) {
//		fmt.Println("capture accepts this record")
//	}
func (driver *Driver) Enabled(context.Context, slog.Level) bool {
	return driver.active.Load() && driver.config.MaxLogs > 0
}

// Export stores record in the ring of logs, dropping the oldest record when
// it is full, and never fails. It gives the record the next [Log.Seq] and
// wakes the channels returned by [Driver.LogsChanged]. It resolves LogValuer
// and text-formatting callbacks before taking the driver's mutex. These
// callbacks may block or reenter the driver; Export cannot cancel them.
// It discards the record, and
// assigns no Seq, when the driver is not started or the [Builder.MaxLogs] capacity is not
// positive. The record is copied, so the caller may reuse it.
// Stop synchronizes with the final commit, so an export whose callbacks finish
// after Stop cannot assign a Seq, append a log, or notify followers.
//
//	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "started", 0)
//	if err := driver.Export(ctx, record); err != nil {
//		return err
//	}
func (driver *Driver) Export(ctx context.Context, record slog.Record) error {
	if !driver.Enabled(ctx, record.Level) {
		return nil
	}

	entry := Log{
		Time:    record.Time,
		Level:   record.Level,
		Message: record.Message,
		Context: telemetry.SpanFromContext(ctx).Context(),
	}
	record.Attrs(func(attr slog.Attr) bool {
		entry.Attrs = capture.AppendLogAttr(entry.Attrs, "", attr)

		return true
	})

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if !driver.active.Load() {
		return nil
	}

	driver.logSeq++
	entry.Seq = driver.logSeq
	driver.notifyLogsLocked()

	if len(driver.logRing) < driver.config.MaxLogs {
		driver.logRing = append(driver.logRing, entry)

		return nil
	}

	driver.logRing[driver.logOldest] = entry
	driver.logOldest = (driver.logOldest + 1) % len(driver.logRing)

	return nil
}

// Logs returns a snapshot of the exported log records, oldest first.
// Mutating it does not affect the driver. It equals LogsAfter(0).
// This eager collection is not a live query and survives ring eviction or
// Reset; the snapshot copies, not its collection wrapper, provide ownership.
//
//	warnings := driver.Logs().Filter(func(_ int, entry memorytelemetry.Log) bool {
//		return entry.Level >= slog.LevelWarn
//	})
//	for _, entry := range warnings {
//		fmt.Println(entry.Level, entry.Message)
//	}
func (driver *Driver) Logs() collection.Slice[Log] {
	return driver.LogsAfter(0)
}

// LogsAfter returns a deep copy of the records still in the ring whose
// [Log.Seq] is greater than seq, oldest first. It returns nil when there are
// none, including when the [Builder.MaxLogs] capacity is not positive. Mutating the result
// does not affect the driver.
// The collection is eager: later exports and Reset cannot change its records
// or copied attributes. The wrapper itself performs no additional copying.
//
// A follower passes the Seq of the last record it handled. Seq values are
// consecutive, so the follower detects dropped records when the first
// returned Seq is greater than seq+1; the number dropped is
// first.Seq-seq-1. With seq 0 the first returned Seq above 1 means records
// were evicted or discarded by [Driver.Reset] before the call.
//
//	last := uint64(0)
//	for _, entry := range driver.LogsAfter(last) {
//		last = entry.Seq
//		fmt.Println(entry.Message)
//	}
func (driver *Driver) LogsAfter(seq uint64) collection.Slice[Log] {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	// The ring holds the newest len(logRing) records, so its oldest Seq is
	// derived from the counter instead of stored.
	oldestSeq := driver.logSeq + 1 - uint64(len(driver.logRing))
	start := 0

	if seq >= oldestSeq {
		start = int(min(seq-oldestSeq+1, uint64(len(driver.logRing))))
	}

	ordered := slices.Concat(driver.logRing[driver.logOldest:], driver.logRing[:driver.logOldest])

	if start == len(ordered) {
		return nil
	}

	logs := make([]Log, 0, len(ordered)-start)

	for _, entry := range ordered[start:] {
		entry.Attrs = slices.Clone(entry.Attrs)
		logs = append(logs, entry)
	}

	return collection.NewSlice(logs)
}

// LogsChanged returns a channel that is closed the next time a record is
// exported or [Driver.Reset] runs, whichever comes first. A closed channel
// stays closed; call LogsChanged again for the next change. It is how a
// follower waits without polling.
//
// To miss no record, take the channel before reading: a record exported
// between the two calls then closes the channel the follower already holds.
// With a nonpositive [Builder.MaxLogs] capacity nothing is ever exported, so the
// channel closes only at Reset.
//
//	for {
//		changed := driver.LogsChanged()
//		for _, entry := range driver.LogsAfter(last) {
//			last = entry.Seq
//		}
//		select {
//		case <-changed:
//		case <-ctx.Done():
//			return
//		}
//	}
func (driver *Driver) LogsChanged() <-chan struct{} {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.logChanged == nil {
		driver.logChanged = make(chan struct{})
	}

	return driver.logChanged
}

// notifyLogsLocked closes the channel handed out by LogsChanged, if any, so
// the next call makes a fresh one. Creating the channel lazily keeps Export
// allocation-free while nobody waits. The caller holds driver.mu.
func (driver *Driver) notifyLogsLocked() {
	if driver.logChanged != nil {
		close(driver.logChanged)

		driver.logChanged = nil
	}
}
