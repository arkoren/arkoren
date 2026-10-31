package telemetry_test

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// decodeLines decodes JSON log lines.
func decodeLines(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()

	var lines []map[string]any

	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}

		var decoded map[string]any

		if err := json.Unmarshal(line, &decoded); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}

		lines = append(lines, decoded)
	}

	return lines
}

// TestHandlerAddsIDs checks correlation attributes with and without a span.
func TestHandlerAddsIDs(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	facade := fake.Telemetry(t)

	var output bytes.Buffer

	logger := slog.New(facade.Handler(slog.NewJSONHandler(&output, nil)))
	ctx, span := facade.Tracer("t").Start(t.Context(), "op")
	defer span.End()

	logger.InfoContext(ctx, "inside")
	logger.InfoContext(t.Context(), "outside")

	lines := decodeLines(t, &output)

	if lines[0]["trace_id"] != span.Context().TraceID.String() || lines[0]["span_id"] != span.Context().SpanID.String() {
		t.Errorf("inside = %v", lines[0])
	}

	if _, ok := lines[1]["trace_id"]; ok {
		t.Errorf("outside = %v", lines[1])
	}
}

// TestHandlerAddsRemoteIDs checks correlation with an adopted remote span.
func TestHandlerAddsRemoteIDs(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	remote := telemetry.SpanContext{
		TraceID: telemetry.TraceID{9},
		SpanID:  telemetry.SpanID{8},
		Remote:  true,
	}
	ctx := telemetry.ContextWithSpanContext(t.Context(), remote)

	slog.New(telemetry.New(nil).Handler(slog.NewJSONHandler(&output, nil))).InfoContext(ctx, "remote")

	if lines := decodeLines(t, &output); lines[0]["trace_id"] != remote.TraceID.String() {
		t.Fatalf("line = %v", lines[0])
	}
}

// TestHandlerDoesNotMutateRecord checks that the caller's record is intact.
func TestHandlerDoesNotMutateRecord(t *testing.T) {
	t.Parallel()

	facade := telemetrytest.New().Telemetry(t)
	ctx, span := facade.Tracer("t").Start(t.Context(), "op")
	defer span.End()

	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "m", 0)
	record.AddAttrs(slog.String("k", "v"))

	if err := facade.Handler(slog.NewJSONHandler(&bytes.Buffer{}, nil)).Handle(ctx, record); err != nil {
		t.Fatal(err)
	}

	if record.NumAttrs() != 1 {
		t.Fatalf("record has %d attributes after Handle, want 1", record.NumAttrs())
	}
}

// TestHandlerExportsSelfContainedRecords checks that WithAttrs and WithGroup
// are folded into exported records.
func TestHandlerExportsSelfContainedRecords(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	facade := fake.Telemetry(t)
	ctx, span := facade.Tracer("t").Start(t.Context(), "op")
	defer span.End()

	logger := slog.New(facade.Handler(slog.DiscardHandler)).
		With("a", 1).
		WithGroup("g").
		With("b", 2).
		WithGroup("").
		WithGroup("h")

	logger.ErrorContext(ctx, "failed", "c", 3)
	logger.ErrorContext(ctx, "empty group is dropped")
	slog.New(facade.Handler(slog.DiscardHandler)).WithGroup("unused").InfoContext(ctx, "no attrs")

	fake.AssertLog(t, slog.LevelError, "failed",
		slog.Int("a", 1), slog.Int("g.b", 2), slog.Int("g.h.c", 3))
	fake.AssertLog(t, slog.LevelInfo, "no attrs")

	logs := fake.Logs()

	if logs[0].TraceID != span.Context().TraceID.String() || logs[0].SpanID != span.Context().SpanID.String() {
		t.Errorf("exported log ids = %q %q", logs[0].TraceID, logs[0].SpanID)
	}

	if _, ok := logs[0].Attrs["trace_id"]; ok {
		t.Error("exported record carries trace_id attribute")
	}

	if len(logs[1].Attrs) != 2 {
		t.Errorf("empty group exported: %v", logs[1].Attrs)
	}
}

