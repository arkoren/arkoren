package storage_test

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/memorystorage"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestNilBuilderTelemetryResolvesDefaultPerOperation verifies a later nil policy
// restores dynamic default lookup instead of retaining the construction default.
func TestNilBuilderTelemetryResolvesDefaultPerOperation(t *testing.T) {
	store := storage.New(memorystorage.New(), func(store *storage.Builder) {
		store.Telemetry(telemetry.New(nil))
		store.Telemetry(nil)
	})

	fake := telemetrytest.New()
	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	if _, err := store.Stat(t.Context(), "absent"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "storage stat")
}

// TestRetainedStoragePolicyCannotReconfigurePublishedFacade verifies independent
// name and telemetry settings while the memory driver remains a shared service.
func TestRetainedStoragePolicyCannotReconfigurePublishedFacade(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	var retained *storage.Builder
	store := storage.New(memorystorage.New(), func(store *storage.Builder) {
		retained = store
		store.Telemetry(fake.Telemetry(t))
		store.Name("uploads")
	})
	retained.Telemetry(telemetry.New(nil))
	retained.Name("mutated")

	if _, err := store.Stat(t.Context(), "absent"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "storage stat", telemetry.String("arkoren.storage.store", "uploads"))
}

// secretKey is a key that must never reach telemetry.
const secretKey = "users/alice@example.com/passport.pdf"

// newTraced installs an isolated recorder before applying caller overrides.
// The returned fake exposes only the telemetry produced by this facade.
func newTraced(t *testing.T, driver storage.Driver, opts ...func(*storage.Builder)) (*storage.Storage, *telemetrytest.Fake) {
	t.Helper()

	fake := telemetrytest.New()

	return storage.New(driver, append([]func(*storage.Builder){func(store *storage.Builder) {
		store.Telemetry(fake.Telemetry(t))
	}}, opts...)...), fake
}

// TestOperationsEmitSpansAndMetrics checks every facade operation records client telemetry.
func TestOperationsEmitSpansAndMetrics(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorystorage.New(), func(store *storage.Builder) {
		store.Name("uploads")
	})

	if _, err := store.PutBytes(t.Context(), secretKey, []byte("hello")); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Stat(t.Context(), secretKey); err != nil {
		t.Fatal(err)
	}

	object, err := store.Open(t.Context(), secretKey)

	if err != nil {
		t.Fatal(err)
	}

	if err := object.Contents.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.List(t.Context(), storage.ListOptions{Prefix: "users/"}); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(t.Context(), secretKey); err != nil {
		t.Fatal(err)
	}

	for _, operation := range []string{"put", "stat", "open", "list", "delete"} {
		fake.AssertSpan(t, "storage "+operation,
			telemetry.String("arkoren.storage.operation", operation),
			telemetry.String("arkoren.storage.store", "uploads"))
		fake.AssertCounter(t, "arkoren.storage.operations", 1, telemetry.String("arkoren.storage.operation", operation))
	}

	fake.AssertSpan(t, "storage put", telemetry.Int64("arkoren.storage.object.size", 5))
	fake.AssertHistogram(t, "arkoren.storage.operation.duration", 5, telemetry.String("arkoren.storage.store", "uploads"))

	for _, span := range fake.Spans() {
		if span.Status == telemetry.Error {
			t.Errorf("span %q is an error", span.Name)
		}
	}
}

// TestDefaultStoreName checks omitted store configuration uses a stable telemetry label.
func TestDefaultStoreName(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorystorage.New())

	if err := store.Delete(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "storage delete", telemetry.String("arkoren.storage.store", storage.DefaultName))
}

// TestMissingObjectIsNotAnError checks absent objects do not mark spans as failed.
func TestMissingObjectIsNotAnError(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorystorage.New())

	if _, err := store.Stat(t.Context(), secretKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}

	span, found := fake.Span("storage stat")

	if !found || span.Status == telemetry.Error {
		t.Fatalf("span = %+v (found %v), want a span that is not an error", span, found)
	}

	fake.AssertCounter(t, "arkoren.storage.operations", 1, telemetry.String("error.type", ""))
}

