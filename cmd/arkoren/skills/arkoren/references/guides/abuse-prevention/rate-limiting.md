---
title: Rate Limiting
description: Apply cache-backed request budgets, choose trusted client keys, and interpret recovery headers and backend failures.
order: 20
summary:
  - Apply separate route budgets using a configured cache and literal namespaces.
  - Choose IP or authenticated-user keys without trusting caller-supplied proxy headers.
  - Understand approximate sliding-window counts, refunds, and cache failure behavior.
  - Interpret recovery headers and observe denials without exposing client identifiers.
---

# Rate Limiting

A password-reset endpoint can trigger hashing, mail, or external calls even when
its caller never completes the flow. A rate budget bounds admitted attempts before
that work runs. The `ratelimit` middleware counts requests by a client key in a
cache and returns `429 Too Many Requests` when its estimate exceeds your limit.

## Route Budgets

Use your configured cache facade `store`; [Cache Drivers](../cache/drivers.md)
owns backend construction and lifecycle. A memory backend keeps independent
budgets in each process. If replicas must consult the same counters, give them
the same shared backend and key policy. This does not make the limiter an exact
distributed request log.

For an existing router `routes` and password-reset handler `reset`, import
`time`, `arkoren.dev/core/service/cache`, and
`arkoren.dev/core/http/middleware/ratelimit`. Protect only that route:

```go
routes.With(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.Limit(4)
	limiter.Window(10 * time.Minute)
	limiter.Namespace(cache.NewNamespace[int64]("app:password-reset"))
})).Post("/password-reset", reset)
```

