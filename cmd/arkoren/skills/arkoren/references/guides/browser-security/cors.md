---
title: CORS
description: Allow browser applications on other origins to call your API, with explicit origins, methods, headers, credentials, and preflight.
order: 30
---

# CORS

## Introduction

Browsers do not let a page read responses from another origin unless that
origin agrees. **Cross-Origin Resource Sharing** (CORS) is how a server agrees:
it answers with headers that name which origins, methods, and headers it
accepts. For some requests, the browser first sends a **preflight**, an
`OPTIONS` request that asks for permission, and only sends the real request
if the answer allows it.

The `cors` middleware sets these headers and answers preflights from allowed
origins before they reach your handlers. CORS controls what browser code can
read; it is not authentication or [CSRF protection](csrf-protection.md).

## Usage

Import `arkoren.dev/core/http/middleware/cors`. With an existing
[router](../http-fundamentals/routing.md) `r`, register middleware before routes.
For a public API that does not use browser credentials, the defaults allow any
origin to make simple `GET`, `HEAD`, and `POST` requests:

```go
r.Use(cors.Middleware())
```

For an API used by your own front end, replace that default policy with explicit
origins and the methods and headers it uses. Import `net/http` for method constants:

```go
r.Use(cors.Middleware(func(policy *cors.Builder) {
	policy.AllowedOrigins("https://app.example.com")
	policy.AllowedMethods(http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete)
	policy.AllowedHeaders("Accept", "Content-Type", "Authorization")
}))
```

Origins are written as `scheme://host` with an optional decimal port from `0`
through `65535`. Except for the complete wildcard `*`, each must be a bare ASCII
origin. Construction panics for missing schemes or hostnames, userinfo such as
`user:password@`, any path (even `/`), query or fragment delimiters (even empty
`?` or `#`), invalid or empty ports, and wildcard host patterns such as
`https://*.example.com`. Non-ASCII hostnames must use ASCII punycode.

Configured origins are lowercased, but not fully canonicalized like browser
URLs. In particular, default ports are not removed: `https://example.com:443`
does not match the browser spelling `https://example.com`. Use trusted origins
in the form the browser sends in `Origin`; this validation is not a URL sanitizer.

### Defaults and empty lists

Callbacks run once, in order; validation uses the final settings after all
callbacks finish. Each list setter replaces its previous list. Omitted or nil
origin, method, and request-header lists receive these defaults:

| Setting | Default |
| --- | --- |
| `AllowedOrigins` | `*` |
| `AllowedMethods` | `GET`, `POST`, `HEAD` |
| `AllowedHeaders` | `Accept`, `Content-Type` |

A nonnil empty slice stays empty. For example,
`policy.AllowedOrigins([]string{}...)` grants no origins; calling
`policy.AllowedOrigins()` passes nil and restores the default wildcard instead.
The same nil-versus-empty distinction applies to `AllowedMethods` and
`AllowedHeaders`; empty lists omit their preflight headers. `ExposedHeaders`
defaults to empty. All supplied lists are copied during construction.

> [!NOTE]
> Add `Authorization` to the allowed headers if your front end sends bearer tokens.

## Cookies and credentials

For a browser front end that relies on cookies across origins, enable
credentials for its explicit origin:

```go
cors.Middleware(func(policy *cors.Builder) {
	policy.AllowedOrigins("https://app.example.com")
	policy.AllowCredentials(true)
})
```

Credentials are off by default and cannot be combined with any `*` origin entry.
Browsers reject that combination, and the middleware panics on it, because
allowing every site to make authenticated requests on behalf of your users would
be unsafe. List your origins instead.

Install this configured middleware with `r.Use` instead of the earlier policy.
Your front end must also request credentials, for example with
`fetch(url, { credentials: "include" })`. Cookie `SameSite` and `Secure` rules
still apply; CORS does not override them or stop a cross-site form submission.

## How requests are handled

A request without an `Origin` header passes through without CORS headers.
So does a request from a disallowed origin; for a cross-origin browser request,
the missing permission headers prevent the page from reading the response,
not the handler from running.

An ordinary request from an allowed origin gets CORS headers and reaches your
handler. A preflight from an allowed origin is answered with `204 No Content`
and never reaches a handler. A preflight is an `OPTIONS` request with an
`Access-Control-Request-Method` header.

The middleware advertises your configured method and header lists; it does not
reject an ordinary request whose method is outside those lists. The browser
decides whether a preflight authorizes its request. Non-browser clients are not
restricted by CORS.

Response headers that scripts may read, beyond the basic ones, are listed with
`cors.Builder.ExposedHeaders`.

### Preflight caching

By default, the middleware sends `Access-Control-Max-Age: 300`, permitting
browsers to cache preflight answers for five minutes; browsers can impose their
own limits. Change this with `policy.MaxAge(duration)` in a configuration
callback. Positive durations are rounded down using whole-second integer
division: `1500*time.Millisecond` sends `1`. Nonpositive durations and positive
durations below one second omit the header. Explicit `MaxAge(0)` therefore
omits it rather than restoring the default. Duration expressions use `time`.

## Placement

Register CORS as one of the first middleware on the router. Preflights must be
answered before authentication runs, because browsers send them without
credentials. Since the router passes unmatched requests through its
middleware, CORS headers are also set on `404` responses, so a front end can
read those errors.

For every option, see the [`cors` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/cors).