// TestErrorsAreClassifiedWithoutKeys checks error labels do not disclose object identities.
func TestErrorsAreClassifiedWithoutKeys(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorystorage.New())

	if _, err := store.Stat(t.Context(), "/"+secretKey); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("err = %v, want invalid key", err)
	}

	if _, err := store.SignURL(t.Context(), secretKey, storage.SignOptions{TTL: time.Minute}); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("err = %v, want unsupported", err)
	}

	fake.AssertSpan(t, "storage stat", telemetry.String("error.type", "storage.ErrInvalidKey"))
	fake.AssertSpan(t, "storage sign", telemetry.String("error.type", "storage.ErrUnsupported"))
	fake.AssertCounter(t, "arkoren.storage.operations", 1, telemetry.String("error.type", "storage.ErrUnsupported"))

	for _, span := range fake.Spans() {
		if span.Status != telemetry.Error || len(span.Events) != 0 {
			t.Errorf("span %q status = %v, events = %d, want error and no events", span.Name, span.Status, len(span.Events))
		}
	}
}

// TestTelemetryNeverRecordsKeys guards against recording private object paths.
func TestTelemetryNeverRecordsKeys(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorystorage.New())

	_, _ = store.PutBytes(t.Context(), secretKey, []byte("x"))
	_, _ = store.Stat(t.Context(), secretKey+"/missing")
	_, _ = store.List(t.Context(), storage.ListOptions{Prefix: "users/alice", Cursor: "alice-cursor"})

	for _, span := range fake.Spans() {
		for _, attr := range span.Attrs {
			if strings.Contains(attr.Text(), "alice") || strings.Contains(span.Name, "alice") {
				t.Errorf("span %q attribute %q carries a key", span.Name, attr.Key())
			}
		}
	}

	for _, metric := range fake.Metrics() {
		for _, point := range metric.Points {
			for _, value := range point.Values {
				if strings.Contains(value, "alice") {
					t.Errorf("metric %q carries a key", metric.Name)
				}
			}
		}
	}
}

// fixedDriver answers every call without allocating.
type fixedDriver struct {
	// Driver reserves the contract; measured operations use allocation-free overrides.
	storage.Driver
}

// Stat returns empty metadata without I/O for allocation measurements.
func (fixedDriver) Stat(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, nil
}

// Open returns a disposable body without I/O for allocation measurements.
func (fixedDriver) Open(context.Context, string) (storage.Object, error) {
	return storage.Object{Contents: io.NopCloser(nil)}, nil
}

// SignURL returns no URL or error without I/O for allocation measurements.
func (fixedDriver) SignURL(context.Context, string, storage.SignOptions) (*url.URL, error) {
	return nil, nil
}

// TestDisabledTelemetryAllocatesNothing checks an untraced metadata lookup adds no allocations.
func TestDisabledTelemetryAllocatesNothing(t *testing.T) {
	store := storage.New(fixedDriver{}, func(store *storage.Builder) {
		store.Telemetry(telemetry.New(nil))
	})

	ctx := context.Background()

	allocations := testing.AllocsPerRun(100, func() {
		_, _ = store.Stat(ctx, "reports/a.pdf")
	})

	if allocations != 0 {
		t.Errorf("disabled telemetry allocated %v times per Stat", allocations)
	}
}

// TestStoragePoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestStoragePoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *storage.Builder
	calls := 0
	component := storage.New(nil, func(store *storage.Builder) {
		calls++
		retained = store
	}, func(store *storage.Builder) {
		if calls != 1 || retained != store {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	storage.New(nil, func(store *storage.Builder) {
		if retained == store {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestStorageNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestStorageNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*storage.Builder)
	empty := []func(*storage.Builder){}

	if storage.New(nil, absent...) == nil || storage.New(nil, empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestStorageNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestStorageNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		storage.New(nil, nil, func(store *storage.Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestStoragePolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestStoragePolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		storage.New(nil, func(store *storage.Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestStorageTelemetryReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestStorageTelemetryReturnsDraft(t *testing.T) {
	t.Parallel()

	store := &storage.Builder{}

	if store.Telemetry(nil) != store {
		t.Fatal("setting returned a different draft")
	}
}

// TestStorageNameReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestStorageNameReturnsDraft(t *testing.T) {
	t.Parallel()

	store := &storage.Builder{}

	if store.Name("uploads") != store {
		t.Fatal("setting returned a different draft")
	}
}

// TestStorageEventsReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestStorageEventsReturnsDraft(t *testing.T) {
	t.Parallel()

	store := &storage.Builder{}

	if store.Events(nil) != store {
		t.Fatal("setting returned a different draft")
	}
}
