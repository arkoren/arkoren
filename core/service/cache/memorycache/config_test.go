package memorycache

import (
	"testing"
	"time"
)

// TestRetainedCachePolicyCannotStartSweeping verifies retained draft edits do
// not change the negative cleanup interval or start background work.
func TestRetainedCachePolicyCannotStartSweeping(t *testing.T) {
	t.Parallel()

	var retained *Builder
	driver := New(func(store *Builder) {
		retained = store
		store.Cleanup(-1)
	})
	retained.Cleanup(time.Second)

	if driver.store.Cleanup != -1 || driver.store.Cancel != nil {
		t.Fatal("retained policy changed cleanup or started a janitor")
	}
}

// TestZeroCleanupUsesDefault verifies that a zero Cleanup means one minute.
func TestZeroCleanupUsesDefault(t *testing.T) {
	t.Parallel()

	if got := New(func(store *Builder) {
		store.Cleanup(0)
	}).store.Cleanup; got != time.Minute {
		t.Fatalf("cleanup = %s, want 1m", got)
	}
}

// TestMemoryCachePoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestMemoryCachePoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	component := New(func(store *Builder) {
		calls++
		retained = store
	}, func(store *Builder) {
		if calls != 1 || retained != store {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	New(func(store *Builder) {
		if retained == store {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestMemoryCacheNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestMemoryCacheNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*Builder)
	empty := []func(*Builder){}

	if New(absent...) == nil || New(empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestMemoryCacheNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestMemoryCacheNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		New(nil, func(store *Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestMemoryCachePolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestMemoryCachePolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		New(func(store *Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestMemoryCacheCleanupReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMemoryCacheCleanupReturnsDraft(t *testing.T) {
	t.Parallel()

	cache := &Builder{}

	if cache.Cleanup(-1) != cache {
		t.Fatal("setting returned a different draft")
	}
}
