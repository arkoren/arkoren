---
title: Client Addresses
description: Resolve client addresses safely for direct requests, trusted proxies, and Cloudflare.
order: 35
summary:
  - Choose a resolver that matches the actual network path to your server.
  - Trust forwarded addresses only from proxies that protect and set those headers.
  - Distinguish client addresses from rate-limit buckets and upstream identity.
---

# Client Addresses

An HTTP server sees the address of its immediate peer. Behind a reverse proxy,
that peer may be the proxy rather than the user. The `clientip` package from
`arkoren.dev/core/http/clientip` provides functions with the shape
`func(*http.Request) string` so request-handling code can resolve an address
without trusting arbitrary headers.

Use a core release that includes `http/clientip`. IAM does not require that
release itself; applications choose their own resolver and core version.

These functions return addresses, not rate-limit buckets. In particular they
do not group IPv6 addresses by `/64`. Use
[client key policy](../abuse-prevention/rate-limiting.md#client-keys) to choose
the appropriate bucket.

## Direct Requests

If clients connect directly, use `clientip.Direct()`. It ignores all headers
and extracts the host from `RemoteAddr`, returning `RemoteAddr` unchanged when
it is not a host and port. IAM already uses this behavior by default.

## Trusted Proxies

If isolated proxies append `X-Forwarded-For`, supply only the networks that
actually send trusted proxy traffic. The following prefix is an example, not a
deployment recommendation:

```go
resolve := clientip.Proxies(netip.MustParsePrefix("10.20.0.0/24"))
```

Import `net/netip` and `clientip` from the paths above. The resolver first
checks the immediate peer. For a trusted peer, it walks the header from right
to left and stops at the first untrusted address. A malformed entry stops the
walk at the nearest trusted hop; only bare IP literals are accepted, not
addresses with ports. An untrusted peer cannot supply a forwarding chain.
Trusting a network that arbitrary clients can use allows address spoofing.

Call `resolve(r)` for an HTTP request `r`. To use this resolver for IAM's abuse
limits, pass it in the construction callback. Assume the `db` and `store`
collaborators from [IAM Overview](../iam/overview.md):

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.ClientIP(resolve)
})
```

Handle the construction error before using `identity`. Supplying a resolver is
optional, and IAM does not import this package on your behalf.

For a generic rate limiter, convert the resolved address to a bucket at the
`KeyFunc` boundary. Import `net/http` and `ratelimit` from
`arkoren.dev/core/http/middleware/ratelimit`:

```go
limiter.KeyFunc(func(r *http.Request) string {
	return ratelimit.AddressKey(resolve(r))
})
```

Here `limiter` is the `*ratelimit.Builder` in the
[middleware construction callback](../abuse-prevention/rate-limiting.md#client-keys).
`AddressKey` applies the limiter's IPv6 `/64` grouping; passing `resolve`
directly would use a separate key for every IPv6 address.

## Deployment Choices

| Path to the application | Choice and boundary |
| --- | --- |
| Direct connection | `Direct()` ignores forwarded headers. |
| Generic reverse proxy | `Proxies` with the actual isolated peer networks; each trusted hop must append or replace the header. |
| Cloudflare directly to origin | `Cloudflare()` uses `CF-Connecting-IP` only from its embedded Cloudflare peer ranges. |
| AWS Application Load Balancer | Use `Proxies` with actual peer networks when [append mode](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/x-forwarded-headers.html) is enabled and client-port preservation is off. Preserve and remove modes cannot establish the client address. |
| Google Cloud external Application Load Balancer | With its [documented header format](https://cloud.google.com/load-balancing/docs/https#x-forwarded-for_header), trust both actual backend-peer ranges and the exact forwarding-rule frontend address (`/32` or `/128`). Restrict backend access to your intended load balancer; shared proxy ranges alone do not establish that boundary. Modes and paths differ, so verify your deployment rather than treating this as an automatic preset. |
| API Gateway HTTP API | No general preset: [HTTP APIs translate `X-Forwarded-*` to `Forwarded`](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-known-issues.html); Lambda event `sourceIp` requires an adapter. |

## Cloudflare

When Cloudflare connects directly to your origin, select its resolver:

```go
resolve := clientip.Cloudflare()
```

Use `resolve(r)` for each request or pass it to IAM as shown above.
`Cloudflare()` uses an embedded snapshot and performs no network I/O. Releases
must update the snapshot as Cloudflare's published ranges change. It checks
only the immediate peer; an intervening proxy is not covered. The shared
Cloudflare ranges do not authenticate your zone. Protect a directly connected
origin with zone-specific Authenticated Origin Pulls. A Cloudflare Tunnel has a
connector between Cloudflare and the application, so this direct-peer preset
does not apply; use a resolver that trusts and validates the connector's
actual forwarding behavior instead. Workers and Pseudo IPv4 settings can
change what `CF-Connecting-IP` means; review
[Cloudflare's header behavior](https://developers.cloudflare.com/fundamentals/reference/http-headers/).

For exact parsing and fallback behavior, see the
[`clientip` API](https://pkg.go.dev/arkoren.dev/core/http/clientip).