This policy admits up to its sliding-window estimate of four requests per client
over ten minutes. Place it before expensive verification or handler work. Choose
the appropriate [middleware scope](../http-fundamentals/middleware.md#choose-scope)
and [execution order](../http-fundamentals/middleware.md#order-policies); a
route-specific limiter does not protect other routes.

The registration snippets on this page are alternatives. Combine the settings
you need in one construction rather than registering the same route repeatedly.

If you intentionally want the default policy across a router's subsequently
registered routes, register it before those routes:

```go
routes.Use(ratelimit.Middleware(store))
```

The default is 60 requests per minute per client IP. Omitting `Limit` or `Window`,
or setting either to zero, restores that setting's default; zero does not disable
limiting. Positive `int` limits are supported. A nonzero window must be positive
and at most `time.Duration(math.MaxInt64/2)` so retention fits two windows.

The default namespace is `arkoren:ratelimit`, with `":"` separators. The first
example instead stores counters named
`app:password-reset:<client key>:<window index>`. Distinct policies need distinct
physical names; a namespace is naming, not an access-control boundary. Read
[Cache Keys](../cache/keys.md) before sharing or changing prefixes and separators.
Changing a deployed name starts separate counters and can reset an active budget.
For a namespace previously using dots, explicitly keep `namespace.Separator(".")`
in its `cache.NewNamespace` callback; new namespaces default to colons.

Construction callbacks run once in order, then final settings are validated. A
nil cache, negative limit, invalid window, or zero/invalid namespace panics. A
later callback can replace an earlier invalid setting before validation; a nil
callback itself panics.

## Client Keys

By default, the limiter uses `RemoteAddr` without its port, unmaps IPv4-mapped
IPv6 addresses, and groups IPv6 addresses by `/64`. It does not trust forwarded
headers. People behind one public address share a budget, so choose limits with
that grouping in mind.

Behind a reverse proxy, configure only networks belonging to trusted proxies.
With `net/netip` imported, this alternative policy uses the example proxy subnet;
replace it with your actual deployment's trust boundary:

```go
routes.With(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.KeyFunc(ratelimit.TrustedProxyKey(
		netip.MustParsePrefix("192.0.2.0/24"),
	))
})).Post("/password-reset", reset)
```

`TrustedProxyKey` ignores `X-Forwarded-For` unless the immediate peer is trusted.
It walks trusted chains right to left and uses the first untrusted address.
Malformed entries or an entirely trusted chain fall back to the nearest trusted
hop that reported them, rather than turning attacker-chosen text into a key.
The resulting addresses use the same IPv6 grouping as the default.

For an account budget, supply `KeyFunc` that reads your application's verified
identity. Run authentication before that limiter and return a stable, nonempty
key for each admitted user. Do not let a request body or arbitrary account header
choose whose budget is charged. A nil `KeyFunc` restores IP-based keys.

Client keys appear in cache names and optional denial events. Prefer an opaque
application identifier over an email, credential, or token. Account budgets
control volume; your application must still check permission for the operation.

## Counting Behavior

The limiter divides time into fixed windows aligned to the Unix epoch. Each
request atomically increments the current window counter. If the counter has no
expiry, the operation gives it twice-window retention; an existing expiry is preserved.
It then reads the previous counter and adds a decreasing
share of that count to the current count. That weighted sum estimates usage over
the last window length.

The estimate assumes requests were evenly distributed within the previous
window, so it is not an exact rolling log. Replicas also need reasonably
synchronized clocks. Integer-ratio calculations preserve large-limit precision
and round recovery upward at nanosecond boundaries, but do not remove these
assumptions. No cache lock serializes the complete increment/read/refund sequence.

An over-budget request is refunded with a decrement and never reaches the
handler. Repeated denied requests therefore do not extend their own block when
refunds succeed. Concurrent requests can briefly observe an increment before
its refund and be denied conservatively. An admitted request remains counted
even if its handler later returns an error.

The backend must implement atomic `IncrementWithTTL`; unsupported drivers return
`cache.ErrUnsupported`. Increment, previous-counter read, or refund errors stop
the request without calling the handler. A nonpositive counter after increment
also fails the request; a missing or nonpositive previous counter counts as zero.
Counter retention overrides the namespace's write TTL.

These failures are not rate-limit denials or permission to bypass protection.
The middleware returns backend errors, wrapping refund failures with context;
it does not sanitize native backend diagnostics. The default HTTP renderer hides
ordinary unclassified backend errors behind a generic `500`, but your logging or
custom error handling can still expose them. Use [cache monitoring](../cache/monitoring.md) and a
[shared telemetry handle](../observability/telemetry.md#shared-handle) for backend
health, with an explicit privacy policy for error data.

## Response Timing

After successful counter operations, responses carry these headers:

| Header | Meaning |
| --- | --- |
| `RateLimit-Limit` | Configured request count. |
| `RateLimit-Remaining` | Estimated available requests, rounded down; zero on denial. |
| `RateLimit-Reset` | On admission, seconds until the full budget recovers if traffic stops. On denial, seconds until one request fits. |
| `Retry-After` | On denial only, the same recovery interval as `RateLimit-Reset`. |

Recovery intervals round up to whole seconds and are at least one second. They
assume no intervening traffic: another caller sharing the key can spend the
recovered budget. These are estimate-based delays, not the age of the oldest
individual request. Counter failures return before these headers are set.

To change denial text without accidentally changing its `429` status or problem
identity, derive from `ratelimit.ErrRateLimited`. Import
`arkoren.dev/foundation/problem` for the callback:

```go
failure := ratelimit.ErrRateLimited.Derive(func(details *problem.Builder) {
	details.Detail("The reset budget is exhausted. Try again later.")
})
```

Use that value when constructing the route's limiter:

```go
routes.With(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.ErrorResponse(failure)
})).Post("/password-reset", reset)
```

Only a completely zero `problem.Details{}` selects the default denial. A nonzero
problem with no status is preserved; the HTTP renderer fills missing standard
members, including status `500`. Its identity, detail, headers, extensions, and
internal causes are not discarded. Denial still skips the handler and sets
`Retry-After` regardless of your custom problem's status. Causes are internal
diagnostics, not response text.

## Denial Events

Events are off by default. To inspect denials, configure your existing
[`*event.Bus`](../events/overview.md) when constructing the route's limiter:

```go
routes.With(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.Events(bus)
})).Post("/password-reset", reset)
```

Normally the first denial for each key in each fixed window publishes
`ratelimit.Exceeded` (`http.ratelimit.exceeded`). Its payload contains `Key`,
`Method`, `Path`, `Limit`, and `Window`. Publication does not depend on a custom
denial problem retaining status `429`.

With events enabled, each denial attempts a cache `Add` for a marker retained
for twice the window. An existing marker suppresses publication; a marker-write
failure still publishes and can produce duplicates. This best-effort mechanism
does not weaken the denial, but it is neither exactly-once delivery nor a durable
audit log. Consumers must tolerate duplicate events.

`Key` is the client portion, without the namespace or window index. `Path` is the
actual URL path, not a route template. Both can identify people or resources:
restrict access, redact before export, and choose retention deliberately. Never
send the event payload to a client as an explanation of its denial.

For all options, see the [`ratelimit` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/ratelimit).
