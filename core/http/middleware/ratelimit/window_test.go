package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// testWindow is the window length used by the clock tests.
const testWindow = time.Minute

// epoch is a moment aligned to the start of a window.
var epoch = time.Unix(1_800_000_000, 0)

// limiterAt is a limiter whose clock the test moves.
type limiterAt struct {
	// t owns request assertions and cleanup for this controlled-clock limiter.
	t *testing.T
	// store holds counters shared by requests made through this limiter.
	store *cache.Cache
	// handler applies the completed policy using the test-controlled clock.
	handler router.Handler
	// now is the current instant observed by the limiter; tests advance it
	// explicitly.
	now time.Time
}

// newLimiterAt returns a limiter of limit requests per testWindow on driver,
// starting at start.
func newLimiterAt(t *testing.T, driver cache.Driver, limit int, start time.Time) *limiterAt {
	t.Helper()

	limiter := &limiterAt{
		t:     t,
		store: cache.New(driver),
		now:   start,
	}
	config := newSettings(
		func(limiter *Builder) {
			limiter.Limit(limit)
		},
		func(limiter *Builder) {
			limiter.Window(testWindow)
		},
		func(limiter *Builder) {
			limiter.Namespace(cache.NewNamespace[int64]("rl", func(space *cache.NamespaceBuilder) {
				space.Separator(":")
			}))
		},
		func(limiter *Builder) {
			limiter.KeyFunc(func(*http.Request) string {
				return "c"
			})
		},
	)

	limiter.handler = middleware(limiter.store, config, func() time.Time {
		return limiter.now
	})(
		func(http.ResponseWriter, *http.Request) error {
			return nil
		},
	)

	return limiter
}

// hit sends one request at the current time.
func (limiter *limiterAt) hit() *http.Response {
	limiter.t.Helper()

	return router.Handler(limiter.handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))
}

// burst sends n requests and returns how many were admitted.
func (limiter *limiterAt) burst(n int) int {
	limiter.t.Helper()

	allowed := 0

	for range n {
		if limiter.hit().StatusCode != http.StatusTooManyRequests {
			allowed++
		}
	}

	return allowed
}

// checkSlidingWindowHasNoBoundaryBurst verifies requests just before a window
// boundary still count just after it, where a fixed window would admit the
// full limit again.
func checkSlidingWindowHasNoBoundaryBurst(t *testing.T, driver cache.Driver) {
	t.Helper()

	limiter := newLimiterAt(t, driver, 10, epoch.Add(50*time.Second))

	if got := limiter.burst(10); got != 10 {
		t.Fatalf("first burst admitted %d, want 10", got)
	}

	limiter.now = epoch.Add(testWindow + 5*time.Second)

	if got := limiter.burst(10); got != 0 {
		t.Fatalf("burst 15s later admitted %d, want 0", got)
	}
}

// TestSlidingWindowHasNoBoundaryBurst runs checkSlidingWindowHasNoBoundaryBurst on the memory driver.
func TestSlidingWindowHasNoBoundaryBurst(t *testing.T) {
	t.Parallel()

	checkSlidingWindowHasNoBoundaryBurst(t, memorycache.New())
}

// checkSlidingWindowDecaysPreviousWindow verifies the previous window's weight
// falls linearly and is gone after two windows.
func checkSlidingWindowDecaysPreviousWindow(t *testing.T, driver cache.Driver) {
	t.Helper()

	limiter := newLimiterAt(t, driver, 10, epoch.Add(50*time.Second))
	limiter.burst(10)

	limiter.now = epoch.Add(testWindow + 30*time.Second)

	if got := limiter.burst(10); got != 5 {
		t.Fatalf("half-decayed burst admitted %d, want 5", got)
	}

	limiter.now = epoch.Add(3 * testWindow)

	if got := limiter.burst(10); got != 10 {
		t.Fatalf("burst after two windows admitted %d, want 10", got)
	}
}

