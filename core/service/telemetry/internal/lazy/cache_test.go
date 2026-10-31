package lazy_test

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"arkoren.dev/core/service/telemetry/internal/lazy"
)

// contentionYields keeps construction runnable while a second bubble enters
// the cache, without advancing either bubble's clock or using wall-clock sleeps.
const contentionYields = 1024

// TestCacheConcurrentBubblesShareConstruction exercises the shared instrument
// cache used by independent virtual-time tests. Coordination channels belong to
// the ordinary parent test, never to either bubble.
func TestCacheConcurrentBubblesShareConstruction(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	contending := make(chan struct{})
	var calls atomic.Int64
	cache := lazy.NewCache(func(owner int) *int {
		calls.Add(1)
		close(started)
		<-contending

		for range contentionYields {
			runtime.Gosched()
		}

		return &owner
	})

	var group sync.WaitGroup
	t.Cleanup(group.Wait)

	group.Go(func() {
		synctest.Test(t, func(t *testing.T) {
			if value := cache.Get(1); *value != 1 {
				t.Fatalf("cached value = %d, want 1", *value)
			}
		})
	})
	<-started
	synctest.Test(t, func(t *testing.T) {
		close(contending)

		if value := cache.Get(1); *value != 1 {
			t.Fatalf("cached value = %d, want 1", *value)
		}

		if count := calls.Load(); count != 1 {
			t.Fatalf("construction calls = %d, want 1", count)
		}
	})
}

// TestCachePanicLeavesPreviousValueAndAllowsRetry verifies that unwinding a
// callback neither publishes its owner nor leaves construction locked.
func TestCachePanicLeavesPreviousValueAndAllowsRetry(t *testing.T) {
	t.Parallel()

	calls := 0
	const failure = "construction failed"
	cache := lazy.NewCache(func(owner int) *int {
		calls++

		if calls == 2 {
			panic(failure)
		}

		return &owner
	})

	first := cache.Get(1)
	func() {
		defer func() {
			if value := recover(); value != failure {
				t.Fatalf("panic = %v, want %q", value, failure)
			}
		}()
		cache.Get(2)
	}()

	if value := cache.Get(1); value != first {
		t.Fatal("failed construction replaced the previous value")
	}

	if value := cache.Get(2); *value != 2 {
		t.Fatalf("retried value = %d, want 2", *value)
	}

	if calls != 3 {
		t.Fatalf("construction calls = %d, want 3", calls)
	}
}

// TestCacheOwnerTransitionsRebuildLatestOnly verifies that returning to an
// earlier owner rebuilds rather than retaining one entry per owner.
func TestCacheOwnerTransitionsRebuildLatestOnly(t *testing.T) {
	t.Parallel()

	calls := 0
	cache := lazy.NewCache(func(owner int) *int {
		calls++

		return &owner
	})

	first := cache.Get(1)

	if value := cache.Get(1); value != first {
		t.Fatal("cached hit returned a different value")
	}

	if value := cache.Get(2); *value != 2 {
		t.Fatalf("second owner value = %d, want 2", *value)
	}

	if value := cache.Get(1); value == first || *value != 1 {
		t.Fatalf("returning owner value = %p (%d), want a new value of 1", value, *value)
	}

	if calls != 3 {
		t.Fatalf("construction calls = %d, want 3", calls)
	}
}

// TestCacheHitDuringConstructionRemainsAvailable verifies that a slow miss
// does not block callers of the currently published owner.
func TestCacheHitDuringConstructionRemainsAvailable(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	releaseConstruction := sync.OnceFunc(func() {
		close(release)
	})

	var group sync.WaitGroup
	t.Cleanup(func() {
		releaseConstruction()
		group.Wait()
	})

	cache := lazy.NewCache(func(owner int) *int {
		if owner == 2 {
			close(started)
			<-release
		}

		return &owner
	})

	first := cache.Get(1)
	result := make(chan *int, 1)
	group.Go(func() {
		result <- cache.Get(2)
	})
	<-started

	if value := cache.Get(1); value != first {
		t.Fatal("cached hit changed during another owner's construction")
	}

	releaseConstruction()

	if value := <-result; *value != 2 {
		t.Fatalf("constructed value = %d, want 2", *value)
	}
}
