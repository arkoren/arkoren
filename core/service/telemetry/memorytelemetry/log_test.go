package memorytelemetry_test

import (
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// TestLogsJoinSpanAndFlattenAttrs checks the span identity, the dotted keys of
// groups, the kept attribute types, and the text form of other values.
func TestLogsJoinSpanAndFlattenAttrs(t *testing.T) {
	t.Parallel()

	driver, facade := start(t)
	logger := slog.New(facade.Handler(slog.DiscardHandler))

	ctx, span := facade.Tracer("t").Start(t.Context(), "work")
	logger.With("a", 1).WithGroup("g").ErrorContext(ctx, "failed",
		"ok", true, "ratio", 0.5, "took", time.Second, slog.Group("inner", slog.String("k", "v")))
	span.End()

	logs := driver.Logs()

	if len(logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs))
	}

	entry := logs[0]

	if entry.Level != slog.LevelError || entry.Message != "failed" || entry.Time.IsZero() {
		t.Fatalf("entry = %+v", entry)
	}

	if entry.Context.SpanID != span.Context().SpanID || entry.Context.TraceID != span.Context().TraceID {
		t.Fatalf("context = %+v, want span %+v", entry.Context, span.Context())
	}

	want := []telemetry.Attr{
		telemetry.Int64("a", 1),
		telemetry.Bool("g.ok", true),
		telemetry.Float64("g.ratio", 0.5),
		telemetry.String("g.took", "1s"),
		telemetry.String("g.inner.k", "v"),
	}

	if len(entry.Attrs) != len(want) {
		t.Fatalf("attrs = %v, want %v", entry.Attrs, want)
	}

	for index, attr := range want {
		if !entry.Attrs[index].Equal(attr) {
			t.Errorf("attr %d = %v, want %v", index, entry.Attrs[index], attr)
		}
	}
}

// TestLogsWithoutSpanHaveInvalidContext checks records logged outside a span.
func TestLogsWithoutSpanHaveInvalidContext(t *testing.T) {
	t.Parallel()

	driver, facade := start(t)
	slog.New(facade.Handler(slog.DiscardHandler)).InfoContext(t.Context(), "plain")

	if logs := driver.Logs(); len(logs) != 1 || logs[0].Context.IsValid() {
		t.Fatalf("logs = %+v", logs)
	}
}

// TestLogRingKeepsNewest checks the bounded ring and its ordering.
func TestLogRingKeepsNewest(t *testing.T) {
	t.Parallel()

	driver, facade := start(t, func(store *memorytelemetry.Builder) {
		store.MaxLogs(3)
	})

	logger := slog.New(facade.Handler(slog.DiscardHandler))

	for index := range 5 {
		logger.InfoContext(t.Context(), fmt.Sprintf("log-%d", index))
	}

	logs := driver.Logs()

	if len(logs) != 3 {
		t.Fatalf("kept %d logs, want 3", len(logs))
	}

	for index, want := range []string{"log-2", "log-3", "log-4"} {
		if logs[index].Message != want {
			t.Errorf("log %d = %s, want %s", index, logs[index].Message, want)
		}
	}
}

// TestLogsDisabledWhenMaxLogsZero checks that no records are kept and the
// handler reports the level disabled.
func TestLogsDisabledWhenMaxLogsZero(t *testing.T) {
	t.Parallel()

	driver, facade := start(t, func(store *memorytelemetry.Builder) {
		store.MaxLogs(0)
	})

	handler := facade.Handler(slog.DiscardHandler)

	if handler.Enabled(t.Context(), slog.LevelError) {
		t.Error("handler enabled with MaxLogs 0")
	}

	slog.New(handler).ErrorContext(t.Context(), "dropped")

	if len(driver.Logs()) != 0 {
		t.Fatal("kept logs with MaxLogs 0")
	}
}

// TestLogsSnapshotAndReset checks that snapshots are copies and Reset clears
// the ring.
func TestLogsSnapshotAndReset(t *testing.T) {
	t.Parallel()

	driver, facade := start(t)
	slog.New(facade.Handler(slog.DiscardHandler)).InfoContext(t.Context(), "kept", "k", "v")

	driver.Logs()[0].Attrs[0] = telemetry.String("k", "changed")

	if got := driver.Logs()[0].Attrs[0].AsString(); got != "v" {
		t.Fatalf("snapshot mutation leaked: %q", got)
	}

	driver.Reset()

	if len(driver.Logs()) != 0 {
		t.Fatal("logs survived Reset")
	}
}

