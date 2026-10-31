# Rate Limit

`arkoren.dev/core/http/middleware/ratelimit` provides sliding-window rate-limit middleware for
Arkoren applications. Each request weighs the previous window's count by how
much of it still overlaps the sliding window, reducing fixed-window boundary
bursts. This is an estimate, not an exact rolling request log. Denied requests
are not counted. Counters
are stored through `arkoren.dev/core/service/cache`, so a shared cache driver enforces the
same limit across application instances (whose clocks should be roughly in sync).

Given a `store` cache and an existing `r` router, allow ten requests per second.
This fragment also uses `time`:

```go
r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.Limit(10)
	limiter.Window(time.Second)
}))
```

Behind a proxy or load balancer, key clients by their forwarded address. The helper trusts
`X-Forwarded-For` only when `RemoteAddr` is within the given prefixes, and picks
the rightmost untrusted entry. Import `net/netip` for the prefix:

```go
r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.KeyFunc(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
}))
```

Counters use `Cache.IncrementWithTTL`, which increments and sets the expiry atomically. A driver that
cannot do this returns `cache.ErrUnsupported` and cannot back the limiter.

Construction callbacks set a custom limit, window, key function, namespace, and error response. By default,
requests are grouped by client IP, limited to 60 requests per minute, and use
keys in the `arkoren:ratelimit:` namespace.

Zero `Limit` and `Window` retain those defaults. Positive `int` limits are accepted;
counts and recovery intervals use integer ratios rather than floating point.
`RateLimit-Remaining` rounds down, while recovery and `Retry-After` round up to
whole seconds and never report less than one second. The estimate is still not
an exact request log.

A negative limit, negative window, window above `time.Duration(math.MaxInt64/2)`,
invalid namespace, or nil cache panics after all callbacks. The window bound keeps
two-window counter retention and recovery representable. Cache failures, including
a nonpositive native count after increment, fail the request without calling the
protected handler. Fix invalid configuration at construction and handle cache
availability as a service failure, not as permission to bypass the limiter.

`Builder.ErrorResponse` defaults to the standard 429 problem only for a completely
zero `problem.Details{}`. A meaningful custom problem is kept even if its stored
status is zero. The HTTP renderer completes its missing members (including status
500), without discarding its type, title, detail, headers, or extensions. Set an
explicit status when you want a custom 429 response. Every denial still carries
`Retry-After`; its classification and events do not depend on the problem's status.

With `Builder.Events(bus)`, a cache marker normally limits `Exceeded` publication
to the first denial per client key and fixed window. If adding that marker fails,
the limiter still publishes; repeated denials can therefore produce duplicates.
Listeners must tolerate them. This best-effort event deduplication is not a
durable delivery guarantee and does not weaken request-limit enforcement.

Use your actual proxy network, not an untrusted client network, in the prefix
list. The second example is an alternative policy, not an additional limiter.
An in-process [memory cache](../../../service/cache/memorycache/README.md)
limits only one process; use a shared driver for a limit across replicas.
