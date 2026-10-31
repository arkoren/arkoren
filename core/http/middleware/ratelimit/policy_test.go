package ratelimit_test

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
)

// requireRatePolicyPanic verifies invalid policy rejection only after every
// callback has run, allowing later callbacks to correct intermediate values.
func requireRatePolicyPanic(t *testing.T, configure ratelimit.BuilderFunc) {
	t.Helper()

	called := false
	defer func() {
		if recover() == nil || !called {
			t.Error("invalid policy was not rejected after all callbacks")
		}
	}()
	ratelimit.Middleware(newLimiter(), configure, func(*ratelimit.Builder) {
		called = true
	})
}

// TestRatePolicyRejectsNegativeLimit excludes an impossible request count rather
// than allowing a policy whose recovery arithmetic has a negative target.
func TestRatePolicyRejectsNegativeLimit(t *testing.T) {
	t.Parallel()

	requireRatePolicyPanic(t, func(policy *ratelimit.Builder) {
		policy.Limit(-1)
	})
}

// TestRatePolicyRejectsNegativeWindow excludes a negative cache lifetime before
// the first request reaches IncrementWithTTL.
func TestRatePolicyRejectsNegativeWindow(t *testing.T) {
	t.Parallel()

	requireRatePolicyPanic(t, func(policy *ratelimit.Builder) {
		policy.Window(-time.Second)
	})
}

// TestRatePolicyRejectsRetentionOverflow excludes windows whose required
// twice-window retention cannot be represented by time.Duration.
func TestRatePolicyRejectsRetentionOverflow(t *testing.T) {
	t.Parallel()

	requireRatePolicyPanic(t, func(policy *ratelimit.Builder) {
		policy.Window(time.Duration(math.MaxInt64/2 + 1))
	})
}

// TestRatePolicyRejectsMissingCache rejects the required dependency after
// callbacks, not as a nil dereference while serving the first protected request.
func TestRatePolicyRejectsMissingCache(t *testing.T) {
	t.Parallel()

	called := false
	defer func() {
		if recover() == nil || !called {
			t.Fatal("missing cache was not rejected after callbacks")
		}
	}()
	ratelimit.Middleware(nil, func(*ratelimit.Builder) {
		called = true
	})
}

// TestRatePolicyInvalidCountersFailClosed checks a corrupted negative counter
// and a signed counter overflow using the documented public namespace/key seam.
// Neither value can authorize a protected request or masquerade as normal usage.
func TestRatePolicyInvalidCountersFailClosed(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		for _, initial := range []int64{-1, math.MaxInt64} {
			checkInvalidRateCounter(t, initial)
		}
	})
}

// checkInvalidRateCounter seeds one native counter, then observes only the
// middleware's HTTP result and whether it called the protected handler.
func checkInvalidRateCounter(t *testing.T, initial int64) {
	t.Helper()

	store := newLimiter()
	space := cache.NewNamespace[int64]("regression.rate")
	index := time.Now().UnixNano() / int64(time.Minute)
	key := space.Key("client:" + strconv.FormatInt(index, 10))

	if err := store.Put(t.Context(), key, initial); err != nil {
		t.Fatal(err)
	}

	called := false
	handler := ratelimit.Middleware(store, func(policy *ratelimit.Builder) {
		policy.Namespace(space)
		policy.Limit(1)
		policy.KeyFunc(func(*http.Request) string {
			return "client"
		})
	})(func(http.ResponseWriter, *http.Request) error {
		called = true

		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if called || response.StatusCode != http.StatusInternalServerError {
		t.Errorf("initial count = %d, protected handler = %t, status = %d", initial, called, response.StatusCode)
	}
}

// TestRatePolicyLargeLimitKeepsRemainingCount preserves positive int limits
// without narrowing them through an inexact float-to-integer conversion.
func TestRatePolicyLargeLimitKeepsRemainingCount(t *testing.T) {
	t.Parallel()

	handler := ratelimit.Middleware(newLimiter(), func(policy *ratelimit.Builder) {
		policy.Limit(math.MaxInt)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if got := response.Header.Get("RateLimit-Remaining"); got != strconv.Itoa(math.MaxInt-1) {
		t.Fatalf("remaining = %q, want %d", got, math.MaxInt-1)
	}
}

// TestRatePolicyLargePreviousCountDecaysPrecisely exercises the overflowing
// multiplication behind weighted counts through a real cache and HTTP headers.
// One virtual second frees exactly floor(Limit/60) slots from a full prior minute.
func TestRatePolicyLargePreviousCountDecaysPrecisely(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Minute - time.Duration(time.Now().UnixNano()%int64(time.Minute)))

		store := newLimiter()
		space := cache.NewNamespace[int64]("regression.previous")
		index := time.Now().UnixNano() / int64(time.Minute)
		key := space.Key("client:" + strconv.FormatInt(index-1, 10))

		if err := store.Put(t.Context(), key, int64(math.MaxInt)); err != nil {
			t.Fatal(err)
		}

		handler := ratelimit.Middleware(store, func(policy *ratelimit.Builder) {
			policy.Namespace(space)
			policy.Limit(math.MaxInt)
			policy.KeyFunc(func(*http.Request) string {
				return "client"
			})
		})(func(http.ResponseWriter, *http.Request) error {
			return nil
		})

		request := httptest.NewRequest(http.MethodGet, "/", nil)

		if response := router.Handler(handler).Record(request); response.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("full prior window status = %d, want 429", response.StatusCode)
		}

		time.Sleep(time.Second)

		response := router.Handler(handler).Record(request)

		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("decayed status = %d, want 204", response.StatusCode)
		}

		expected := strconv.FormatInt(int64(math.MaxInt)/60-1, 10)

		if got := response.Header.Get("RateLimit-Remaining"); got != expected {
			t.Fatalf("remaining = %q, want %q", got, expected)
		}
	})
}

