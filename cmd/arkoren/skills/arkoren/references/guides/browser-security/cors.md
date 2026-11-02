---
title: CORS
description: Grant explicit cross-origin browser response access and handle preflights without confusing CORS with request authorization.
order: 30
summary:
  - Allow a known front end's origin, methods, and headers without treating CORS as authorization.
  - Answer preflights before authentication and control their advertised cache lifetime.
  - Enable cookie credentials and readable response metadata deliberately.
  - Distinguish public defaults, explicit empty lists, and malformed origin failures.
---

# CORS

A portal at `https://portal.example.com` cannot normally read responses from
`https://api.example.com`. Cross-Origin Resource Sharing (**CORS**) lets the API
grant that browser access with response headers. An origin consists of a scheme,
host, and optional port; changing any of them changes the origin.

CORS is a browser response-access policy, not authentication or a write barrier.
Disallowed origins can still reach your handlers without CORS permission
headers. Keep authentication, resource authorization, and
[CSRF protection](csrf-protection.md) where cookies authorize actions.

## Allow a Front End

For a portal that reads account details and sends JSON changes with a bearer
token, allow its exact origin, methods, and request headers. Import `net/http`
and `arkoren.dev/core/http/middleware/cors`. On a fresh API router `routes`,
register this before authentication, body checks, and routes:

```go
routes.Use(cors.Middleware(func(policy *cors.Builder) {
	policy.AllowedOrigins("https://portal.example.com")
	policy.AllowedMethods(http.MethodGet, http.MethodPatch)
	policy.AllowedHeaders("Content-Type", "Authorization")
}))
```

Requests with that `Origin` receive permission headers. `Authorization` must be
listed when browser code supplies a bearer token; listing it does not validate
the token. The policy applies to every route on this router, including unmatched
requests, so the portal can read eligible error responses too. Choose narrower
[group scope](../http-fundamentals/middleware.md#choose-scope) if only part of
the API shares the policy.

Use bare ASCII origins, without a trailing slash. Construction panics for
missing schemes or hostnames, userinfo, paths (including `/`), query or fragment
delimiters (including empty `?` or `#`), wildcard host patterns, and invalid or
empty ports. Decimal ports must be in `0`–`65535`; international hostnames need
ASCII punycode. Only the complete `*` wildcard is supported.

Configured origins are lowercased, not fully canonicalized. For example,
`https://portal.example.com:443` does not match a browser's
`https://portal.example.com`. Configure the spelling the browser sends in
`Origin`; this validation is not a general URL sanitizer.

## Handle Preflights

Before a cross-origin JSON `PATCH`, the browser asks whether the API permits
the method and headers. This **preflight** is an `OPTIONS` request with
`Origin` and `Access-Control-Request-Method`:

```http
OPTIONS /account/preferences HTTP/1.1
Host: api.example.com
Origin: https://portal.example.com
Access-Control-Request-Method: PATCH
Access-Control-Request-Headers: authorization, content-type
```

For an allowed origin, the middleware answers `204 No Content` without calling
the next layer. It advertises your configured method and header lists; the
browser checks those lists before sending the actual request. A `204` alone
does not mean the requested method or headers were approved.

Requests without `Origin`, requests from disallowed origins, and ordinary
`OPTIONS` requests without `Access-Control-Request-Method` continue to the next
layer. Allowed non-preflight requests also continue. CORS does not reject an
ordinary request because its method is absent from `AllowedMethods`.

Preflights arrive without credentials, so CORS must precede authentication.
Otherwise an authentication failure can stop the browser before it sends its
authenticated request. See [policy ordering](../http-fundamentals/middleware.md#order-policies).
Non-browser clients do not enforce these permission headers.

Preflight answers default to `Access-Control-Max-Age: 300`, allowing five minutes
of caching, subject to browser limits. To disable this advertised cache lifetime,
add `policy.MaxAge(0)` to the construction callback. Positive durations are
rounded down to whole seconds: `1500*time.Millisecond` sends `1`. Nonpositive
durations and positive durations below one second omit the header; import `time`
when using duration expressions.

## Send Credentials

If the portal uses session cookies instead of bearer tokens, explicitly allow
credentials for that trusted origin. Replace the previous registration with
this cookie-backed variant:

```go
routes.Use(cors.Middleware(func(policy *cors.Builder) {
	policy.AllowedOrigins("https://portal.example.com")
	policy.AllowedMethods(http.MethodGet, http.MethodPatch)
	policy.AllowedHeaders("Content-Type")
	policy.AllowCredentials(true)
}))
```

Credentials are off by default. Combining `AllowCredentials(true)` with any `*`
origin entry panics at construction. Never replace a trusted origin list with
arbitrary reflection of the request's origin.

Browser code must also opt in to credentials, for example with
`fetch(url, { credentials: "include" })`. Cookie `SameSite`, `Secure`, and browser
privacy rules still apply; CORS does not override them. Configure
[session cookies](../http-capabilities/sessions.md#enable-sessions) appropriately
and retain CSRF protection, with a [trusted origin](csrf-protection.md#trust-browser-origins)
for intentional cross-origin writes. Trusting an origin for response access neither
authenticates its visitors nor authorizes their account changes.

## Expose Metadata

If the portal displays an API request ID for support, allow script access to
that response header. This separate construction example assumes your server
already sets `X-Request-ID`:

```go
routes.Use(cors.Middleware(func(policy *cors.Builder) {
	policy.AllowedOrigins("https://portal.example.com")
	policy.ExposedHeaders("X-Request-ID")
}))
```

`ExposedHeaders` names response headers beyond the browser's basic readable
set; it does not create their values. Include the method, request-header, and
credential settings from your chosen policy when combining these examples.
With an explicit origin list, responses vary on `Origin`, including requests
without that header. Recognized allowed preflights also vary on requested
method and headers, so shared caches can distinguish permission answers.

## Refine Defaults

Use `cors.Middleware()` without configuration only when you intend public
cross-origin response access without credentials. It allows `*`, advertises
`GET`, `POST`, and `HEAD`, and allows request headers `Accept` and `Content-Type`.
No extra response headers are exposed by default.

List setters replace earlier lists. After all callbacks run, omitted or nil
origin, method, and request-header lists receive defaults; nonnil empty lists
remain empty. To grant no origins explicitly:

```go
routes.Use(cors.Middleware(func(policy *cors.Builder) {
	policy.AllowedOrigins([]string{}...)
}))
```

That omits origin permission but still passes requests onward. In contrast,
`policy.AllowedOrigins()` supplies nil and restores the wildcard default. The
same distinction applies to `AllowedMethods` and `AllowedHeaders`: explicit
empty lists omit their preflight headers. Configuration lists are copied during
construction, and validation uses the final settings after all callbacks.

For every setting, see the [`cors` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/cors).
