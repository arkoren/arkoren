package ratelimit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// markerFailureDriver keeps real memory-backed counters but fails the optional
// event marker operation. It models a partial cache failure without network I/O.
type markerFailureDriver struct {
	// Driver supplies every counter operation; only Add is replaced below.
	cache.Driver
}

// Add reports a marker-write failure without changing the underlying counters.
// The limiter must still deny the request and may publish a duplicate event.
func (driver markerFailureDriver) Add(context.Context, string, []byte, time.Duration) (bool, error) {
	return false, errors.New("marker unavailable")
}

// TestRatePolicyMarkerFailureAllowsDuplicateEvents verifies the documented
// best-effort publication rule through public middleware and the public event
// bus. Marker failure must not turn either denied request into an allowed one.
func TestRatePolicyMarkerFailureAllowsDuplicateEvents(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		bus := event.New()
		publications := 0

		unsubscribe, err := bus.Listen(ratelimit.Exceeded, func(context.Context, ratelimit.ExceededEvent) error {
			publications++

			return nil
		})

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(unsubscribe)

		store := cache.New(markerFailureDriver{Driver: memorycache.New()})
		handler := ratelimit.Middleware(store, func(policy *ratelimit.Builder) {
			policy.Limit(1)
			policy.Events(bus)
		})(func(http.ResponseWriter, *http.Request) error {
			return nil
		})

		request := httptest.NewRequest(http.MethodGet, "/", nil)

		_ = router.Handler(handler).Record(request)

		for range 2 {
			if response := router.Handler(handler).Record(request); response.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("marker failure changed denial status to %d", response.StatusCode)
			}
		}

		if publications != 2 {
			t.Fatalf("publications = %d, want two best-effort events after marker failures", publications)
		}
	})
}

// TestRateLimitPublishesExceededOncePerWindow verifies repeated denials of
// one key publish a single event.
func TestRateLimitPublishesExceededOncePerWindow(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got []ratelimit.ExceededEvent

	if _, err := bus.Listen(ratelimit.Exceeded, func(_ context.Context, payload ratelimit.ExceededEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := ratelimit.Middleware(newLimiter(),
		func(limiter *ratelimit.Builder) {
			limiter.Limit(1)
		},
		func(limiter *ratelimit.Builder) {
			limiter.Window(time.Hour)
		},
		func(limiter *ratelimit.Builder) {
			limiter.KeyFunc(func(*http.Request) string {
				return "client-1"
			})
		},
		func(limiter *ratelimit.Builder) {
			limiter.Events(bus)
		},
	)(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	for range 4 {
		router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "/search?q=x", nil))
	}

	if len(got) != 1 {
		t.Fatalf("Exceeded events = %d, want one", len(got))
	}

	if got[0].Key != "client-1" || got[0].Path != "/search" {
		t.Fatalf("Exceeded client/request = %+v", got[0])
	}

	if got[0].Limit != 1 || got[0].Window != time.Hour {
		t.Fatalf("Exceeded = %+v, want one event for client-1", got)
	}
}
