package memorytelemetry

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/internal/mcptest"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
)

// captured owns a real memory driver and facade; queries call its managed MCP
// tools directly so transport concerns cannot hide capture or filter bugs.
type captured struct {
	// store retains the records under test.
	store *Driver
	// signals emits correlated spans, logs, and metrics.
	signals *telemetry.Telemetry
	// tools contains the owner's immutable declarations.
	tools []mcp.Tool
}

// newCaptured starts local capture and validates every emitted declaration.
func newCaptured(t *testing.T, configure ...BuilderFunc) *captured {
	t.Helper()

	store := New(configure...)
	signals := telemetry.New(store)

	if err := signals.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	tools := store.MCPTools("memory")

	for _, tool := range tools {
		if err := tool.Err(); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		if err := signals.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return &captured{store: store, signals: signals, tools: tools}
}

// TestMCPTracesSummarizeErrorsAndDurations preserves root selection, span
// counts, error attributes, and human/machine pairs across typed tool calls.
func TestMCPTracesSummarizeErrorsAndDurations(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		capture := newCaptured(t)
		ctx, root := capture.signals.Tracer("test").Start(t.Context(), "GET /users/{id}", telemetry.Kind(telemetry.Server))
		_, child := capture.signals.Tracer("test").Start(ctx, "SELECT users")
		child.SetAttributes(telemetry.String("error.type", "timeout"))
		child.SetStatus(telemetry.Error, "query timed out")
		child.End()
		time.Sleep(182 * time.Millisecond)
		root.SetAttributes(telemetry.Int("http.response.status_code", 500), telemetry.String("http.route", "/users/{id}"))
		root.End()
		time.Sleep(12 * time.Second)

		result := mcptest.Call[diagnostic.List[traceSummary]](t, mcptest.Find(t, capture.tools, "memory.traces"), `{}`)

		if len(result.Items) != 1 || result.Truncated {
			t.Fatalf("result=%+v", result)
		}

		summary := result.Items[0]

		if summary.TraceID != root.Context().TraceID.String() || summary.Name != "GET /users/{id}" || summary.SpanCount != 2 {
			t.Fatalf("summary=%+v", summary)
		}

		if summary.ErrorCount != 1 || summary.Status != diagnostic.StatusError || summary.HTTPStatus != 500 || summary.ErrorType != "timeout" {
			t.Fatalf("summary=%+v", summary)
		}

		if summary.DurationMS != 182 || summary.Duration.Std() != 182*time.Millisecond || summary.Age != "12s ago" {
			t.Fatalf("summary=%+v", summary)
		}
	})
}

// TestMCPTracesFilterBeforeLimit checks outcome, root route, contained span
// name, and newest-first ordering before the result limit is applied.
func TestMCPTracesFilterBeforeLimit(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		capture := newCaptured(t)
		_, first := capture.signals.Tracer("test").Start(t.Context(), "first")
		first.End()
		time.Sleep(time.Second)

		ctx, root := capture.signals.Tracer("test").Start(t.Context(), "GET /fail", telemetry.Attrs(telemetry.String("http.route", "/fail")))
		_, child := capture.signals.Tracer("test").Start(ctx, "db query")
		child.SetStatus(telemetry.Error, "failed")
		child.End()
		time.Sleep(time.Second)
		root.End()

		tool := mcptest.Find(t, capture.tools, "memory.traces")
		result := mcptest.Call[diagnostic.List[traceSummary]](t, tool, `{"status":"error","route":"/fail","name":"db","min_duration":"500ms","since":"5s","limit":1}`)

		if len(result.Items) != 1 || result.Items[0].TraceID != root.Context().TraceID.String() {
			t.Fatalf("result=%+v", result)
		}

		latest := mcptest.Call[diagnostic.List[traceSummary]](t, tool, `{"limit":1}`)

		if !latest.Truncated || latest.Items[0].Name != "GET /fail" {
			t.Fatalf("latest=%+v", latest)
		}

		time.Sleep(time.Minute)

		old := mcptest.Call[diagnostic.List[traceSummary]](t, tool, `{"since":"1s"}`)

		if len(old.Items) != 0 {
			t.Fatalf("old=%+v", old)
		}
	})
}

