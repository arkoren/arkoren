package ratelimit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// newLimiter creates an in-memory cache for rate-limit tests.
func newLimiter() *cache.Cache {
	return cache.New(memorycache.New())
}

// TestRateLimitAllowsRequestsWithinLimit verifies allowed requests.
func TestRateLimitAllowsRequestsWithinLimit(t *testing.T) {
	t.Parallel()

	limiter := newLimiter()
	handler := ratelimit.Middleware(limiter, func(limiter *ratelimit.Builder) {
		limiter.Limit(2)
		limiter.Window(time.Minute)
		limiter.KeyFunc(func(*http.Request) string {
			return "client"
		})
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		writer.WriteHeader(http.StatusNoContent)

		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if response.StatusCode != http.StatusNoContent || response.Header.Get("RateLimit-Remaining") != "1" {
		t.Fatalf("status = %d, remaining = %q", response.StatusCode, response.Header.Get("RateLimit-Remaining"))
	}
}

// TestRateLimitBlocksRequestsExceedingLimit verifies limit enforcement.
func TestRateLimitBlocksRequestsExceedingLimit(t *testing.T) {
	t.Parallel()

	limiter := newLimiter()
	handler := ratelimit.Middleware(limiter, func(limiter *ratelimit.Builder) {
		limiter.Limit(1)
		limiter.Window(time.Minute)
		limiter.KeyFunc(func(*http.Request) string {
			return "client"
		})
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		writer.WriteHeader(http.StatusNoContent)

		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)

	first := router.Handler(handler).Record(request)

	second := router.Handler(handler).Record(request)

	if first.StatusCode != http.StatusNoContent || second.StatusCode != http.StatusTooManyRequests ||
		second.Header.Get("RateLimit-Remaining") != "0" {
		t.Fatalf("first = %d, second = %d, remaining = %q",
			first.StatusCode, second.StatusCode, second.Header.Get("RateLimit-Remaining"))
	}
}

// TestRateLimitSeparatesKeys verifies independently limited client keys.
func TestRateLimitSeparatesKeys(t *testing.T) {
	t.Parallel()

	limiter := newLimiter()
	handler := ratelimit.Middleware(limiter, func(limiter *ratelimit.Builder) {
		limiter.Limit(1)
		limiter.Window(time.Minute)
		limiter.KeyFunc(func(request *http.Request) string {
			return request.Header.Get("X-Client")
		})
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		writer.WriteHeader(http.StatusNoContent)

		return nil
	})

	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.Header.Set("X-Client", "one")

	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.Header.Set("X-Client", "two")

	if router.Handler(handler).Record(first).StatusCode != http.StatusNoContent ||
		router.Handler(handler).Record(second).StatusCode != http.StatusNoContent {
		t.Fatal("rate limit did not separate client keys")
	}
}

// TestRateLimitResetsAfterWindow verifies counter expiry resets the limit using
// public middleware and virtual time, without sleeping on the wall clock.
func TestRateLimitResetsAfterWindow(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		limiter := newLimiter()
		handler := ratelimit.Middleware(limiter, func(limiter *ratelimit.Builder) {
			limiter.Limit(1)
			limiter.Window(20 * time.Millisecond)
			limiter.KeyFunc(func(*http.Request) string {
				return "client"
			})
		})(func(writer http.ResponseWriter, _ *http.Request) error {
			writer.WriteHeader(http.StatusNoContent)

			return nil
		})

		request := httptest.NewRequest(http.MethodGet, "/", nil)

		if router.Handler(handler).Record(request).StatusCode != http.StatusNoContent ||
			router.Handler(handler).Record(request).StatusCode != http.StatusTooManyRequests {
			t.Fatal("rate limit did not enforce window limit")
		}

		time.Sleep(40 * time.Millisecond)

		if router.Handler(handler).Record(request).StatusCode != http.StatusNoContent {
			t.Fatal("rate limit did not reset after window")
		}
	})
}

// TestRateLimitSetsRetryAfterWhenBlocked verifies a blocked response carries
// matching whole-second Retry-After and RateLimit-Reset values. Their exact
// value depends on the position in the window; window_test.go pins it with a
// fixed clock.
func TestRateLimitSetsRetryAfterWhenBlocked(t *testing.T) {
	t.Parallel()

	limiter := newLimiter()
	handler := ratelimit.Middleware(limiter, func(limiter *ratelimit.Builder) {
		limiter.Limit(1)
		limiter.Window(1500 * time.Millisecond)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)

	_ = router.Handler(handler).Record(request)

	blocked := router.Handler(handler).Record(request)

	retryAfter, err := strconv.Atoi(blocked.Header.Get("Retry-After"))

	if blocked.StatusCode != http.StatusTooManyRequests || err != nil || retryAfter < 1 || retryAfter > 3 {
		t.Fatalf("status = %d, retry-after = %q", blocked.StatusCode, blocked.Header.Get("Retry-After"))
	}

	if blocked.Header.Get("RateLimit-Reset") != blocked.Header.Get("Retry-After") {
		t.Fatalf("reset = %q, retry-after = %q", blocked.Header.Get("RateLimit-Reset"), blocked.Header.Get("Retry-After"))
	}
}

// TestRateLimitGroupsIPv6ByPrefix checks that two IPv6 addresses in the same
// /64 share a request budget instead of bypassing the limiter.
func TestRateLimitGroupsIPv6ByPrefix(t *testing.T) {
	t.Parallel()

	limiter := newLimiter()
	handler := ratelimit.Middleware(limiter, func(limiter *ratelimit.Builder) {
		limiter.Limit(1)
		limiter.Window(time.Minute)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.RemoteAddr = "[2001:db8::1]:1234"
	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.RemoteAddr = "[2001:db8::2]:1234"

	_ = router.Handler(handler).Record(first)

	if response := router.Handler(handler).Record(second); response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 for the same /64", response.StatusCode)
	}
}

// forwardedRequest returns a request from remote carrying the header lines.
func forwardedRequest(remote string, lines ...string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = remote

	for _, line := range lines {
		request.Header.Add("X-Forwarded-For", line)
	}

	return request
}

// checkTrustedProxyKey resolves the key for a request from remote carrying the
// header lines through a TrustedProxyKey trusting 10.0.0.0/8 and fd00::/8, and
// fails the test unless it equals want.
func checkTrustedProxyKey(t *testing.T, want, remote string, lines ...string) {
	t.Helper()

	keyFunc := ratelimit.TrustedProxyKey(
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("fd00::/8"),
	)

	if got := keyFunc(forwardedRequest(remote, lines...)); got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}

// TestTrustedProxyKeyUntrustedRemoteIgnoresHeader verifies client resolution through trusted proxies.
func TestTrustedProxyKeyUntrustedRemoteIgnoresHeader(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "203.0.113.9", "203.0.113.9:1", "1.2.3.4")
}

// TestTrustedProxyKeyNoHeaderUsesRemote verifies client resolution through trusted proxies.
func TestTrustedProxyKeyNoHeaderUsesRemote(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "10.0.0.1", "10.0.0.1:1")
}

// TestTrustedProxyKeyClientBehindProxy verifies client resolution through trusted proxies.
func TestTrustedProxyKeyClientBehindProxy(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "198.51.100.7", "10.0.0.1:1", "198.51.100.7")
}

