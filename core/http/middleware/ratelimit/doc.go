// Package ratelimit provides middleware that limits requests per key with a sliding window counted in a cache,
// answering 429 by default with RateLimit headers.
//
// With [Builder.Events], denials publish [Exceeded] ("http.ratelimit.exceeded")
// with the key and policy. A cache marker normally deduplicates each key/window;
// marker failures still publish and can produce duplicates. This is best-effort
// deduplication, not durable or exactly-once delivery.
//
// Zero Limit and Window select 60 requests per minute. Positive int limits are
// supported without float precision loss. A window must fit twice in
// time.Duration; negative policy values, an invalid namespace, or a nil cache
// panic after construction callbacks. Invalid native counter results and cache
// errors fail requests without invoking the protected handler. Recovery headers
// round up from integer nanosecond ratios, while the sliding-window estimate
// still assumes requests were uniformly distributed within each fixed window.
// ErrorResponse selects the standard denial problem only when completely zero.
// Nonzero problems, including statusless custom values, reach the HTTP renderer
// unchanged; it supplies missing standard members such as status 500. Denials
// still carry Retry-After regardless of that custom rendered status.
//
// Example:
//
//	store := cache.New(memorycache.New())
//	routes := router.New()
//	routes.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.Limit(10)
//		limiter.Window(time.Second)
//	}))
package ratelimit