// TestMCPTraceTreeJoinsLogsAndPreservesEmptyArrays covers nested spans,
// span events, failure status, attributes, and deterministic child ordering.
func TestMCPTraceTreeJoinsLogsAndPreservesEmptyArrays(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t)
	logger := slog.New(capture.signals.Handler(slog.DiscardHandler))
	ctx, root := capture.signals.Tracer("checkout").Start(t.Context(), "GET /pay", telemetry.Kind(telemetry.Server), telemetry.Attrs(telemetry.String("http.route", "/pay")))
	logger.InfoContext(ctx, "charging", "attempt", 1)

	childCtx, charge := capture.signals.Tracer("checkout").Start(ctx, "charge", telemetry.Kind(telemetry.Client))
	charge.AddEvent("retry", telemetry.Int("n", 1))
	charge.SetStatus(telemetry.Error, "declined")
	logger.WarnContext(childCtx, "retrying")

	_, deep := capture.signals.Tracer("checkout").Start(childCtx, "deep")
	deep.End()
	charge.End()

	_, audit := capture.signals.Tracer("checkout").Start(ctx, "audit")
	audit.End()
	root.End()

	result := mcptest.Call[traceView](t, mcptest.Find(t, capture.tools, "memory.trace"), `{"id":"`+root.Context().TraceID.String()+`"}`)

	if len(result.Roots) != 1 || result.OrphanLogs == nil || len(result.OrphanLogs) != 0 {
		t.Fatalf("trace=%+v", result)
	}

	node := result.Roots[0]

	if node.SpanID != root.Context().SpanID.String() || node.Scope != "checkout" || node.Kind != "server" {
		t.Fatalf("root=%+v", node)
	}

	if len(node.Logs) != 1 || node.Logs[0].Message != "charging" || node.Attrs["http.route"] != "/pay" {
		t.Fatalf("root=%+v", node)
	}

	if len(node.Children) != 2 || node.Children[0].Name != "charge" || node.Children[1].Name != "audit" {
		t.Fatalf("children=%+v", node.Children)
	}

	charged := node.Children[0]

	if charged.Status != diagnostic.StatusError || len(charged.Events) != 1 || len(charged.Children) != 1 || charged.Logs[0].Message != "retrying" {
		t.Fatalf("charge=%+v", charged)
	}

	if node.Children[1].Logs == nil || node.Children[1].Children == nil || node.Children[1].Events == nil {
		t.Fatal("empty arrays became null")
	}
}

// TestMCPTraceOrphanLogsRemainVisible preserves records from unfinished or
// evicted spans without attaching them to an unrelated recorded span.
func TestMCPTraceOrphanLogsRemainVisible(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t)
	ctx, root := capture.signals.Tracer("test").Start(t.Context(), "root")
	ctx, unfinished := capture.signals.Tracer("test").Start(ctx, "unfinished")
	slog.New(capture.signals.Handler(slog.DiscardHandler)).InfoContext(ctx, "orphan")
	root.End()

	result := mcptest.Call[traceView](t, mcptest.Find(t, capture.tools, "memory.trace"), `{"id":"`+root.Context().TraceID.String()+`"}`)

	if len(result.OrphanLogs) != 1 || result.OrphanLogs[0].SpanID != unfinished.Context().SpanID.String() {
		t.Fatalf("trace=%+v", result)
	}

	unfinished.End()
}

// TestMCPTraceDisconnectedRootsOrderByStart preserves remote parent identity
// and the summary's first-root selection when one trace enters twice.
func TestMCPTraceDisconnectedRootsOrderByStart(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		capture := newCaptured(t)
		id := "4bf92f3577b34da6a3ce929d0e0e4736"

		for _, parent := range []string{"00f067aa0ba902b7", "11f067aa0ba902b7"} {
			ctx := capture.signals.Extract(t.Context(), telemetry.MapCarrier{"traceparent": "00-" + id + "-" + parent + "-01"})
			_, span := capture.signals.Tracer("test").Start(ctx, "entry "+parent[:2])
			span.End()
			time.Sleep(time.Second)
		}

		result := mcptest.Call[traceView](t, mcptest.Find(t, capture.tools, "memory.trace"), `{"id":"`+id+`"}`)

		if len(result.Roots) != 2 || result.Roots[0].Name != "entry 00" || result.Roots[1].Name != "entry 11" || result.Roots[0].ParentID != "00f067aa0ba902b7" {
			t.Fatalf("trace=%+v", result)
		}
	})
}

