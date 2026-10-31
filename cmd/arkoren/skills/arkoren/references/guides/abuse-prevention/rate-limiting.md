---
title: Rate Limiting
description: Limit how many requests a client can make in a time window, per route or service-wide, with standard headers and a shared counter.
order: 20
---

# Rate Limiting

## Introduction

Login and password-reset attempts can consume expensive work even when they
fail. Rate limiting caps how often a client can call these endpoints, helping
control abuse and keep one client from crowding out the others.

The `ratelimit` middleware counts requests per client in a
[cache](../cache/overview.md) and rejects a request with
`429 Too Many Requests` by default once the client is over its limit. Because
the counts live in the cache, every replica of a service shares them when the
cache is shared, such as Redis.

## Usage

Use the [cache guide](../cache/overview.md) to construct and start a cache driver,
then pass your cache facade `store` to the middleware. Import
`arkoren.dev/core/http/middleware/ratelimit`; `r` below is your existing
[router](../http-fundamentals/routing.md). Register middleware before routes to
apply the default policy: 60 requests per minute per client IP address.

```go
r.Use(ratelimit.Middleware(store))
```

For sensitive routes such as login, use `With` to apply a stricter policy.
Give it a [namespace](../cache/overview.md#sharing-a-prefix-and-ttl-with-namespaces)
with distinct physical names so it counts separately
from the general limit. Import `time` and `arkoren.dev/core/service/cache`;
`login` is your existing `router.Handler`:

```go
r.With(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.Limit(5)
	limiter.Window(15 * time.Minute)
	limiter.Namespace(cache.NewNamespace[int64]("ratelimit.login"))
})).Post("/login", login)
```

New namespaces use `":"`, so this prefix produces
`ratelimit.login:<client key>:<window index>`; the dot inside `ratelimit.login`
stays literal. If a deployed custom namespace used the former dot default,
preserve its counter names while upgrading by replacing the namespace expression
above with:

```go
cache.NewNamespace[int64]("ratelimit.login", func(namespace *cache.NamespaceBuilder) {
	namespace.Separator(".")
})
```

Changing that separator starts separate counters and can reset an active limit.
The built-in `arkoren:ratelimit` namespace already uses `":"` explicitly;
its names and counter policy do not change and require no storage migration.

> [!WARNING]
> The memory cache driver counts per process. With several replicas, each one
> allows the full limit. Use a shared cache in production.

### Configuration bounds

Omitting `Limit` or setting it to zero selects `60`; omitting `Window` or setting
it to zero selects one minute. Neither zero setting disables limiting. Any
positive `int` limit is supported. A nonzero window must be positive and no
larger than `time.Duration(math.MaxInt64/2)`, because counter retention and
recovery can span two windows.

All construction callbacks run once, in order, before final validation. A nil
cache, negative limit or window, oversized window, or invalid namespace panics
at construction. A later callback can replace an earlier invalid setting before
validation. An omitted or nil `KeyFunc` restores the client-IP default.

## What the client sees

When the cache operation succeeds, the response carries `RateLimit-Limit`,
`RateLimit-Remaining`, and `RateLimit-Reset`, the number of seconds until the
full limit is available again if the client stops sending. On a denied response,
`RateLimit-Reset` instead equals `Retry-After`: seconds until one request would
be allowed with no intervening traffic. Recovery values round up to at least one
second. Other clients sharing that key can still consume the available budget.

`RateLimit-Remaining` rounds the estimated available requests down. Decisions
and recovery calculations use exact integer ratios, rounding recovery upward
at nanosecond boundaries and then up to whole seconds for the headers. Large
limits do not lose precision through floating-point conversion. These are
calculations for the sliding-window estimate, not a guarantee about the exact
age of every request.

### Custom denial problems

To change the default message while keeping its `429` status and problem
identity, derive from `ratelimit.ErrRateLimited`. Import
`arkoren.dev/foundation/problem` for the callback type and configure this policy
instead of the earlier registration:

```go
failure := ratelimit.ErrRateLimited.Derive(func(details *problem.Builder) {
	details.Detail("Too many attempts. Please retry later.")
})
r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.ErrorResponse(failure)
}))
```

Only a completely zero `problem.Details{}` selects the default problem. A
meaningful problem with status zero is not replaced: its identity, detail,
headers, extensions, and causes are retained, and the HTTP renderer completes
missing standard members, including status `500`. Causes remain internal
diagnostics, not response text. Set the intended status or derive from the
default rather than assuming a statusless problem becomes
`429`. Denial still skips the protected handler and sets `Retry-After` regardless
of the custom status; event publication is independent of that status too.

## How requests are counted

The middleware uses a **sliding window**. Time is divided into windows of the
configured length, and each client has a counter per window. To decide on a
request, it combines the current window's count with a share of the previous
window's count, weighted by how much of the previous window still overlaps the
last full window length. This avoids the burst that a simple fixed window allows,
where a client could send the full limit at the end of one window and again at
the start of the next.

The estimate assumes requests were evenly distributed over the previous window.
Replicas need reasonably synchronized clocks; precise integer arithmetic does
not remove either assumption.

Rejected requests are not counted, so a client that keeps sending requests
while it is blocked does not stay blocked longer.

Every supported cache driver must increment a counter and set its expiry
atomically through `IncrementWithTTL`; a driver that cannot do this returns
`cache.ErrUnsupported`, and the request fails rather than using an unsafe
fallback. The sliding-window estimate still combines separate counter reads
and refunds, so it is not an exact rolling log of request times.

Cache or refund errors fail the request without calling the protected handler;
they are service failures, not permission to bypass the limit. A nonpositive
current counter returned after increment also fails the request. A missing or
nonpositive previous-window counter is treated as zero. Under concurrency, an
in-flight increment can briefly deny another request before it is refunded,
which errs toward enforcing the limit.

## Choosing the key

Requests are counted per **key**, by default the client's IP address. IPv6
addresses are grouped by their `/64` network, because a single client usually
controls a whole network of that size.

Behind a load balancer or proxy, every request arrives from the proxy's
address, so every client would share one limit. Trust the proxy's
`X-Forwarded-For` header instead, naming only networks actually controlled by
your trusted proxies. The prefix below is illustrative, not a safe default for
every deployment. Import `net/netip`:

```go
ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.KeyFunc(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
})
```

The header is ignored for requests that do not come from a trusted network, so
a client cannot spoof its address. To limit per user instead of per address,
pass your own key function that reads the user from the request.

Install authentication before a user-keyed limiter, and return a stable,
nonempty key for every admitted user. Policies producing the same physical name share
counters; choose distinct physical names for unrelated limits. `Builder.Namespace`
takes `cache.Namespace[int64]`; names and separators are literal, not an isolation
boundary. The default uses `arkoren:ratelimit` with separator `":"`, preserving
existing counter names. Window policy sets retention to twice the window,
regardless of the namespace's write TTL. An invalid namespace panics during
middleware construction after configuration callbacks run. Cache failures fail the
request rather than allowing it through; monitor cache availability.

## Observing denials

Events are off by default. To observe denials, pass your existing
[`*event.Bus`](../events/overview.md) with `limiter.Events(bus)` in the
configuration callback. Normally the first denied request for each key in each
fixed window publishes `ratelimit.Exceeded` (`http.ratelimit.exceeded`). The
payload contains `Key`, `Method`, `Path`, `Limit`, and `Window`.

Deduplication is best-effort, not an exactly-once guarantee. When events are
enabled, each denial attempts one cache `Add` for a marker retained for twice
the window. An existing marker suppresses publication; a marker-write failure
still publishes, so repeated denials can produce duplicates. This failure does
not weaken request denial. Consumers must tolerate duplicates; these in-process
events are not a durable audit log.

`Key` is the client portion, not the namespace or window suffix, and can identify
a person or account. `Path` is the actual URL path, not a route template, and can
identify a resource. Restrict access, redact before export, and choose retention
deliberately; do not expose the whole payload to clients.

For every option, see the [`ratelimit` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/ratelimit).
