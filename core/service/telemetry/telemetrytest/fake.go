package telemetrytest

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// Log is a log record exported to a [Fake].
// Fake.Logs returns detached outer containers for assertions; attributes retain
// slog's typed values rather than converting every value to text.
//
//	fake := telemetrytest.New()
//	logger := slog.New(fake.Telemetry(t).Handler(slog.DiscardHandler))
//	logger.InfoContext(t.Context(), "operation completed", "result", "hit")
//	for _, entry := range fake.Logs() {
//		fmt.Println(entry.Message, entry.Attrs["result"].String())
//	}
type Log struct {
	// Level is the severity of the record.
	// The fake accepts all levels while started, regardless of a local sink.
	//
	//	for _, entry := range fake.Logs() {
	//		if entry.Level >= slog.LevelWarn {
	//			fmt.Println(entry.Message)
	//		}
	//	}
	Level slog.Level

	// Message is the log message.
	// It is application-controlled text; tests should use nonsensitive fixtures.
	//
	//	for _, entry := range fake.Logs() {
	//		fmt.Println(entry.Message)
	//	}
	Message string

	// Attrs are the record attributes, including those added by WithAttrs and
	// WithGroup. Attributes inside groups are flattened with dotted keys,
	// such as "http.method".
	// The map returned by Logs is copied, but opaque Any objects inside values
	// are not recursively cloned.
	//
	//	for _, entry := range fake.Logs() {
	//		if value, ok := entry.Attrs["result"]; ok {
	//			fmt.Println(value.String())
	//		}
	//	}
	Attrs map[string]slog.Value

	// TraceID and SpanID identify the span in the context the record was
	// logged with, as hexadecimal text. Both are empty without a valid span.
	//
	//	for _, entry := range fake.Logs() {
	//		if entry.TraceID != "" && entry.SpanID != "" {
	//			fmt.Println("record is correlated to a span")
	//		}
	//	}
	TraceID, SpanID string
}

// Fake is a [telemetry.TraceDriver], [telemetry.MetricDriver], and
// [telemetry.LogDriver] that records everything in memorytelemetry. It embeds the
// [memorytelemetry.Driver], so Spans, Metrics, Reset, and Resource are available and
// the fake is a [Reader].
//
// Pass it to the application with application.Builder.Telemetry, which starts it, or call
// [Fake.Telemetry] for a ready-to-use [telemetry.Telemetry]; do not do both.
// Like every driver it records nothing until started. It is safe for
// concurrent use.
// Trace recorders inherit memorytelemetry's mandatory actual-parent metadata.
//
//	fake := telemetrytest.New()
//	service := fake.Telemetry(t)
//	_, span := service.Tracer("example.com/cache").Start(t.Context(), "cache get")
//	span.End()
//	fake.AssertSpanCount(t, 1)
type Fake struct {
	// Driver provides in-memory trace and metric recording and their snapshots.
	// Start the fake through Telemetry before emitting signals.
	//
	// Example:
	//
	//	fake := telemetrytest.New()
	//	service := fake.Telemetry(t)
	//	_, span := service.Tracer("example").Start(t.Context(), "work")
	//	span.End()
	//	fmt.Println(len(fake.Driver.Spans()))
	*memorytelemetry.Driver

	// mu guards every field below.
	mu sync.Mutex

	// active is true between Start and Stop.
	active bool

	// logs holds exported log records in order.
	logs []Log

	// facadeMu guards facade. It is separate from mu because starting the
	// facade calls back into the fake.
	facadeMu sync.Mutex

	// facade is the Telemetry created by [Fake.Telemetry].
	facade *telemetry.Telemetry
}

var (
	_ telemetry.TraceDriver  = (*Fake)(nil)
	_ telemetry.MetricDriver = (*Fake)(nil)
	_ telemetry.LogDriver    = (*Fake)(nil)
	_ Reader                 = (*Fake)(nil)
)

// New returns an empty fake. Callbacks configure the underlying memory driver
// directly once, synchronously in order on one fresh draft. Supplied nil
// callbacks and callback panics propagate. Omission and nil/empty expanded
// slices keep defaults. The driver copies settings before publication.
//
//	fake := telemetrytest.New(func(store *memorytelemetry.Builder) {
//		store.MaxSpans(100)
//	})
func New(configure ...memorytelemetry.BuilderFunc) *Fake {
	return &Fake{Driver: memorytelemetry.New(configure...)}
}

// Start starts the fake and its embedded memory driver with setup. Use it only
// for explicit lifecycle ownership; Fake.Telemetry starts its own facade and
// must not be mixed with a prior raw Start.
//
//	fake := telemetrytest.New()
//	if err := fake.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
//	if err := fake.Stop(ctx); err != nil {
//		return err
//	}
func (fake *Fake) Start(ctx context.Context, setup telemetry.Setup) error {
	if err := fake.Driver.Start(ctx, setup); err != nil {
		return err
	}

	fake.mu.Lock()

	fake.active = true
	fake.mu.Unlock()

	return nil
}