// TestMCPTraceUniquePrefixesAndSafeFailures checks case-insensitive lookup,
// ambiguity without candidate disclosure, and not-found classification.
func TestMCPTraceUniquePrefixesAndSafeFailures(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t)

	for _, id := range []string{"4bf92f3577b34da6a3ce929d0e0e4736", "4bf92f35aaaaaaaaaaaaaaaaaaaaaaaa"} {
		ctx := capture.signals.Extract(t.Context(), telemetry.MapCarrier{"traceparent": "00-" + id + "-00f067aa0ba902b7-01"})
		_, span := capture.signals.Tracer("test").Start(ctx, "job")
		span.End()
	}

	tool := mcptest.Find(t, capture.tools, "memory.trace")
	result := mcptest.Call[traceView](t, tool, `{"id":"4BF92F357"}`)

	if result.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace=%+v", result)
	}

	_, err := tool.Call(t.Context(), []byte(`{"id":"4bf92f35"}`))
	failure, ok := mcp.ToolFailureInfo(err)

	if !ok || failure.Code != "ambiguous_trace" || strings.Contains(failure.Message, "4bf92f35") {
		t.Fatalf("failure=%+v err=%v", failure, err)
	}

	_, err = tool.Call(t.Context(), []byte(`{"id":"00000000"}`))

	if !errors.Is(err, ErrTraceNotFound) {
		t.Fatalf("err=%v", err)
	}
}

// TestMCPTraceRejectsMalformedIDs retains strict typed validation before lookup.
func TestMCPTraceRejectsMalformedIDs(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t)
	tool := mcptest.Find(t, capture.tools, "memory.trace")

	for _, input := range []string{`{}`, `{"id":"abc"}`, `{"id":"nothexnothex"}`, `{"id":"` + strings.Repeat("a", 33) + `"}`} {
		if _, err := tool.Call(t.Context(), []byte(input)); !errors.Is(err, mcp.ErrInvalidInput) {
			t.Fatalf("input=%s err=%v", input, err)
		}
	}
}

// TestMCPLogsFilterLevelTraceAgeAndLimit preserves historical filter behavior
// while returning a continuation cursor for every non-null result object.
func TestMCPLogsFilterLevelTraceAgeAndLimit(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		capture := newCaptured(t)
		logger := slog.New(capture.signals.Handler(slog.DiscardHandler))
		ctx, span := capture.signals.Tracer("test").Start(t.Context(), "work")
		logger.InfoContext(ctx, "old")
		time.Sleep(time.Minute)
		logger.WarnContext(ctx, "warn")
		logger.ErrorContext(ctx, "error")
		logger.ErrorContext(t.Context(), "outside")
		span.End()

		page := mcptest.Call[diagnostic.Page[logView]](t, mcptest.Find(t, capture.tools, "memory.logs"), `{"level":"warn","trace_id":"`+span.Context().TraceID.String()[:8]+`","since":"30s","limit":1}`)

		if len(page.Items) != 1 || page.Items[0].Message != "error" || !page.Truncated || page.Cursor == "" {
			t.Fatalf("page=%+v", page)
		}
	})
}

// TestMCPMetricsAggregatesAndPrefix preserves per-instrument aggregation and
// exact label ordering in object-shaped tool results.
func TestMCPMetricsAggregatesAndPrefix(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t)
	counter := capture.signals.Meter("test").Counter("shop.count")
	counter.Add(t.Context(), 2)
	counter.Add(t.Context(), 3)
	capture.signals.Meter("test").Counter("other.count").Add(t.Context(), 1)

	result := mcptest.Call[diagnostic.List[metricView]](t, mcptest.Find(t, capture.tools, "memory.metrics"), `{"name":"shop."}`)

	if len(result.Items) != 1 || result.Items[0].Name != "shop.count" || result.Items[0].Points[0].Sum != 5 {
		t.Fatalf("result=%+v", result)
	}
}

// TestMCPLogCursorTailLossAndPagination covers an empty initial ring, overflow,
// bounded pages, and no skipped matching record at the page-size boundary.
func TestMCPLogCursorTailLossAndPagination(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t, func(store *Builder) { store.MaxLogs(2) })

	tool := mcptest.Find(t, capture.tools, "memory.logs")
	anchor := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"tail"}`)

	if anchor.Items == nil || len(anchor.Items) != 0 || anchor.Cursor == "" {
		t.Fatalf("anchor=%+v", anchor)
	}

	for _, message := range []string{"first", "second", "third"} {
		if err := capture.store.Export(t.Context(), slog.NewRecord(time.Now(), slog.LevelInfo, message, 0)); err != nil {
			t.Fatal(err)
		}
	}

	page := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"`+anchor.Cursor+`","limit":1}`)

	if page.Dropped != 1 || !page.HasMore || page.Items[0].Message != "second" {
		t.Fatalf("page=%+v", page)
	}

	next := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"`+page.Cursor+`","limit":1}`)

	if next.HasMore || next.Items[0].Message != "third" {
		t.Fatalf("next=%+v", next)
	}
}

// TestMCPLogCursorAdvancesExcludedRecords prevents reporting false loss after
// already-scanned nonmatches are overwritten from the ring.
func TestMCPLogCursorAdvancesExcludedRecords(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t, func(store *Builder) { store.MaxLogs(1) })

	tool := mcptest.Find(t, capture.tools, "memory.logs")
	anchor := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"tail","level":"error"}`)

	if err := capture.store.Export(t.Context(), slog.NewRecord(time.Now(), slog.LevelInfo, "excluded", 0)); err != nil {
		t.Fatal(err)
	}

	empty := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"`+anchor.Cursor+`","level":"error"}`)

	if len(empty.Items) != 0 || empty.Cursor == anchor.Cursor {
		t.Fatalf("empty=%+v", empty)
	}

	if err := capture.store.Export(t.Context(), slog.NewRecord(time.Now(), slog.LevelError, "match", 0)); err != nil {
		t.Fatal(err)
	}

	page := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"`+empty.Cursor+`","level":"error"}`)

	if page.Dropped != 0 || len(page.Items) != 1 {
		t.Fatalf("page=%+v", page)
	}
}

