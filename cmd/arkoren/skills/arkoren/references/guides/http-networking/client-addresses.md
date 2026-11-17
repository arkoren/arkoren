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
do not group IPv6 addresses by `/64`. Use the
[rate-limiting guide](../abuse-prevention/rate-limiting.md) for bucket policy.

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

To use this resolver for IAM's abuse limits, add it to your existing
construction callback:

```go
identity.ClientIP(resolve)
```

See [IAM Overview](../iam/overview.md) for construction. Supplying a resolver
is optional, and IAM does not import this package on your behalf.

## Deployment Choices

| Path to the application | Choice and boundary |
| --- | --- |
| Direct connection | `Direct()` ignores forwarded headers. |
| Generic reverse proxy | `Proxies` with the actual isolated peer networks; each trusted hop must append or replace the header. |
| Cloudflare directly to origin | `Cloudflare()` uses `CF-Connecting-IP` only from its embedded Cloudflare peer ranges. |
| AWS Application Load Balancer | Use `Proxies` with actual peer networks when [append mode](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/x-forwarded-headers.html) is enabled and client-port preservation is off. Preserve and remove modes cannot establish the client address. |
| Google Cloud external Application Load Balancer | With its [documented header format](https://cloud.google.com/load-balancing/docs/https#x-forwarded-for_header), trust both actual backend-peer ranges and the exact forwarding-rule frontend address (`/32` or `/128`). Restrict backend access to your intended load balancer; shared proxy ranges alone do not establish that boundary. Modes and paths differ, so verify your deployment rather than treating this as an automatic preset. |
| API Gateway HTTP API | No general preset: [HTTP APIs translate `X-Forwarded-*` to `Forwarded`](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-known-issues.html); Lambda event `sourceIp` requires an adapter. |

`Cloudflare()` uses an embedded snapshot and performs no network I/O. Releases
must update the snapshot as Cloudflare's published ranges change. It checks
only the immediate peer; an intervening proxy is not covered. The shared
Cloudflare ranges do not authenticate your zone. Protect the origin with
zone-specific authenticated origin pulls or a Cloudflare Tunnel. Workers and
Pseudo IPv4 settings can change what `CF-Connecting-IP` means; review
[Cloudflare's header behavior](https://developers.cloudflare.com/fundamentals/reference/http-headers/).

For exact parsing and fallback behavior, see the
[`clientip` API](https://pkg.go.dev/arkoren.dev/core/http/clientip).