// TestTrustedProxyKeySpoofedLeftmostIgnored verifies client resolution through trusted proxies.
func TestTrustedProxyKeySpoofedLeftmostIgnored(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "198.51.100.7", "10.0.0.1:1", "6.6.6.6, 198.51.100.7")
}

// TestTrustedProxyKeyMultipleProxies verifies client resolution through trusted proxies.
func TestTrustedProxyKeyMultipleProxies(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "198.51.100.7", "10.0.0.1:1", "198.51.100.7, 10.0.0.2, 10.0.0.3")
}

// TestTrustedProxyKeyMultipleHeaderLines verifies client resolution through trusted proxies.
func TestTrustedProxyKeyMultipleHeaderLines(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "198.51.100.7", "10.0.0.1:1", "6.6.6.6", "198.51.100.7, 10.0.0.2")
}

// TestTrustedProxyKeyGarbageEntryFallsBackToHop verifies client resolution through trusted proxies.
func TestTrustedProxyKeyGarbageEntryFallsBackToHop(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "10.0.0.2", "10.0.0.1:1", "198.51.100.7, garbage, 10.0.0.2")
}

// TestTrustedProxyKeyGarbageOnlyFallsBackToRemote verifies client resolution through trusted proxies.
func TestTrustedProxyKeyGarbageOnlyFallsBackToRemote(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "10.0.0.1", "10.0.0.1:1", "<script>")
}