// TestMCPLogCursorRejectsWrongFilterToolAndFuture keeps cursors bound to their
// owner query. Decimal uint64 positions never travel as JSON floating numbers.
func TestMCPLogCursorRejectsWrongFilterToolAndFuture(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t)
	tool := mcptest.Find(t, capture.tools, "memory.logs")
	anchor := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"tail"}`)

	_, err := tool.Call(t.Context(), []byte(`{"cursor":"`+anchor.Cursor+`","level":"error"}`))
	failure, ok := mcp.ToolFailureInfo(err)

	if !ok || failure.Code != "invalid_cursor" {
		t.Fatalf("err=%v", err)
	}

	bytes, err := base64.RawURLEncoding.DecodeString(anchor.Cursor)

	if err != nil {
		t.Fatal(err)
	}

	var token map[string]any

	if err := json.Unmarshal(bytes, &token); err != nil {
		t.Fatal(err)
	}

	token["s"] = "18446744073709551615"

	bytes, err = json.Marshal(token)

	if err != nil {
		t.Fatal(err)
	}

	future := base64.RawURLEncoding.EncodeToString(bytes)

	_, err = tool.Call(t.Context(), []byte(`{"cursor":"`+future+`"}`))
	failure, ok = mcp.ToolFailureInfo(err)

	if !ok || failure.Code != "invalid_cursor" {
		t.Fatalf("err=%v", err)
	}
}

// TestMCPLogCursorEmptyResetChangesInstance covers reset without any new log
// record. A continuation returns immediately with a fresh empty tail.
func TestMCPLogCursorEmptyResetChangesInstance(t *testing.T) {
	t.Parallel()

	capture := newCaptured(t)
	tool := mcptest.Find(t, capture.tools, "memory.logs")
	anchor := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"tail"}`)
	capture.store.Reset()

	page := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"`+anchor.Cursor+`","wait":"25s"}`)

	if !page.InstanceChanged || len(page.Items) != 0 || page.Cursor == anchor.Cursor {
		t.Fatalf("page=%+v", page)
	}
}

// TestMCPLogPollUsesFixedDeadline despite nonmatching arrivals; virtual time
// proves that notifications do not extend the requested idle-call budget.
func TestMCPLogPollUsesFixedDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		capture := newCaptured(t)
		tool := mcptest.Find(t, capture.tools, "memory.logs")
		anchor := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"tail","level":"error"}`)
		done := make(chan diagnostic.Page[logView], 1)
		start := time.Now()

		go func() {
			done <- mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"`+anchor.Cursor+`","level":"error","wait":"2s"}`)
		}()

		synctest.Wait()
		time.Sleep(time.Second)

		if err := capture.store.Export(t.Context(), slog.NewRecord(time.Now(), slog.LevelInfo, "nonmatch", 0)); err != nil {
			t.Fatal(err)
		}

		page := <-done

		if time.Since(start) != 2*time.Second || len(page.Items) != 0 || page.Cursor == anchor.Cursor {
			t.Fatalf("elapsed=%v page=%+v", time.Since(start), page)
		}
	})
}

// TestMCPLogPollCancellation tests idle waiting without real
// sleeps or a live streaming goroutine at the transport layer.
func TestMCPLogPollCancellation(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		capture := newCaptured(t)
		tool := mcptest.Find(t, capture.tools, "memory.logs")
		anchor := mcptest.Call[diagnostic.Page[logView]](t, tool, `{"cursor":"tail"}`)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() { _, err := tool.Call(ctx, []byte(`{"cursor":"`+anchor.Cursor+`","wait":"25s"}`)); done <- err }()

		synctest.Wait()
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
}
