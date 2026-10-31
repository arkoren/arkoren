package search_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// secretText is query text and document content that must never reach
// telemetry.
const secretText = "alice@example.com"

// newTraced registers the test index and installs an isolated recorder before
// caller settings, so tests can override the initial configuration.
func newTraced(t *testing.T, driver search.Driver, opts ...func(*search.Builder)) (*search.Engine, *telemetrytest.Fake) {
	t.Helper()

	fake := telemetrytest.New()
	opts = append([]func(*search.Builder){func(engine *search.Builder) {
		engine.Indexes(testIndex)
		engine.Telemetry(fake.Telemetry(t))
	}}, opts...)

	return search.New(driver, opts...), fake
}

// TestEveryOperationIsOneClientSpan checks facade calls produce exactly one client span each.
func TestEveryOperationIsOneClientSpan(t *testing.T) {
	t.Parallel()

	engine, fake := newTraced(t, memorysearch.New(), func(engine *search.Builder) {
		engine.Name("catalog")
	})

	ctx := t.Context()

	_ = engine.Sync(ctx)
	_ = engine.Check(ctx)
	_ = engine.Put(ctx, testIndex, doc{ID: "a", Title: "x"})
	_, _ = engine.Search(ctx, search.NewQuery(testIndex, "x"), pagination.PageParams{})
	_ = engine.Delete(ctx, testIndex, "a")
	_ = engine.Drop(ctx, testIndex)

	for _, name := range []string{"sync", "check", "put", "search", "delete", "drop"} {
		fake.AssertSpan(t, "search "+name,
			telemetry.String("arkoren.search.operation", name),
			telemetry.String("arkoren.search.name", "catalog"))
	}

	fake.AssertSpanCount(t, 6)
	fake.AssertSpan(t, "search put", telemetry.String("arkoren.search.index", "docs"))
	fake.AssertCounter(t, "arkoren.search.operations", 1,
		telemetry.String("arkoren.search.operation", "search"), telemetry.String("arkoren.search.index", "docs"))
	fake.AssertHistogram(t, "arkoren.search.operation.duration", 1,
		telemetry.String("arkoren.search.operation", "sync"), telemetry.String("arkoren.search.name", "catalog"))
}

// TestSearchRecordsTheHitCount checks result counts appear on the search span.
func TestSearchRecordsTheHitCount(t *testing.T) {
	t.Parallel()

	engine, fake := newTraced(t, memorysearch.New())
	ctx := t.Context()

	_ = engine.Sync(ctx)
	_ = engine.Put(ctx, testIndex, doc{ID: "a", Title: "x"}, doc{ID: "b", Title: "x"}, doc{ID: "c", Title: "y"})
	_, _ = engine.Search(ctx, search.NewQuery(testIndex, "x"), pagination.PageParams{})

	fake.AssertSpan(t, "search search", telemetry.Int("arkoren.search.hits", 2))
	fake.AssertSpan(t, "search sync", telemetry.String("arkoren.search.name", search.DefaultName))
}

// TestQueryTextAndDocumentsNeverRecorded guards against disclosure of search payloads.
func TestQueryTextAndDocumentsNeverRecorded(t *testing.T) {
	t.Parallel()

	engine, fake := newTraced(t, memorysearch.New())
	ctx := t.Context()

	_ = engine.Sync(ctx)
	_ = engine.Put(ctx, testIndex, doc{ID: "a", Title: secretText})
	_, _ = engine.Search(ctx, search.NewQuery(testIndex, secretText, func(query *search.QueryBuilder) {
		query.Where("group", secretText)
	}), pagination.PageParams{})

	for _, span := range fake.Spans() {
		for _, attr := range span.Attrs {
			if text := attr.Text(); strings.Contains(text, "alice") {
				t.Errorf("attribute %s=%q holds query text or a document", attr.Key(), text)
			}
		}
	}

	for _, metric := range fake.Metrics() {
		for _, point := range metric.Points {
			for _, label := range point.Values {
				if strings.Contains(label, "alice") {
					t.Errorf("metric %s label %q holds query text", metric.Name, label)
				}
			}
		}
	}
}

// TestErrorsRecordTheSentinelName checks known failures retain stable telemetry labels.
func TestErrorsRecordTheSentinelName(t *testing.T) {
	t.Parallel()

	engine, fake := newTraced(t, memorysearch.New())

	if err := engine.Put(t.Context(), testIndex, doc{ID: "a"}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Put error = %v", err)
	}

	span, _ := fake.Span("search put")

	if span.Status != telemetry.Error || len(span.Events) != 0 {
		t.Fatalf("span status = %v events = %v, want an error without recorded events", span.Status, span.Events)
	}

	fake.AssertSpan(t, "search put", telemetry.String("error.type", "search.ErrNotFound"))
	fake.AssertCounter(t, "arkoren.search.operations", 1, telemetry.String("error.type", "search.ErrNotFound"))
}

// TestInvalidQueryIsTracedWithItsSentinel checks declaration errors are classified without payloads.
func TestInvalidQueryIsTracedWithItsSentinel(t *testing.T) {
	t.Parallel()

	engine, fake := newTraced(t, memorysearch.New())

	_, err := engine.Search(t.Context(), search.NewQuery(testIndex, "", func(query *search.QueryBuilder) {
		query.Where("missing", secretText)
	}), pagination.PageParams{})

	if !errors.Is(err, search.ErrInvalidQuery) {
		t.Fatalf("Search error = %v", err)
	}

	fake.AssertSpan(t, "search search", telemetry.String("error.type", "search.ErrInvalidQuery"))
}

// TestDefaultTelemetryResolvedLazily checks operations observe telemetry installed after construction.
func TestDefaultTelemetryResolvedLazily(t *testing.T) {
	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(testIndex)
	})

	fake := telemetrytest.New()

	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	_ = engine.Sync(context.Background())

	fake.AssertSpan(t, "search sync")
}
