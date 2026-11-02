---
title: Secure Headers
description: Set browser response policies for framing, content handling, resource loading, and deliberate HTTPS persistence.
order: 20
summary:
  - Apply framing, content-sniffing, and referrer defaults before registering routes.
  - Tailor resource and capability restrictions to the pages you serve.
  - Enable persistent HTTPS policy only after checking host coverage and proxy behavior.
  - Omit proxy-owned headers without assuming omission removes existing values.
---

# Secure Headers

An account page should not become an invisible control inside another site's
frame. Security response headers tell browsers whether they may embed your
pages, how to interpret content, and which resources a page may load. The
`secureheaders` middleware supplies a shared baseline; it does not authenticate
requests or reject forged actions.

## Set Defaults

On a fresh [router](../http-fundamentals/routing.md) named `routes`, import
`arkoren.dev/core/http/middleware/secureheaders` and register the policy before
routes:

```go
routes.Use(secureheaders.Middleware())
```

At router scope, requests that reach this layer receive the configured headers,
including unmatched requests. A policy outside this layer can reject requests
before the headers are set; choose [middleware order](../http-fundamentals/middleware.md#order-policies)
accordingly.

The baseline sets:

- `X-Frame-Options: DENY` and `Content-Security-Policy: frame-ancestors 'none'`,
  forbidding other pages, including your own, from framing the response;
- `X-Content-Type-Options: nosniff`, disabling content-type guessing rather than
  fixing an incorrect [response content type](../http-fundamentals/responses.md);
- `Referrer-Policy: strict-origin-when-cross-origin`, retaining the full referrer
  for same-origin requests, sending only the origin across origins, and omitting
  it on HTTPS-to-HTTP downgrades;
- `X-XSS-Protection: 0`, disabling the legacy browser XSS auditor.

HSTS and Permissions Policy are omitted. In particular, HTTPS alone does not
enable HSTS. The default Content Security Policy forbids framing but does not
restrict scripts, styles, or images.

## Restrict Resources

If your portal serves scripts, styles, and images from its own origin, restrict
resource loading explicitly. Replace the baseline registration with this
application-specific policy:

```go
routes.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
	headers.ContentSecurityPolicy("default-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
	headers.PermissionsPolicy("camera=(), microphone=(), geolocation=()")
}))
```

The browser limits resources to the page's origin, disables object embeds, and
restricts base URLs. Inline scripts/styles and third-party resources need their
own deliberate policy; test your actual pages before deploying this example.
Permissions Policy separately disables the named browser capabilities.

`ContentSecurityPolicy` replaces the whole default string. Keep
`frame-ancestors 'none'` if framing must remain forbidden. If your own pages
must frame one another, use `frame-ancestors 'self'` and set
`headers.FrameOptions("SAMEORIGIN")` in the same construction callback so the
two framing policies agree. A resource policy reduces exposure; it does not
replace safe HTML rendering or output escaping.

## Enable HSTS

HTTP Strict Transport Security (**HSTS**) tells a browser to remember that a host
must use HTTPS for subsequent requests. Before enabling it, establish
[HTTPS service](../http-networking/server.md) and ensure the host can remain
available over HTTPS for the entire selected duration. HSTS neither configures
TLS nor redirects an initial HTTP visit.

For a host ready to commit to one day, use this alternative registration. If you
also need the resource policy above, keep those settings in the same callback:

```go
routes.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
	headers.StrictTransportSecurity("max-age=86400")
}))
```

The middleware sends this value when `request.TLS` is nonnil or
`X-Forwarded-Proto` is exactly `https`; otherwise it omits HSTS. Forwarded
protocol detection has no proxy allowlist. A TLS-terminating proxy must overwrite
client-supplied forwarding headers with the real external protocol. Browsers
ignore HSTS received over plain HTTP; the forwarding header does not turn an
HTTP connection into HTTPS.

> [!WARNING]
> Add `includeSubDomains` only if every subdomain can serve HTTPS for the policy
> lifetime, or remembered policy can make those subdomains unreachable. Add
> `preload` only after reviewing the [preload requirements and removal process](https://hstspreload.org/);
> it is a separate deployment commitment.

If you relied on the previous automatic two-year policy, preserve it explicitly
with `headers.StrictTransportSecurity("max-age=63072000")`. The current default
omits it. Omitting HSTS, or configuring an empty string, does not clear a policy
already remembered by browsers.

## Delegate Headers

If your ingress owns a particular response header, omit that header from this
middleware rather than maintaining two policies. For an ingress-owned referrer
policy, use this alternative configuration:

```go
routes.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
	headers.ReferrerPolicy("")
}))
```

An explicit empty setting stays omitted; other settings retain their defaults.
Omission does not remove a header another handler or proxy sets. Review the
final response at the browser-facing edge, not only the application response.

For all header settings, see the [`secureheaders` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/secureheaders).
