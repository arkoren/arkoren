package telemetry_test

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/service/telemetry"
)

// TestInstrumentsConcurrentFirstUseBuildsOnce holds the public construction
// callback open while two callers resolve the same telemetry. Both must receive
// the same published value, with no duplicate callback execution.
func TestInstrumentsConcurrentFirstUseBuildsOnce(t *testing.T) {
	t.Parallel()

	var builds atomic.Int64
	var group sync.WaitGroup
	started := make(chan struct{})
	contending := make(chan struct{})
	release := make(chan struct{})
	releaseConstruction := sync.OnceFunc(func() {
		close(release)
	})
	t.Cleanup(func() {
		releaseConstruction()
		group.Wait()
	})

	instruments := telemetry.NewInstruments(func(*telemetry.Telemetry) *int64 {
		value := builds.Add(1)

		if value == 1 {
			close(started)
			<-release
		}

		return &value
	})

	service := telemetry.New(nil)
	results := make(chan *int64, 2)
	group.Go(func() {
		results <- instruments.For(service)
	})
	<-started
	group.Go(func() {
		close(contending)
		results <- instruments.For(service)
	})
	<-contending
	// constructionYields lets the contender enter the cache while the first
	// callback is held. Mutex waits intentionally are not durably blocked in
	// synctest, so this contention test uses ordinary goroutines instead.
	const constructionYields = 1024

	for range constructionYields {
		runtime.Gosched()
	}

	if got := builds.Load(); got != 1 {
		t.Fatalf("build calls while construction is held = %d, want 1", got)
	}

	select {
	case <-results:
		t.Fatal("caller returned before construction was released")
	default:
	}

	releaseConstruction()

	first, second := <-results, <-results

	if got := builds.Load(); got != 1 {
		t.Fatalf("build calls = %d, want 1", got)
	}

	if first != second {
		t.Fatal("concurrent For calls returned different values")
	}
}

// TestInstrumentsBuildOncePerTelemetry verifies the build cache.
func TestInstrumentsBuildOncePerTelemetry(t *testing.T) {
	t.Parallel()

	builds := 0
	instruments := telemetry.NewInstruments(func(*telemetry.Telemetry) *int {
		builds++

		return &builds
	})

	first, second := telemetry.New(nil), telemetry.New(nil)

	instruments.For(first)
	instruments.For(first)
	instruments.For(second)

	if builds != 2 {
		t.Fatalf("builds = %d, want 2", builds)
	}
}
