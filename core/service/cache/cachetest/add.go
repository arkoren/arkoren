package cachetest

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/service/cache"
)

// addConcurrency requires [cache.Driver.Add] to be an atomic claim: when many
// callers add the same absent key, exactly one reports added and the stored
// value is the winner's. Consumers such as single-use links rely on this to
// accept a token once, so a driver must make the absence check and the write
// one backend operation, never a Has followed by a Put.
func addConcurrency(t *testing.T, s suite) {
	s.sub(t, "OneWinnerForAbsentKey", false, func(t *testing.T, d cache.Driver, key string) {
		// contenders exceeds typical core counts so racing check/write pairs overlap.
		const contenders = 32

		var winners atomic.Int32
		var winning atomic.Value
		var workers sync.WaitGroup
		start := make(chan struct{})

		for i := range contenders {
			workers.Go(func() {
				value := strconv.Itoa(i)

				<-start

				added, err := d.Add(t.Context(), key, []byte(value), 0)

				if err != nil {
					t.Errorf("Add: %v", err)

					return
				}

				if added {
					winners.Add(1)
					winning.Store(value)
				}
			})
		}

		close(start)
		workers.Wait()

		if got := winners.Load(); got != 1 {
			t.Fatalf("winners = %d, want one", got)
		}

		wantValue(t, d, key, winning.Load().(string))
	})
}