// TestLogsDiscardedBeforeStart checks that an unstarted driver keeps nothing.
func TestLogsDiscardedBeforeStart(t *testing.T) {
	t.Parallel()

	driver := memorytelemetry.New()

	if err := driver.Export(t.Context(), slog.NewRecord(time.Time{}, slog.LevelInfo, "early", 0)); err != nil {
		t.Fatal(err)
	}

	if len(driver.Logs()) != 0 {
		t.Fatal("kept a log before Start")
	}
}

// exportN exports n records named by their index through the driver.
func exportN(t *testing.T, driver *memorytelemetry.Driver, n int) {
	t.Helper()

	for index := range n {
		if err := driver.Export(t.Context(), slog.NewRecord(time.Time{}, slog.LevelInfo, fmt.Sprintf("log-%d", index), 0)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLogSeqIncreasesAndSurvivesReset checks Seq starts at 1 and never goes
// backwards, even across Reset.
func TestLogSeqIncreasesAndSurvivesReset(t *testing.T) {
	t.Parallel()

	driver, _ := start(t)
	exportN(t, driver, 2)
	driver.Reset()
	exportN(t, driver, 1)

	logs := driver.Logs()

	if len(logs) != 1 || logs[0].Seq != 3 {
		t.Fatalf("logs after Reset = %+v, want one with Seq 3", logs)
	}
}

// TestLogsAfterReturnsOnlyNewer checks the cursor semantics, including a
// cursor at or beyond the newest record.
func TestLogsAfterReturnsOnlyNewer(t *testing.T) {
	t.Parallel()

	driver, _ := start(t)
	exportN(t, driver, 4)

	logs := driver.LogsAfter(2)

	if len(logs) != 2 || logs[0].Seq != 3 || logs[1].Seq != 4 {
		t.Fatalf("LogsAfter(2) = %+v", logs)
	}

	if got := driver.LogsAfter(4); got != nil {
		t.Fatalf("LogsAfter(4) = %+v, want nil", got)
	}

	if got := driver.LogsAfter(99); got != nil {
		t.Fatalf("LogsAfter(99) = %+v, want nil", got)
	}
}

// TestLogsAfterReportsEvictionAsGap checks that dropped records show as a gap
// between the cursor and the first returned Seq.
func TestLogsAfterReportsEvictionAsGap(t *testing.T) {
	t.Parallel()

	driver, _ := start(t, func(store *memorytelemetry.Builder) {
		store.MaxLogs(3)
	})
	exportN(t, driver, 10)

	logs := driver.LogsAfter(2)

	if len(logs) != 3 || logs[0].Seq != 8 || logs[2].Seq != 10 {
		t.Fatalf("LogsAfter(2) = %+v, want Seq 8..10", logs)
	}
}

// TestLogsChangedClosesOnExportAndReset checks that a channel closes once and
// the next call returns a fresh one.
func TestLogsChangedClosesOnExportAndReset(t *testing.T) {
	t.Parallel()

	driver, _ := start(t)

	first := driver.LogsChanged()

	if driver.LogsChanged() != first {
		t.Fatal("LogsChanged returned a new channel without a change")
	}

	select {
	case <-first:
		t.Fatal("channel closed before any change")
	default:
	}

	exportN(t, driver, 1)

	select {
	case <-first:
	default:
		t.Fatal("channel not closed by Export")
	}

	second := driver.LogsChanged()

	if second == first {
		t.Fatal("LogsChanged returned the closed channel")
	}

	driver.Reset()

	select {
	case <-second:
	default:
		t.Fatal("channel not closed by Reset")
	}
}

// TestLogsAfterDisabledRing checks that a ring with MaxLogs 0 returns nothing
// and never wakes on Export.
func TestLogsAfterDisabledRing(t *testing.T) {
	t.Parallel()

	driver, _ := start(t, func(store *memorytelemetry.Builder) {
		store.MaxLogs(0)
	})

	changed := driver.LogsChanged()
	exportN(t, driver, 3)

	if driver.LogsAfter(0) != nil {
		t.Fatal("disabled ring returned logs")
	}

	select {
	case <-changed:
		t.Fatal("disabled ring woke a follower")
	default:
	}
}

// TestLogsFollowerSeesNoDuplicatesWhenRingOverflows checks that a follower
// reading less often than the ring fills gets each record at most once and
// sees the drop as a gap.
func TestLogsFollowerSeesNoDuplicatesWhenRingOverflows(t *testing.T) {
	t.Parallel()

	driver, _ := start(t, func(store *memorytelemetry.Builder) {
		store.MaxLogs(4)
	})

	var last uint64
	seen := map[uint64]bool{}

	for _, burst := range []int{3, 9, 2} {
		exportN(t, driver, burst)

		for _, entry := range driver.LogsAfter(last) {
			if seen[entry.Seq] || entry.Seq <= last {
				t.Fatalf("duplicate or reordered Seq %d after %d", entry.Seq, last)
			}

			seen[entry.Seq], last = true, entry.Seq
		}
	}

	if last != 14 || len(seen) != 3+4+2 {
		t.Fatalf("last = %d, seen = %d, want 14 and 9", last, len(seen))
	}
}

// TestLogsConcurrentExportAndFollow exercises Export, LogsAfter, and
// LogsChanged together under the race detector and checks the follower sees
// strictly increasing Seq.
func TestLogsConcurrentExportAndFollow(t *testing.T) {
	t.Parallel()

	driver, _ := start(t, func(store *memorytelemetry.Builder) {
		store.MaxLogs(8)
	})

	const total = 500

	var writers sync.WaitGroup

	for range 4 {
		writers.Go(func() {
			exportN(t, driver, total/4)
		})
	}

	var last uint64

	for last < total {
		changed := driver.LogsChanged()

		for _, entry := range driver.LogsAfter(last) {
			if entry.Seq <= last {
				t.Errorf("Seq %d after %d", entry.Seq, last)
			}

			last = entry.Seq
		}

		if last < total {
			<-changed
		}
	}

	writers.Wait()
}

// TestLogSnapshotOperationsSurviveConcurrentReset covers both log read APIs
// and their copied attributes while exports, eviction, and Reset continue.
func TestLogSnapshotOperationsSurviveConcurrentReset(t *testing.T) {
	t.Parallel()

	driver, facade := start(t, func(store *memorytelemetry.Builder) {
		store.MaxLogs(2)
	})

	logger := slog.New(facade.Handler(slog.DiscardHandler))
	logger.InfoContext(t.Context(), "first", "key", "value")
	logger.WarnContext(t.Context(), "second", "key", "value")

	logs, after := driver.Logs(), driver.LogsAfter(1)
	after[0].Attrs[0] = telemetry.String("key", "edited")

	if driver.LogsAfter(1)[0].Attrs[0].AsString() != "value" {
		t.Fatal("LogsAfter exposed ring attributes")
	}

	var workers sync.WaitGroup
	workers.Go(func() {
		for range 30 {
			logger.InfoContext(t.Context(), "later")
			driver.Reset()
		}
	})

	for range 30 {
		warnings := logs.Filter(func(_ int, entry memorytelemetry.Log) bool {
			return entry.Level >= slog.LevelWarn
		})

		if warnings.Len() != 1 || warnings[0].Seq != 2 {
			t.Error("log snapshot changed while ring advanced")
		}
	}

	workers.Wait()

	retained := after.Filter(func(_ int, entry memorytelemetry.Log) bool {
		return entry.Seq > 1
	})

	if retained.Len() != 1 || retained[0].Message != "second" {
		t.Fatalf("LogsAfter snapshot = %+v", after)
	}
}

// logValueFunc supplies a deterministic caller-controlled LogValuer callback.
// It exposes public log resolution without inspecting a driver mutex or ring.
type logValueFunc func() slog.Value

// LogValue returns the callback's value. Tests use it to stop the driver while
// an admitted Export is still resolving its attributes.
func (value logValueFunc) LogValue() slog.Value {
	return value()
}

// TestExportDoesNotCommitAfterReentrantStop stops the driver from a LogValuer
// callback. Export must resolve user data outside its locks and recheck active
// state before committing, rather than adding a new record after Stop returns.
func TestExportDoesNotCommitAfterReentrantStop(t *testing.T) {
	t.Parallel()

	driver := memorytelemetry.New()

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})

	value := logValueFunc(func() slog.Value {
		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}

		return slog.StringValue("resolved")
	})

	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "operation", 0)
	record.AddAttrs(slog.Any("metadata", value))

	if err := driver.Export(t.Context(), record); err != nil {
		t.Fatal(err)
	}

	if got := driver.Logs(); len(got) != 0 {
		t.Fatalf("logs after reentrant Stop = %+v, want none", got)
	}
}
