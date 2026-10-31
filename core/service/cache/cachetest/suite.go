package cachetest

import (
	"context"
	"crypto/rand"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/cache"
)

// ttl is the lifetime used by expiry subtests. Whole seconds keep the suite
// valid for backends that report TTLs with one-second precision.
const ttl = 2 * time.Second

// realTimeMargin is added to every sleep when the clock is external, so the
// backend has certainly expired entries by the time the suite looks.
const realTimeMargin = 250 * time.Millisecond

// config holds the settings produced by [Option] values.
type config struct {
	// externalClock selects real-time sleeps instead of synctest bubbles.
	externalClock bool
}

// Option declares a capability or environment of the driver under test.
// Options never relax the contract's semantics.
//
// Example: options := []cachetest.Option{cachetest.WithExternalClock()}
type Option func(*config)

// WithExternalClock declares that the driver's expiry is driven by a clock
// the program cannot control, for example a Redis server. Time-dependent
// subtests then run in real time (a few seconds each, in parallel) rather
// than inside a testing/synctest bubble.
//
// Example: cachetest.Run(t, newDriver, cachetest.WithExternalClock())
func WithExternalClock() Option {
	return func(config *config) {
		config.externalClock = true
	}
}

// suite carries the state shared by the subtests of one [Run] call.
type suite struct {
	// newDriver creates the driver under test.
	newDriver func(t *testing.T) cache.Driver

	// config holds the declared options.
	config config
}

// Run executes the cache conformance suite against drivers produced by
// newDriver. It is called once per subtest, so each subtest gets an isolated
// driver where the backend allows it; subtests also use unique keys so a
// shared backend works. Run calls t.Run for every subtest and marks them
// parallel.
// Counter cases cover both int64 bounds, nonmutating overflow, MinInt64
// subtraction, and atomic contention without requiring a portable overflow
// error identity. Timed checks use the selected clock policy.
//
// Example:
//
//	cachetest.Run(t, func(*testing.T) cache.Driver {
//		return memorycache.New()
//	})
func Run(t *testing.T, newDriver func(t *testing.T) cache.Driver, opts ...Option) {
	t.Helper()

	var settings config

	for _, opt := range opts {
		opt(&settings)
	}

	s := suite{newDriver: newDriver, config: settings}

	for _, group := range []struct {
		// name labels the conformance group in test output.
		name string
		// fn runs the conformance group with its shared suite configuration.
		fn func(*testing.T, suite)
	}{
		{"Basics", basics},
		{"Expiry", expiry},
		{"Add", addCases},
		{"CompareAndPut", compareAndPutCases},
		{"CompareAndPutExpiry", compareAndPutExpiry},
		{"CompareAndPutFailures", compareAndPutFailures},
		{"CompareAndPutConcurrency", compareAndPutConcurrency},
		{"Counters", counters},
		{"CounterBounds", counterBounds},
		{"CounterOverflow", counterOverflow},
		{"CounterOverflowConcurrency", counterOverflowConcurrency},
		{"IncrementWithTTL", incrementWithTTL},
		{"Pull", pullCases},
		{"Locks", locks},
		{"LeaseFacade", leaseFacade},
		{"Cancellation", cancellation},
		{"Concurrency", concurrency},
	} {
		t.Run(group.name, func(t *testing.T) {
			group.fn(t, s)
		})
	}
}

// sub runs fn as a parallel subtest with a fresh driver and a unique key.
// Timed subtests run inside a synctest bubble unless the clock is external.
func (suite suite) sub(t *testing.T, name string, timed bool, fn func(t *testing.T, d cache.Driver, key string)) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		t.Parallel()

		body := func(t *testing.T) {
			d := suite.newDriver(t)
			key := "arkoren-conformance-" + rand.Text()
			t.Cleanup(func() {
				ctx := context.WithoutCancel(t.Context())

				// The lock facade stores its locks under "lock:" + name.
				for _, cleanup := range []string{key, "lock:" + key, key + ":destination"} {
					if err := d.Delete(ctx, cleanup); err != nil {
						t.Errorf("cleanup Delete(%q) = %v", cleanup, err)
					}
				}
			})

			fn(t, d, key)
		}

		if timed && !suite.config.externalClock {
			synctest.Test(t, body)

			return
		}

		body(t)
	})
}

// sleep waits for d on the driver's clock.
func (suite suite) sleep(d time.Duration) {
	if suite.config.externalClock {
		d += realTimeMargin
	}

	time.Sleep(d)
}
