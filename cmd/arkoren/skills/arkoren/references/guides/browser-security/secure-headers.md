---
title: Secure Headers
description: Set response headers that block framing and content sniffing, then deliberately enable persistent HTTPS policy for your deployment.
order: 20
---

# Secure Headers

## Introduction

To control whether another site can frame your pages and how browsers handle
your responses, set HTTP security response headers. The `secureheaders`
middleware supplies defaults for framing, content-type guessing, and referrer
disclosure. Persistent HTTPS policy is opt-in.

Start with those defaults on your router. If you serve HTML, also tailor the
Content Security Policy to your scripts, styles, and other resources: the
default policy forbids framing but does not restrict resource loading.

## Usage

Import `arkoren.dev/core/http/middleware/secureheaders`. On an existing
[router](../http-fundamentals/routing.md) `r`, install the defaults before routes:

```go
r.Use(secureheaders.Middleware())
```

The defaults are:

- `X-Content-Type-Options: nosniff`, so browsers trust your `Content-Type`
  instead of guessing;
- `X-Frame-Options: DENY` and a `Content-Security-Policy` of
  `frame-ancestors 'none'`, so no site can frame your pages;
- `Referrer-Policy: strict-origin-when-cross-origin`, so full URLs are not
  leaked to other sites;
- `X-XSS-Protection: 0`, which turns off an outdated browser filter that
  caused more problems than it solved.

`Strict-Transport-Security` (HSTS) is omitted by default, even on HTTPS.
Applications must deliberately enable it after establishing their deployment's
HTTPS policy.

## Enabling persistent HTTPS policy

HSTS tells browsers to use HTTPS for subsequent requests to your host for the
configured `max-age` in seconds. Enable it only when the host can remain
available over HTTPS for that period. For an existing router `r`, replace the
default middleware registration with a deliberately chosen policy:

```go
r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
	headers.StrictTransportSecurity("max-age=86400")
}))
```

This policy tells browsers to use HTTPS for one day. It does not redirect HTTP
requests or configure TLS.

> [!WARNING]
> Add `includeSubDomains` only when every subdomain serves HTTPS; otherwise,
> those subdomains become unreachable to browsers that remember the policy.
> Add `preload` only after reviewing the [browser preload requirements and
> removal process](https://hstspreload.org/). Preloading is a separate commitment,
> not a routine middleware default.

`Strict-Transport-Security` is only sent on HTTPS requests, including requests
that reached a proxy over HTTPS, when `X-Forwarded-Proto` is exactly `https`.
The middleware trusts that header without a proxy allowlist. Configure your
TLS-terminating proxy to overwrite it with the actual client protocol; do not
trust arbitrary client-supplied forwarding headers.

### Migrating from the previous default

Previously, `Middleware()` sent `max-age=63072000` on HTTPS without configuration.
It now omits HSTS. If your deployment intentionally relies on that two-year
policy, preserve it explicitly with
`headers.StrictTransportSecurity("max-age=63072000")` in the construction
callback. Existing explicit policies are unchanged. Omitting the header or
setting it to `""` does not clear policy already cached by browsers.

## A content security policy

The default policy only forbids framing. An application that serves HTML
should extend it to say where scripts, styles, and images may come from, which
helps reduce cross-site scripting risk. Replace the earlier middleware with a
policy that matches your resources:

```go
r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
	headers.ContentSecurityPolicy("default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
}))
```

Keep `frame-ancestors` in the policy consistent with `X-Frame-Options`: to
allow framing by your own pages, set both, to `'self'` and `SAMEORIGIN`.

If the page must also disable browser capabilities, add
`headers.PermissionsPolicy("camera=(), microphone=(), geolocation=()")` to that
callback. Permissions Policy controls those capabilities separately from resource
loading.

## Leaving a header out

Pass an empty string to a builder setting to omit that header, for example when a
proxy in front of the service already sets it.

> [!NOTE]
> Construction starts with the recommended defaults. Setting a header to `""`
> omits it from this middleware; settings you do not change retain their defaults.
> This does not remove a header set by another handler or proxy.

For every setting, see the [`secureheaders` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/secureheaders).
