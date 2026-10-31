package cache_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// secretKey is a key that must never reach telemetry.
const secretKey = "session:v1:alice@example.com"

// getFails is a driver whose Get fails with err.
type getFails struct {
	// Driver supplies the operations not overridden by the failing read.
	cache.Driver

	// err is the failure returned by the test driver.
	err error
}

// Get returns the configured read failure without exposing a value.
func (driver getFails) Get(context.Context, string) ([]byte, error) {
	return nil, driver.err
}

// newTraced creates a facade with an isolated telemetry recorder, then applies
// caller settings so each test can override the initial configuration.
func newTraced(t *testing.T, driver cache.Driver, opts ...func(*cache.Builder)) (*cache.Cache, *telemetrytest.Fake) {
	t.Helper()

	fake := telemetrytest.New()

	return cache.New(driver, append([]func(*cache.Builder){func(store *cache.Builder) {
		store.Telemetry(fake.Telemetry(t))
	}}, opts...)...), fake
}

// TestGetHitAndMiss verifies hits and misses record distinct telemetry outcomes.
func TestGetHitAndMiss(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorycache.New(), func(store *cache.Builder) {
		store.Name("sessions")
	})

	if _, err := store.Get(t.Context(), cache.NewKey[string](secretKey)); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}

	if err := store.Put(t.Context(), cache.NewKey[string](secretKey), "v"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(t.Context(), cache.NewKey[string](secretKey)); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpanCount(t, 3)
	fake.AssertSpan(t, "cache get",
		telemetry.String("arkoren.cache.operation", "get"),
		telemetry.String("arkoren.cache.store", "sessions"),
		telemetry.Bool("arkoren.cache.hit", true))
	fake.AssertSpan(t, "cache get", telemetry.Bool("arkoren.cache.hit", false))
	fake.AssertCounter(t, "arkoren.cache.operations", 1,
		telemetry.String("arkoren.cache.operation", "get"), telemetry.String("arkoren.cache.hit", "true"))
	fake.AssertCounter(t, "arkoren.cache.operations", 1,
		telemetry.String("arkoren.cache.operation", "get"), telemetry.String("arkoren.cache.hit", "false"))
	fake.AssertCounter(t, "arkoren.cache.operations", 1,
		telemetry.String("arkoren.cache.operation", "put"), telemetry.String("arkoren.cache.hit", ""))
	fake.AssertHistogram(t, "arkoren.cache.operation.duration", 2,
		telemetry.String("arkoren.cache.operation", "get"), telemetry.String("arkoren.cache.store", "sessions"))

	for _, span := range fake.Spans() {
		if span.Status == telemetry.Error {
			t.Errorf("span %q is an error", span.Name)
		}
	}
}

// TestMissIsNotAnError verifies missing cache entries do not mark spans as failed.
func TestMissIsNotAnError(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorycache.New())

	_, _ = store.Get(t.Context(), cache.NewKey[string]("absent"))

	span, _ := fake.Span("cache get")

	if _, found := span.Attr("error.type"); found || span.Status == telemetry.Error {
		t.Errorf("miss recorded as error: %+v", span)
	}

	fake.AssertSpan(t, "cache get", telemetry.String("arkoren.cache.store", cache.DefaultName))
}

// TestRememberIsOneSpan verifies each call records one span on both hit and miss.
func TestRememberIsOneSpan(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorycache.New())
	compute := func() (string, error) {
		return "value", nil
	}

	for range 2 {
		if _, err := store.Remember(t.Context(), cache.NewKey[string](secretKey), compute); err != nil {
			t.Fatal(err)
		}
	}

	fake.AssertSpanCount(t, 2)
	fake.AssertSpan(t, "cache remember", telemetry.Bool("arkoren.cache.hit", false))
	fake.AssertSpan(t, "cache remember", telemetry.Bool("arkoren.cache.hit", true))

}

// TestEveryOperationIsOneSpan verifies facade calls do not duplicate driver spans.
func TestEveryOperationIsOneSpan(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorycache.New())
	ctx := t.Context()

	_ = store.Put(ctx, cache.NewKey[int]("a"), 1)
	_, _ = store.Has(ctx, cache.NewKey[int]("a"))
	_, _ = store.Add(ctx, cache.NewKey[int]("b"), 1)
	_, _ = store.Increment(ctx, cache.NewKey[int64]("n"), 1)
	_, _ = store.Decrement(ctx, cache.NewKey[int64]("n"), 1)
	_, _ = store.TTL(ctx, cache.NewKey[int]("a"))
	_, _ = store.Pull(ctx, cache.NewKey[int]("a"))
	_ = store.Delete(ctx, cache.NewKey[int]("b"))

	for _, name := range []string{"put", "has", "add", "increment", "decrement", "ttl", "pull", "delete"} {
		fake.AssertSpan(t, "cache "+name)
	}

	fake.AssertSpanCount(t, 8)
	fake.AssertSpan(t, "cache has", telemetry.Bool("arkoren.cache.hit", true))
}

