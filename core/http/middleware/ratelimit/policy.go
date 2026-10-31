package ratelimit

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
)

// Builder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.Limit(60)
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// Limit is the number of requests allowed within Window. Zero selects 60;
// positive int values are accepted without a float precision limit. A negative
// count makes Middleware panic after all construction callbacks have run.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.Limit(1024)
//	}))
func (limiter *Builder) Limit(limit int) *Builder {
	limiter.settings.Limit = limit

	return limiter
}

// Window is the length of the sliding window over which Limit applies. Zero
// selects one minute. Other values must be positive and at most half the largest
// time.Duration, because counters and recovery intervals span two windows.
// Middleware panics for an invalid final window after all callbacks have run.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.Window(time.Second)
//	}))
func (limiter *Builder) Window(window time.Duration) *Builder {
	limiter.settings.Window = window

	return limiter
}

// KeyFunc extracts the client-specific portion of the cache key.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.KeyFunc(nil)
//	}))
func (limiter *Builder) KeyFunc(keyFunc func(*http.Request) string) *Builder {
	limiter.settings.KeyFunc = keyFunc

	return limiter
}

// Events receives [Exceeded]. Nil, the default, publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.Events(event.New())
//	}))
func (limiter *Builder) Events(bus *event.Bus) *Builder {
	limiter.settings.Events = bus

	return limiter
}

// Middleware returns middleware that limits each key to the configured
// number of requests in any sliding window, counting in c.
//
// The limiter is a sliding-window counter. Time is divided into fixed windows
// of settings.Window aligned to the Unix epoch, and each key keeps one counter
// per window index, joined to the configured namespace and client with a TTL of
// twice the window. A request increments the current window's counter and
// reads the previous window's counter, then estimates the requests in the
// last window as
//
//	previous * (1 - elapsed/Window) + current
//
// where elapsed is the time since the current window began. The request is
// denied when the estimate exceeds Limit. Unlike a fixed window this cannot
// admit twice the limit across a window boundary: the previous window's
// requests keep counting, with linearly decaying weight. The estimate assumes
// requests were evenly spread over the previous window, so it is
// approximate, but it needs only atomic increments. Instances must have
// reasonably synchronized clocks.
//
// Denied requests do not count. A denied request's increment is undone with a
// decrement, so a client that keeps retrying while limited does not extend its
// own block: the counters would otherwise carry into later windows and a
// client sending many times the limit could stay blocked for many windows.
// Concurrent requests may briefly be denied because another in-flight
// request's increment is visible, which errs towards the limit.
//
// After successful counter operations, responses carry RateLimit-Limit,
// RateLimit-Remaining (the estimated requests still available, rounded down),
// and RateLimit-Reset. Reset is the
// whole seconds, rounded up and at least one, until the estimate would decay
// to zero if the client stopped sending, which is the moment the full Limit
// is available again. A denial also carries Retry-After, independently of the
// configured problem's status, equal to its Reset: the whole seconds, rounded up
// and at least one, until the estimate
// leaves room for one more request. Both are computed from the same window
// arithmetic as the decision and never fall early.
// Counts and recovery intervals use integer ratios with upward rounding, so
// large limits and nanosecond boundaries do not lose precision through floats.
//
// Counters are kept with [cache.Cache.IncrementWithTTL], which increments and
// sets the expiry atomically, so a counter can never outlive its window. A
// driver that cannot do this returns [cache.ErrUnsupported] and the limiter
// cannot be used with it.
//
// With [Builder.Events], the first denied request of a key in each fixed window
// normally publishes [Exceeded]. Finding the first costs one cache Add per denied
// request, only when events are enabled. Deduplication is best-effort: failure to
// add the marker still publishes, so repeated denials can produce duplicate
// events. Counter failures instead fail the request; they never bypass the limit.
//
// A nil cache, negative Limit, invalid Namespace, or Window outside
// (0, time.Duration(math.MaxInt64/2)] panics after all callbacks. Zero Limit and
// Window retain their defaults of 60 and one minute. Cache errors, including a
// nonpositive counter returned after increment, fail the request without calling
// the protected handler.
//
// Example:
//
//	routes.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.Namespace(cache.NewNamespace[int64]("app.rate"))
//	}))
func Middleware(c *cache.Cache, configure ...BuilderFunc) router.Middleware {
	config := newSettings(configure...)

	if c == nil {
		panic("ratelimit: cache must not be nil")
	}

	if config.Limit < 0 {
		panic("ratelimit: Limit must be positive or zero for the default")
	}

	if config.Window < 0 || config.Window > time.Duration(math.MaxInt64/2) {
		panic("ratelimit: Window must be positive and fit twice in time.Duration")
	}

	if err := config.Namespace.Err(); err != nil {
		panic("ratelimit: invalid namespace: " + err.Error())
	}

	return middleware(c, config, time.Now)
}

// forwardedForHeader carries the proxy chain a request traveled through.
const forwardedForHeader = "X-Forwarded-For"

// clientIP returns the request remote address without its port, reduced by
// [addressKey]. An address that does not parse is returned unchanged.
func clientIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)

	if err != nil {
		host = request.RemoteAddr
	}

	address, err := netip.ParseAddr(host)

	if err != nil {
		return host
	}

	return addressKey(address)
}

// addressKey returns the key for address. IPv4-mapped IPv6 addresses are
// unmapped and IPv6 addresses are reduced to their /64 prefix, the usual
// per-client allocation, so a client cannot rotate addresses to escape its
// limit.
func addressKey(address netip.Addr) string {
	address = address.Unmap()

	if address.Is4() {
		return address.String()
	}

	// Callers pass parsed addresses; a non-IPv4 address always accepts a /64 prefix.
	prefix, _ := address.WithZone("").Prefix(64)

	return prefix.String()
}

// TrustedProxyKey returns a key function for use with [Builder.KeyFunc] that
// identifies clients behind reverse proxies or load balancers. When the
// request's RemoteAddr lies within trusted, the X-Forwarded-For header lines
// are walked from right to left and the first address outside trusted is the
// client. When RemoteAddr is not trusted the header is client-controlled and
// ignored. If an entry cannot be parsed, or every address is trusted, the
// nearest trusted hop that reported it is the key, so attacker-chosen strings
// never become keys. Addresses are reduced as in the default key function.
//
// Example:
//
//	routes.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.KeyFunc(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
//	}))
func TrustedProxyKey(trusted ...netip.Prefix) func(*http.Request) string {
	trusted = slices.Clone(trusted)
	// trustedAddress checks the construction snapshot for each parsed proxy hop;
	// a caller's forwarded header never changes the configured trust set.
	trustedAddress := func(address netip.Addr) bool {
		return slices.ContainsFunc(trusted, func(prefix netip.Prefix) bool {
			return prefix.Contains(address)
		})
	}

	return func(request *http.Request) string {
		host, _, err := net.SplitHostPort(request.RemoteAddr)

		if err != nil {
			host = request.RemoteAddr
		}

		hop, err := netip.ParseAddr(host)

		if err != nil {
			return host
		}

		hop = hop.Unmap().WithZone("")

		if !trustedAddress(hop) {
			return addressKey(hop)
		}

		chain := strings.Join(request.Header.Values(forwardedForHeader), ",")

		for _, entry := range slices.Backward(strings.Split(chain, ",")) {
			address, err := netip.ParseAddr(strings.TrimSpace(entry))

			if err != nil {
				break
			}

			address = address.Unmap().WithZone("")

			if !trustedAddress(address) {
				return addressKey(address)
			}

			hop = address
		}

		return addressKey(hop)
	}
}