// TestSlidingWindowDecaysPreviousWindow runs checkSlidingWindowDecaysPreviousWindow on the memory driver.
func TestSlidingWindowDecaysPreviousWindow(t *testing.T) {
	t.Parallel()

	checkSlidingWindowDecaysPreviousWindow(t, memorycache.New())
}

// checkSlidingWindowDoesNotCountDeniedRequests verifies denied requests are
// refunded, so hammering does not extend the block.
func checkSlidingWindowDoesNotCountDeniedRequests(t *testing.T, driver cache.Driver) {
	t.Helper()

	limiter := newLimiterAt(t, driver, 2, epoch.Add(10*time.Second))

	if got := limiter.burst(50); got != 2 {
		t.Fatalf("burst admitted %d, want 2", got)
	}

	key := "rl:c:" + strconv.FormatInt(epoch.UnixNano()/testWindow.Nanoseconds(), 10)

	count, err := limiter.store.Get(context.Background(), cache.NewKey[int64](key))

	if err != nil || count != 2 {
		t.Fatalf("counter = %d, %v, want 2", count, err)
	}

	limiter.now = epoch.Add(2 * testWindow)

	if got := limiter.burst(2); got != 2 {
		t.Fatalf("burst two windows later admitted %d, want 2", got)
	}
}

// TestSlidingWindowDoesNotCountDeniedRequests runs checkSlidingWindowDoesNotCountDeniedRequests on the memory driver.
func TestSlidingWindowDoesNotCountDeniedRequests(t *testing.T) {
	t.Parallel()

	checkSlidingWindowDoesNotCountDeniedRequests(t, memorycache.New())
}

// checkSlidingWindowHeaders verifies Remaining, Reset, and Retry-After, and
// that a client honoring Retry-After is admitted while one retrying a second
// earlier is not. With 4s elapsed in a 60s window, 56s remain: Reset is 56s plus
// a full window, and Retry-After is 56s plus half a window.
func checkSlidingWindowHeaders(t *testing.T, driver cache.Driver) {
	t.Helper()

	limiter := newLimiterAt(t, driver, 2, epoch.Add(4*time.Second))

	first := limiter.hit()
	want := map[string]string{
		"RateLimit-Limit":     "2",
		"RateLimit-Remaining": "1",
		"RateLimit-Reset":     "116",
	}

	for header, value := range want {
		if got := first.Header.Get(header); got != value {
			t.Errorf("first %s = %q, want %q", header, got, value)
		}
	}

	second := limiter.hit()

	if got := second.Header.Get("RateLimit-Remaining"); got != "0" {
		t.Errorf("second remaining = %q, want 0", got)
	}

	blocked := limiter.hit()

	if blocked.StatusCode != http.StatusTooManyRequests || blocked.Header.Get("RateLimit-Remaining") != "0" ||
		blocked.Header.Get("Retry-After") != "86" || blocked.Header.Get("RateLimit-Reset") != "86" {
		t.Fatalf("blocked = %d remaining %q retry-after %q reset %q", blocked.StatusCode,
			blocked.Header.Get("RateLimit-Remaining"), blocked.Header.Get("Retry-After"), blocked.Header.Get("RateLimit-Reset"))
	}

	limiter.now = epoch.Add(89 * time.Second)

	if limiter.hit().StatusCode != http.StatusTooManyRequests {
		t.Fatal("request one second before Retry-After was admitted")
	}

	limiter.now = epoch.Add(90 * time.Second)

	if limiter.hit().StatusCode == http.StatusTooManyRequests {
		t.Fatal("request at Retry-After was denied")
	}
}

// TestSlidingWindowHeaders runs checkSlidingWindowHeaders on the memory driver.
func TestSlidingWindowHeaders(t *testing.T) {
	t.Parallel()

	checkSlidingWindowHeaders(t, memorycache.New())
}
