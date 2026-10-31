---
title: Middleware
description: Apply middleware to routers, groups, and routes, choose a safe order, and write your own when the shipped middleware does not fit.
order: 50
---

# Middleware

## Introduction

**Middleware** is code that runs around a handler. It sees each request before
the handler does and each result after it, which makes it the place for
concerns that apply to many routes at once: security headers, authentication,
rate limiting, logging, body limits. A middleware can let the request through,
change it, add to the response, or stop the request with an error before the
handler runs.

The order matters: middleware can observe or protect only the handlers and
middleware inside it. Start by choosing its scope—router, group, or route—then
place it in the chain. Arkoren's shipped middleware is documented alongside
the capability it provides.

## Applying middleware

There are three places to apply middleware, from widest to narrowest.

The examples use an existing `routes := router.New()` from
`arkoren.dev/core/http/router`. Import shipped middleware from
`arkoren.dev/core/http/middleware/<name>`; `request` is
`arkoren.dev/core/http/request`, and `problem` is
`arkoren.dev/foundation/problem`. Call `Use` before registering routes: it
panics after this router or a derived router has registered a route.
Registration examples are independent; start each with a fresh router rather
than applying later `Use` examples after earlier route registrations.

**On a router**, with `Use`, it wraps every route registered after it. Use it
for what every route needs:

```go
routes.Use(router.Recover(), secureheaders.Middleware())
```

`Recover` converts a handler panic into an error response, and `secureheaders`
adds protective response headers. Register routes after this call.

**On a group**, inside `Group`, it wraps only that group's routes.

Here `requireAdmin` is your existing `router.Middleware`, and `stats` is your
existing `router.Handler`:

```go
routes.Group("/admin", func(admin *router.Router) {
	admin.Use(requireAdmin)
	admin.Get("/stats", stats)
})
```

**On one route**, with `With`, it wraps a single registration and leaves the
router unchanged. With an existing `store` of type `*cache.Cache` from
`arkoren.dev/core/service/cache` and a `login` handler, limit that route to five
requests per default one-minute window; see
[rate limiting](../abuse-prevention/rate-limiting.md) for shared-store setup and client keys:

```go
routes.With(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.Limit(5)
})).Post("/login", login)
```

## Ordering

Middleware passed to `Use` runs in the order given: the first one is the
outermost and sees the request first and the result last. Order matters,
because a middleware can only observe what happens inside it. A good default
order for an API appears below.