// TestTrustedProxyKeyAllTrusted verifies client resolution through trusted proxies.
func TestTrustedProxyKeyAllTrusted(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "10.0.0.5", "10.0.0.1:1", "10.0.0.5, 10.0.0.2")
}

// TestTrustedProxyKeyIPv6ClientReducedTo64 verifies client resolution through trusted proxies.
func TestTrustedProxyKeyIPv6ClientReducedTo64(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "2001:db8::/64", "10.0.0.1:1", "2001:db8::1")
}

// TestTrustedProxyKeyIPv6TrustedProxy verifies client resolution through trusted proxies.
func TestTrustedProxyKeyIPv6TrustedProxy(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "2001:db8:0:1::/64", "[fd00::1]:1", "2001:db8:0:1::5")
}

// TestTrustedProxyKeyMappedIPv4Unmapped verifies client resolution through trusted proxies.
func TestTrustedProxyKeyMappedIPv4Unmapped(t *testing.T) {
	t.Parallel()

	checkTrustedProxyKey(t, "198.51.100.7", "[::ffff:10.0.0.1]:1", "::ffff:198.51.100.7")
}

// prefixDriver prefixes every key it stores and exposes its inner driver, like
// a tenant wrapper.
type prefixDriver struct {
	// Driver supplies the underlying cache operations; overrides verify that
	// wrappers remain on the request path.
	cache.Driver
}

// Unwrap returns the wrapped driver.
func (driver prefixDriver) Unwrap() cache.Driver {
	return driver.Driver
}

// Add stores value under the prefixed key.
func (driver prefixDriver) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return driver.Driver.Add(ctx, "tenant:"+key, value, ttl)
}

// Increment increments the prefixed key.
func (driver prefixDriver) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	return driver.Driver.Increment(ctx, "tenant:"+key, delta)
}

// IncrementWithTTL increments the prefixed key.
func (driver prefixDriver) IncrementWithTTL(
	ctx context.Context,
	key string,
	delta int64,
	ttl time.Duration,
) (int64, time.Duration, error) {
	return driver.Driver.IncrementWithTTL(ctx, "tenant:"+key, delta, ttl)
}

// TTL reads the TTL of the prefixed key.
func (driver prefixDriver) TTL(ctx context.Context, key string) (time.Duration, error) {
	return driver.Driver.TTL(ctx, "tenant:"+key)
}

// TestRateLimitUsesWrapperMethods verifies a wrapping driver's key handling
// is not bypassed.
func TestRateLimitUsesWrapperMethods(t *testing.T) {
	t.Parallel()

	inner := memorycache.New()
	handler := ratelimit.Middleware(cache.New(prefixDriver{
		Driver: inner,
	}), func(limiter *ratelimit.Builder) {
		limiter.Limit(2)
		limiter.Window(time.Minute)
		limiter.Namespace(cache.NewNamespace[int64]("rl", func(space *cache.NamespaceBuilder) {
			space.Separator(":")
		}))
		limiter.KeyFunc(func(*http.Request) string {
			return "client"
		})
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)

	_ = router.Handler(handler).Record(request)

	_ = router.Handler(handler).Record(request)

	// The counter key ends in the window index, which may have advanced once
	// since the requests ran.
	index := time.Now().UnixNano() / time.Minute.Nanoseconds()
	var prefixed, bare bool

	for _, window := range []int64{index - 1, index} {
		suffix := ":" + strconv.FormatInt(window, 10)

		hasPrefixed, err := inner.Has(context.Background(), "tenant:rl:client"+suffix)

		if err != nil {
			t.Fatal(err)
		}

		hasBare, err := inner.Has(context.Background(), "rl:client"+suffix)

		if err != nil {
			t.Fatal(err)
		}

		prefixed, bare = prefixed || hasPrefixed, bare || hasBare
	}

	if !prefixed || bare {
		t.Fatalf("prefixed key present = %t, unprefixed key present = %t", prefixed, bare)
	}
}