// TestRatePolicyZeroValuesKeepDefaults checks the existing zero/default policy
// through response headers, without inspecting completed private settings.
func TestRatePolicyZeroValuesKeepDefaults(t *testing.T) {
	t.Parallel()

	handler := ratelimit.Middleware(newLimiter(), func(policy *ratelimit.Builder) {
		policy.Limit(0)
		policy.Window(0)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Header.Get("RateLimit-Limit") != "60" || response.Header.Get("RateLimit-Remaining") != "59" {
		t.Fatalf("zero policy changed defaults: %v", response.Header)
	}
}

// TestRatePolicyValidationUsesFinalSettings allows an invalid intermediate edit
// to be corrected, while retaining the public callbacks' once/in-order contract.
func TestRatePolicyValidationUsesFinalSettings(t *testing.T) {
	t.Parallel()

	order := 0
	handler := ratelimit.Middleware(newLimiter(), func(policy *ratelimit.Builder) {
		order++
		policy.Window(-time.Second)
		policy.Limit(-1)
	}, func(policy *ratelimit.Builder) {
		if order != 1 {
			t.Fatal("callbacks were replayed or reordered")
		}

		order++
		policy.Window(time.Minute)
		policy.Limit(1)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if order != 2 || response.StatusCode != http.StatusNoContent {
		t.Fatalf("callbacks = %d, status = %d", order, response.StatusCode)
	}
}

// TestRatePolicyRecoveryNeverRoundsEarly checks a full recovery just above a
// whole-second boundary that float duration arithmetic rounds down to that second.
func TestRatePolicyRecoveryNeverRoundsEarly(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		window := (1<<29)*time.Second + time.Nanosecond
		time.Sleep(window - time.Duration(time.Now().UnixNano()%int64(window)))

		handler := ratelimit.Middleware(newLimiter(), func(policy *ratelimit.Builder) {
			policy.Limit(1)
			policy.Window(window)
		})(func(http.ResponseWriter, *http.Request) error {
			return nil
		})

		request := httptest.NewRequest(http.MethodGet, "/", nil)

		_ = router.Handler(handler).Record(request)

		blocked := router.Handler(handler).Record(request)

		if got := blocked.Header.Get("Retry-After"); got != "1073741825" {
			t.Fatalf("Retry-After = %q, want upward-rounded recovery", got)
		}
	})
}

// TestRatePolicyLargestWindowKeepsRetryAfterPositive reaches the largest valid
// twice-window recovery interval through public middleware. Virtual time places
// the requests exactly on a boundary without waiting centuries in real time.
func TestRatePolicyLargestWindowKeepsRetryAfterPositive(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		window := time.Duration(math.MaxInt64 / 2)
		time.Sleep(window - time.Duration(time.Now().UnixNano()%int64(window)))

		handler := ratelimit.Middleware(newLimiter(), func(policy *ratelimit.Builder) {
			policy.Limit(1)
			policy.Window(window)
		})(func(http.ResponseWriter, *http.Request) error {
			return nil
		})

		request := httptest.NewRequest(http.MethodGet, "/", nil)

		_ = router.Handler(handler).Record(request)

		blocked := router.Handler(handler).Record(request)

		if blocked.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", blocked.StatusCode)
		}

		if got := blocked.Header.Get("Retry-After"); got != "9223372037" {
			t.Fatalf("Retry-After = %q, want full two-window recovery", got)
		}
	})
}
