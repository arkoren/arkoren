package cache_test

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestRetainedCachePolicyCannotReconfigurePublishedFacade verifies the copied
// settings root while preserving the shared telemetry service identity.
func TestRetainedCachePolicyCannotReconfigurePublishedFacade(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	var retained *cache.Builder
	store := cache.New(memorycache.New(), func(store *cache.Builder) {
		retained = store
		store.Telemetry(fake.Telemetry(t))
		store.Name("sessions")
	})
	retained.Telemetry(telemetry.New(nil))
	retained.Name("mutated")

	if _, err := store.Get(t.Context(), cache.NewKey[string]("absent")); !errors.Is(err, cache.ErrNotFound) {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "cache get", telemetry.String("arkoren.cache.store", "sessions"))
}

// TestCachePoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestCachePoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *cache.Builder
	calls := 0
	component := cache.New(nil, func(store *cache.Builder) {
		calls++
		retained = store
	}, func(store *cache.Builder) {
		if calls != 1 || retained != store {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	cache.New(nil, func(store *cache.Builder) {
		if retained == store {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestCacheNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestCacheNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*cache.Builder)
	empty := []func(*cache.Builder){}

	if cache.New(nil, absent...) == nil || cache.New(nil, empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestCacheNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestCacheNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		cache.New(nil, nil, func(store *cache.Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestCachePolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestCachePolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		cache.New(nil, func(store *cache.Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestCacheTelemetryReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestCacheTelemetryReturnsDraft(t *testing.T) {
	t.Parallel()

	store := &cache.Builder{}

	if store.Telemetry(nil) != store {
		t.Fatal("setting returned a different draft")
	}
}

// TestCacheNameReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestCacheNameReturnsDraft(t *testing.T) {
	t.Parallel()

	store := &cache.Builder{}

	if store.Name("sessions") != store {
		t.Fatal("setting returned a different draft")
	}
}