// Stop stops recording in both fake log capture and the embedded memory driver.
// Recorded data stays readable; repeated shutdown is safe and releases recording
// state even if ctx has expired. No external exporter resources are owned.
//
//	fake := telemetrytest.New()
//	_ = fake.Telemetry(t)
//	if err := fake.Stop(context.Background()); err != nil {
//		t.Fatal(err)
//	}
//	fmt.Println(len(fake.Logs()))
func (fake *Fake) Stop(ctx context.Context) error {
	fake.mu.Lock()

	fake.active = false
	fake.mu.Unlock()

	return fake.Driver.Stop(ctx)
}

// Telemetry returns a [telemetry.Telemetry] over the fake that is already
// started and is shut down when t ends. Repeated calls return the same value.
// It fails t if the fake was started already.
//
//	fake := telemetrytest.New()
//	service := fake.Telemetry(t)
//	service.Meter("example.com/cache").Counter("requests").Add(t.Context(), 1)
//	fake.AssertCounter(t, "requests", 1)
func (fake *Fake) Telemetry(t testing.TB) *telemetry.Telemetry {
	t.Helper()

	fake.facadeMu.Lock()
	defer fake.facadeMu.Unlock()

	if fake.facade != nil {
		return fake.facade
	}

	facade := telemetry.New(fake)

	if err := facade.Start(t.Context(), telemetry.Setup{Resource: telemetry.Resource{Service: "test"}}); err != nil {
		t.Fatalf("start fake telemetry: %v", err)
	}

	t.Cleanup(func() {
		_ = facade.Stop(context.WithoutCancel(t.Context()))
	})

	fake.facade = facade

	return facade
}

// Enabled reports true: the fake exports every level.
// It is a level-interest predicate even before Start; Export still drops data
// when inactive, and the facade applies its own lifecycle gate.
//
//	fake := telemetrytest.New()
//	fmt.Println(fake.Enabled(context.Background(), slog.LevelDebug)) // true
func (fake *Fake) Enabled(context.Context, slog.Level) bool {
	return true
}

// Export records record. It discards the record before Start and after
// Stop.
// Groups are flattened to dotted attribute keys. LogValuer resolution is
// serialized with fake log state, so those callbacks must not reenter this fake.
//
//	fake := telemetrytest.New()
//	_ = fake.Telemetry(t)
//	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "operation completed", 0)
//	record.AddAttrs(slog.String("result", "hit"))
//	if err := fake.Export(t.Context(), record); err != nil {
//		t.Fatal(err)
//	}
//	fake.AssertLog(t, slog.LevelInfo, "operation completed", slog.String("result", "hit"))
func (fake *Fake) Export(ctx context.Context, record slog.Record) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	if !fake.active {
		return nil
	}

	entry := Log{
		Level:   record.Level,
		Message: record.Message,
		Attrs:   map[string]slog.Value{},
	}
	record.Attrs(func(attr slog.Attr) bool {
		flatten(entry.Attrs, "", attr)

		return true
	})

	if spanContext := telemetry.SpanFromContext(ctx).Context(); spanContext.IsValid() {
		entry.TraceID, entry.SpanID = spanContext.TraceID.String(), spanContext.SpanID.String()
	}

	fake.logs = append(fake.logs, entry)

	return nil
}

// Logs returns exported records in order with a copied result slice and a
// copied attribute map per record. Assigning map entries does not affect the
// fake; opaque Any objects inside slog.Values retain their ordinary reference
// semantics rather than being recursively cloned.
//
//	for _, entry := range fake.Logs() {
//		fmt.Println(entry.Level, entry.Message)
//	}
func (fake *Fake) Logs() []Log {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	logs := slices.Clone(fake.logs)

	for index := range logs {
		logs[index].Attrs = maps.Clone(logs[index].Attrs)
	}

	return logs
}

// Reset discards recorded spans, metric values, and logs. The lifecycle is
// unchanged.
// Instrument registrations remain valid and can record again after Reset.
//
//	fake := telemetrytest.New()
//	service := fake.Telemetry(t)
//	requests := service.Meter("example.com/cache").Counter("requests")
//	requests.Add(t.Context(), 1)
//	fake.Reset()
//	requests.Add(t.Context(), 2)
//	fake.AssertCounter(t, "requests", 2)
func (fake *Fake) Reset() {
	fake.Driver.Reset()

	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.logs = nil
}

// flatten stores attr in values under its dotted key, descending into groups.
func flatten(values map[string]slog.Value, prefix string, attr slog.Attr) {
	value := attr.Value.Resolve()

	if value.Kind() != slog.KindGroup {
		values[prefix+attr.Key] = value

		return
	}

	if attr.Key != "" {
		prefix += attr.Key + "."
	}

	for _, member := range value.Group() {
		flatten(values, prefix, member)
	}
}