// TestKeysAndValuesNeverRecorded guards against payload disclosure in telemetry.
func TestKeysAndValuesNeverRecorded(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorycache.New())

	_ = store.Put(t.Context(), cache.NewKey[string](secretKey), "secret-value")
	_, _ = store.Get(t.Context(), cache.NewKey[string](secretKey))
	_, _ = store.Remember(t.Context(), cache.NewKey[string](secretKey), func() (string, error) {
		return "secret-value", nil
	})

	for _, span := range fake.Spans() {
		if strings.Contains(span.Name, "alice") {
			t.Errorf("span name %q holds the key", span.Name)
		}

		for _, attr := range span.Attrs {
			text := attr.Text()

			if strings.Contains(text, "alice") || strings.Contains(text, "secret-value") {
				t.Errorf("attribute %s=%q holds a key or value", attr.Key(), text)
			}
		}
	}

	for _, metric := range fake.Metrics() {
		for _, point := range metric.Points {
			for _, label := range point.Values {
				if strings.Contains(label, "alice") {
					t.Errorf("metric %s label %q holds the key", metric.Name, label)
				}
			}
		}
	}
}

// TestErrorsRecordTypeOnly verifies driver error text is excluded from spans.
func TestErrorsRecordTypeOnly(t *testing.T) {
	t.Parallel()

	secret := errors.New("connection to " + secretKey + " refused")
	store, fake := newTraced(t, getFails{err: secret})

	if _, err := store.Get(t.Context(), cache.NewKey[string](secretKey)); err == nil {
		t.Fatal("expected error")
	}

	span, _ := fake.Span("cache get")

	if span.Status != telemetry.Error {
		t.Fatalf("status = %v, want error", span.Status)
	}

	if len(span.Events) != 0 || strings.Contains(span.StatusDescription, "alice") {
		t.Errorf("error text leaked: events=%v description=%q", span.Events, span.StatusDescription)
	}

	fake.AssertSpan(t, "cache get", telemetry.String("error.type", "*errors.errorString"))

	if _, found := span.Attr("arkoren.cache.hit"); found {
		t.Error("failed read reports a hit state")
	}

	fake.AssertCounter(t, "arkoren.cache.operations", 1, telemetry.String("error.type", "*errors.errorString"))
	fake.AssertHistogram(t, "arkoren.cache.operation.duration", 1, telemetry.String("error.type", "*errors.errorString"))
}

// TestSentinelErrorTypes verifies known contract errors retain stable telemetry labels.
func TestSentinelErrorTypes(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorycache.New())

	key := cache.NewKey[int]("k", func(entry *cache.KeyBuilder) {
		entry.TTL(-time.Second)
	})

	if err := store.Put(t.Context(), key, 1); !errors.Is(err, cache.ErrInvalidTTL) {
		t.Fatalf("err = %v", err)
	}

	fake.AssertSpan(t, "cache put", telemetry.String("error.type", "cache.ErrInvalidTTL"))
}

// TestDefaultResolvedLazily verifies operations observe telemetry installed after construction.
func TestDefaultResolvedLazily(t *testing.T) {
	store := cache.New(memorycache.New())
	fake := telemetrytest.New()

	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	_, _ = store.Get(t.Context(), cache.NewKey[string]("k"))

	fake.AssertSpan(t, "cache get")
}

// TestDisabledTelemetryAllocatesNothing compares facade allocations with the untraced baseline.
func TestDisabledTelemetryAllocatesNothing(t *testing.T) {
	driver := memorycache.New()
	ctx := context.Background()
	plain := cache.New(driver, func(store *cache.Builder) {
		store.Telemetry(telemetry.New(nil))
	})

	key := cache.NewKey[string]("k")

	traced := testing.AllocsPerRun(100, func() {
		_, _ = plain.Get(ctx, key)
	})

	// The uninstrumented Get: the driver call plus the decode target.
	baseline := testing.AllocsPerRun(100, func() {
		var value string

		raw, err := driver.Get(ctx, "k")

		if err == nil {
			_ = json.Unmarshal(raw, &value)
		}
	})

	if traced != baseline {
		t.Errorf("disabled telemetry changed allocations per Get from %v to %v", baseline, traced)
	}
}

// BenchmarkCacheGetNoTelemetry measures typed reads with telemetry disabled.
func BenchmarkCacheGetNoTelemetry(b *testing.B) {
	store := cache.New(memorycache.New(), func(store *cache.Builder) {
		store.Telemetry(telemetry.New(nil))
	})

	ctx := context.Background()
	key := cache.NewKey[string]("k")

	_ = store.Put(ctx, key, "v")
	b.ReportAllocs()

	for b.Loop() {
		_, _ = store.Get(ctx, key)
	}
}

// BenchmarkCacheGetDriverBaseline measures direct reads for comparison with facade overhead.
func BenchmarkCacheGetDriverBaseline(b *testing.B) {
	driver := memorycache.New()
	ctx := context.Background()

	_ = driver.Put(ctx, "k", []byte(`"v"`), 0)
	b.ReportAllocs()

	for b.Loop() {
		var value string

		raw, _ := driver.Get(ctx, "k")
		_ = json.Unmarshal(raw, &value)
	}
}