Here `mappings` is your `[]errormap.Mapping`, `policies` is your
`[]cors.BuilderFunc`, and `tel` is your `*telemetry.Telemetry` from
`arkoren.dev/core/service/telemetry`. Configure these through
[error mapping](../http-fundamentals/error-handling.md#mapping-errors-to-responses),
[CORS](../browser-security/cors.md), and [request tracing](../observability/request-tracing.md):

```go
routes.Use(
	errormap.Middleware(mappings...), // sees every error from the middleware below
	tel.Middleware(),                 // traces the whole request (tel is a *telemetry.Telemetry)
	requestlog.Middleware(),          // logs failures, with trace IDs
	router.Recover(),                 // turns panics into errors the above can see
	secureheaders.Middleware(),
	cors.Middleware(policies...),
	bodylimit.Middleware(),
	contenttype.Middleware(),
)
```

The general rules behind it: error mapping goes first so it sees every error;
observability comes next so it measures everything inside it; `Recover` comes
after logging so a recovered panic is logged as a failure; and middleware that
rejects requests early, such as CORS preflights and size limits, comes before
the handlers it protects.

> [!NOTE]
> Requests that match no route still pass through the router's middleware, so
> a `404` or `405` response carries security and CORS headers too.

## Writing middleware

Write your own middleware when an application-specific concern does not fit a
shipped one. A middleware takes a handler and returns a handler that wraps it.
The wrapping function runs when the chain is built, not for every request. Keep
request-local data inside the returned handler; captured state is shared across
concurrent requests and must be safe for that use.

The following functions use `net/http`; handler parameter `r` is the incoming
request, not the router. This example checks presence only; it does not authorize
access to a tenant. Enforce that with [authorization](../authorization/overview.md):

```go
func requireTenant(next router.Handler) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if request.Header(r, "X-Tenant") == "" {
			return problem.BadRequest().Derive(func(details *problem.Builder) {
				details.Detail("A tenant header is required.")
			})
		}

		return next(w, r)
	}
}
```

Register it with `routes.Use(requireTenant)` before registering routes. A request
without the header receives a `400` Problem Details response; otherwise the
wrapped handler runs.

Because handlers return errors, so does middleware. A middleware that stops
a request returns an error, which the router renders like any other. A
middleware that calls `next` receives the handler's error and can inspect it,
replace it, or pass it on, which is how [error mapping](../http-fundamentals/error-handling.md#mapping-errors-to-responses)
works.

### Acting on the response

Middleware that needs to change ordinary response headers must do so before the
handler writes the final status. Register a hook for that moment with `request.Hooks`.
This function also imports `fmt` and `time`; serve it through the Arkoren router,
which installs the request hook registry:

```go
func serverTiming(next router.Handler) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		start := time.Now()
		request.Hooks(r).BeforeWriteHeader(func(w http.ResponseWriter, _ int) {
			w.Header().Set("Server-Timing", fmt.Sprintf("app;dur=%d", time.Since(start).Milliseconds()))
		})

		return next(w, r)
	}
}
```

`BeforeWriteHeader` runs for the final status, not for informational responses
such as `103 Early Hints`; `101 Switching Protocols` counts as final.
`AfterResponse` runs when router handling finishes, including while unwinding a
panic, and receives the returned handler error. It suits logging and metrics,
but is not proof that the client received the response. An unrecovered panic can
leave that error nil.

### Passing values to handlers

Middleware often resolves something that handlers need, such as the current
tenant. `router.Provide` turns a function that returns a derived context into
middleware. Derive it from the incoming `r.Context()`, not `context.Background()`
or `context.TODO()`: replacing the context would discard cancellation, lifecycle
hooks, and state used by outer middleware and adapters. The same requirement
applies when your own middleware forwards a request with a changed context.

Here `tenants.FromHost` and `withTenant` are application functions: the former
validates the hostname against trusted tenant configuration, and the latter
derives a context containing the tenant from its supplied parent. Import
`context` and map domain errors with outer
[error-mapping middleware](../http-fundamentals/error-handling.md#mapping-errors-to-responses)
before registering routes:

```go
routes.Use(router.Provide(func(w http.ResponseWriter, r *http.Request) (context.Context, error) {
	tenant, err := tenants.FromHost(r.Context(), r.Host)
	if err != nil {
		return nil, err
	}

	return withTenant(r.Context(), tenant), nil
}))
```

Handlers read the value from the incoming request's context. Returning an error
stops the request.

## Using `net/http` middleware

Middleware written for the standard library, in the form
`func(http.Handler) http.Handler`, works with `router.HTTPMiddleware`.

With an existing standard-library-compatible middleware named `gzip`, attach it
before registering routes:

```go
routes.Use(router.HTTPMiddleware(gzip))
```

`HTTPMiddleware(gzip)` creates an adapter; it does not call `gzip` immediately.
The native `gzip(next)` constructor runs once each time the adapter wraps a
handler, not globally once per router. For registered routes, this happens during
registration. One `Methods` or `Any` registration shares its chain across those
methods; separate registrations build separate chains. The
[unmatched-request fallback](routing.md#requests-that-match-no-route) has one
additional chain, built on its first request and reused afterward. Native
handlers and their captures must support concurrent requests.

Returned errors are rendered inside the native middleware when the response is
still unwritten, so it can observe the error status and body. They are also
returned outward. A recovered error rendered there can be wrapped without losing
its `errors.Is` or `errors.As` behavior; it does not cause an abort merely because
the adapter has now written the problem. A panic after earlier output remains a
[post-commit failure](error-handling.md#after-the-response-has-started).

For errors rendered inside native middleware, register `HTTPMiddleware(gzip)`
before the error mapper that must choose that response. This puts the mapper
inside the adapter, so mapping happens before rendering. An outer mapper still
receives returned errors, but cannot rewrite a response already written by the
adapter; the mapper inside can only see failures from the layers it wraps.

The adapter tracks writes through `next` and commitment observed at enclosing
native boundaries, including through wrappers that hide `Unwrap`. It cannot see
output written only to a middleware's private buffer before calling `next` until
the middleware forwards it through a tracked boundary. Write normal HTTP output
through the supplied writer: writing directly to an unwrapped writer bypasses
hooks and commitment tracking. Use `http.ResponseController` for native capability
operations instead, and preserve the incoming request context when calling `next`.

## Middleware in Arkoren

These pages describe the general-purpose middleware:

- [Body limit](body-limit.md) caps the size of request bodies.
- [Content type](content-type.md) rejects bodies of the wrong media type.
- [CORS](../browser-security/cors.md) lets browsers on other origins call your API.
- [CSRF protection](../browser-security/csrf-protection.md) stops other sites from sending requests
  as your signed-in users.
- [Locale](../http-capabilities/locale.md) picks the client's language for messages.
- [Rate limiting](../abuse-prevention/rate-limiting.md) limits how often a client can call a route.
- [Request logging](../observability/request-logging.md) logs failed requests.
- [Secure headers](../browser-security/secure-headers.md) sets protective response headers.

Other middleware is described with the feature it belongs to:
[CAPTCHA](../abuse-prevention/captcha.md) (vendor-local token verification for selected routes),
[sessions](../http-capabilities/sessions.md), [secure links](../http-capabilities/secure-links.md),
[error mapping](../http-fundamentals/error-handling.md#mapping-errors-to-responses),
[authentication](../authentication/overview.md),
[authorization](../authorization/overview.md), and
[telemetry](../observability/request-tracing.md).

For every router method and middleware adapter, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