// TestHandlerEnabled checks the Enabled contract.
func TestHandlerEnabled(t *testing.T) {
	t.Parallel()

	next := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})

	plain := telemetry.New(nil).Handler(next)

	if plain.Enabled(t.Context(), slog.LevelInfo) || !plain.Enabled(t.Context(), slog.LevelWarn) {
		t.Error("handler without exporter must follow next")
	}

	exporting := telemetrytest.New().Telemetry(t).Handler(next)

	if !exporting.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("handler with an enabled exporter must be enabled for every level")
	}
}

// TestHandlerExportsOnlyWhenStarted checks that nothing is exported before
// Start and after Stop.
func TestHandlerExportsOnlyWhenStarted(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	facade := telemetry.New(fake)
	logger := slog.New(facade.Handler(slog.DiscardHandler))

	logger.ErrorContext(t.Context(), "before")

	_ = facade.Start(t.Context(), telemetry.Setup{})
	logger.ErrorContext(t.Context(), "during")

	_ = facade.Stop(t.Context())
	logger.ErrorContext(t.Context(), "after")

	if logs := fake.Logs(); len(logs) != 1 || logs[0].Message != "during" {
		t.Fatalf("logs = %+v, want only \"during\"", logs)
	}
}

// TestHandlerNilNext checks that a nil handler discards locally.
func TestHandlerNilNext(t *testing.T) {
	t.Parallel()

	handler := telemetry.New(nil).Handler(nil)

	if handler.Enabled(t.Context(), slog.LevelError) {
		t.Fatal("nil next must be disabled")
	}

	if handler.WithAttrs(nil) != handler || handler.WithGroup("") != handler {
		t.Fatal("empty WithAttrs and WithGroup must return the same handler")
	}
}

// rewritingHandler takes ownership of WithAttrs input as slog permits. It
// rewrites the local handler's copy to expose publication-order mistakes in
// the telemetry handler's independent export metadata.
type rewritingHandler struct {
	// Handler supplies ordinary disabled local logging behavior.
	slog.Handler
}

// WithAttrs rewrites its owned input and keeps the same local sink. Exported
// telemetry must already have captured a separate copy of the original attrs.
func (handler rewritingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	attrs[0] = slog.String("native", "rewritten")

	return handler
}

// TestHandlerCapturesAttrsBeforeTransferringOwnership observes exported logs
// when the local handler legitimately rewrites its owned WithAttrs slice.
// The independent exporter must retain the caller's original attributes.
func TestHandlerCapturesAttrsBeforeTransferringOwnership(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	handler := service.Handler(rewritingHandler{Handler: slog.DiscardHandler})
	logger := slog.New(handler.WithAttrs([]slog.Attr{slog.String("region", "eu")}))
	logger.InfoContext(t.Context(), "operation")

	logs := fake.Logs()

	if len(logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs))
	}

	if got := logs[0].Attrs["region"].String(); got != "eu" {
		t.Fatalf("exported region = %q, want eu; attrs = %v", got, logs[0].Attrs)
	}
}

// TestLogObserverIsNotNotifiedWhenExportStopsTelemetry stops the facade from
// error formatting during a real backend Export. Passive delivery captured
// earlier in Handle must still be suppressed when the exporter returns.
func TestLogObserverIsNotNotifiedWhenExportStopsTelemetry(t *testing.T) {
	t.Parallel()

	driver := memorytelemetry.New()
	service := startTelemetry(t, driver)
	observer := &countingObserver{}
	service.Observe(observer)

	logger := slog.New(service.Handler(slog.DiscardHandler))
	logger.InfoContext(t.Context(), "operation", slog.Any("error", callbackError{format: func() {
		if err := service.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}}))

	if observer.logs != 0 || len(driver.Logs()) != 0 {
		t.Fatalf("observed logs = %d, recorded logs = %d; want 0 each", observer.logs, len(driver.Logs()))
	}
}
