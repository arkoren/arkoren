---
title: Routing
description: Register routes and error-returning handlers, use wildcards, group routes under a prefix or middleware, and mount net/http handlers.
order: 10
---

# Routing

## Introduction

The router decides which handler answers a request. Arkoren's router is built
on Go's own `http.ServeMux`, so its patterns, wildcards, and matching rules are
the ones you may already know from the standard library. What it adds is a
handler type that returns an error, middleware, route groups, and consistent
error responses, including for requests that match no route at all.

## Defining routes

Import `arkoren.dev/core/http/router`, `arkoren.dev/core/http/response`, and
`net/http`. Create a router with `router.New` and register a handler for a method
and a path:

```go
r := router.New()

r.Get("/", func(w http.ResponseWriter, r *http.Request) error {
	return response.String(w, http.StatusOK, "Hello")
})
```

A GET to `/` returns `200` and `Hello`. The later examples assume existing
application handlers (`status`, `serveFile`, `stats`, `users`, and `login`) of type
`router.Handler`, and application middleware `requireAdmin` of type
`router.Middleware`. Configuration variants use a fresh router: do not add `Use`
after an earlier example registered a route. Focused fragments retain errors
but omit routine checks; handle them before using results.

There is a method for every HTTP verb: `Get`, `Post`, `Put`, `Patch`,
`Delete`, `Head`, `Options`, and the rarer ones. `Any` registers a handler
for all of the usual methods at once, and `Methods` for a list you choose:

```go
r.Methods([]string{http.MethodGet, http.MethodHead}, "/status", status)
```

A router is an `http.Handler`, so you can serve it with Arkoren's
[server](../http-networking/server.md) or with any other.
Finish route and middleware registration before serving requests, including on
routers with no registered routes. Keep the pointer returned by `router.New`;
use `Clone`, `With`, or `Group` for scopes instead of copying a router value.

To publish a machine-readable API contract, add an optional trailing operation
callback to a route registration. The [OpenAPI overview](../openapi/overview.md) shows how to
describe requests and responses, export the document, and serve Swagger UI.
Documentation does not change handler behavior; unannotated routes still work
but are omitted from the document.

### Inspecting registered routes

To check a route inventory before serving, call `Routes` on your existing
router `r`. It returns a `collection.Slice[router.Route]`, so you can filter
registrations without building a separate collection:

```go
gets := r.Routes().Filter(func(_ int, route router.Route) bool {
	return route.Method == http.MethodGet
})
byMethod := r.Routes().GroupBy(func(_ int, route router.Route) string {
	return route.Method
})
```

`gets` preserves registration order; `byMethod` preserves that order within
each group. Inspection includes the whole router tree, even from a child
router. Finish registration before inspecting concurrently. The result is a
detached eager snapshot, not a live query: later registrations cannot change it,
and edits cannot change the router. The collection wrapper adds operations, not
ownership; `Routes` supplies the copy. Names and source locations can disclose
application layout, so restrict access to published inventories.

### Handlers return errors

An Arkoren handler has the signature of a `net/http` handler plus an `error`
result:

```go
func(w http.ResponseWriter, r *http.Request) error
```

When a handler cannot do its job, it returns the error instead of writing an
error response. If the response has not been committed, the router renders the
error as Problem Details by default. This keeps error handling in one place;
[Error handling](error-handling.md) explains status selection, custom rendering,
and what changes after output has started.

A handler that returns `nil` without writing anything answers
`204 No Content`. This applies to Arkoren's error-returning handlers, not
ordinary `net/http` handlers: an adapted standard handler that writes nothing
answers `200 OK`.

## Route parameters

Patterns can contain **wildcards** in braces. A wildcard matches one path
segment, and you read its value with `r.PathValue`:

For a route registered as `/users/{id}`, read the captured value inside its
handler:

```go
id := r.PathValue("id")
```

When a parameter must be a number, `request.Param[int]` parses it and returns a
`400 Bad Request` problem for anything else. Return that error before using the
parsed value. Go integer types except `uintptr`, floating-point types, booleans,
and strings work, including named types such as `type UserID int64`:

```go
id, err := request.Param[int](r, "id") // arkoren.dev/core/http/request
```

A wildcard that ends in `...` matches the rest of the path, including
slashes. It is useful for file paths:

```go
r.Get("/files/{path...}", serveFile)
```

[Requests](requests.md) covers every way to read parameters, query strings,
headers, and bodies.

### Trailing slashes

Every route except `/` matches with and without a trailing slash, so
`/users/42` and `/users/42/` reach the same handler. You do not need to
register both.

### Conflicting routes

Routes conflict when neither pattern is more specific than the other for
requests they both match, or when a pattern is registered twice. A more specific
route such as `/users/new` can coexist with `/users/{id}`.
Registering a conflicting route panics with a message that names both patterns. Because routes are
registered while the service starts, a conflict shows up the first time you
run it.

## Route groups

A **group** registers several routes under a common path prefix. The function
you pass receives a sub-router whose routes are all prefixed:

```go
r.Group("/admin", func(admin *router.Router) {
	admin.Get("/stats", stats) // GET /admin/stats
	admin.Get("/users", users) // GET /admin/users
})
```

Groups are most useful together with middleware: middleware added inside a
group applies only to that group's routes:

```go
r.Group("/admin", func(admin *router.Router) {
	admin.Use(requireAdmin)
	admin.Get("/stats", stats)
})
```

## Middleware on routes

Middleware wraps handlers to run code before or after them, such as
authentication, logging, or rate limiting. Add middleware for every route of a
router with `Use`:

```go
r := router.New()
r.Use(router.Recover())
r.Use(secureheaders.Middleware()) // arkoren.dev/core/http/middleware/secureheaders
```

`Use` must come before the routes it should wrap, and it panics if routes were
already registered, because those routes would silently skip it. To apply
middleware to a single route, use `With`, which returns a sub-router with the
extra middleware and leaves the original router unchanged. The following
limiter uses `arkoren.dev/core/http/middleware/ratelimit` and an existing
`*cache.Cache` named `store`; follow [rate limiting](../abuse-prevention/rate-limiting.md)
for cache setup and key selection:

```go
r.With(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.Limit(5)
})).Post("/login", login)
```

[Middleware](middleware.md) explains how middleware works and
describes each one that ships with Arkoren.

### Recovering from panics

`router.New` does not install panic recovery for you. Before registering routes,
add `router.Recover()` outside the handlers and middleware it must protect. An
ordinary panic before the response is committed then follows normal error
rendering, usually a generic `500 Internal Server Error`:

```go
r.Use(router.Recover())
```

If you also use request logging or telemetry, put them outside recovery so they
can observe the returned error. Follow the recommended
[middleware order](middleware.md#ordering). Recovery cannot replace an already
committed response; [panic handling](error-handling.md#panics) explains the abort
behavior and custom conversion, including a converter that returns nil.

## Requests that match no route

When no route matches the path, the router answers `404 Not Found`. When the
path matches but the method does not, it answers `405 Method Not Allowed`
with an `Allow` header. Both are rendered as Problem Details, like any other
error, and both pass through the router's middleware, so CORS and security
headers are applied to them too.

The served router builds a separate fallback middleware chain on the first such
request and reuses it for later `404` and `405` responses. It does not rebuild
that chain for each unmatched request. Keep captured middleware state safe for
concurrent requests, just as for registered routes.

## Mounting standard handlers

Many libraries provide a plain `http.Handler`, such as a metrics endpoint or a
file server. Wrap it with `router.HTTP` to register it like any other route:

```go
r.Get("/metrics", router.HTTP(metrics))
```

Here `metrics` is an existing `http.Handler`. It writes its own errors because a
standard handler has no error result to return to the router.

If a library instead provides middleware in the form
`func(http.Handler) http.Handler`, adapt it with `router.HTTPMiddleware`.
Here `gzip` is your existing standard-library-compatible middleware. On a fresh
router, attach it before registering routes:

```go
r.Use(router.HTTPMiddleware(gzip))
```

The native constructor runs once per wrapping, not per request or globally once
per router. See [standard HTTP middleware](middleware.md#using-nethttp-middleware)
for chain lifetime, context preservation, and error-rendering boundaries.

To serve a whole `http.Handler` under a prefix, such as an admin tool with its own
routes, use `Mount`. Here `handler` is an existing `http.Handler` written for `/`.
The mount serves the exact prefix and everything below it, stripping the prefix
before calling the handler:

```go
r.Mount("/tools", handler)
// GET /tools/       reaches the handler as GET /
// POST /tools/a/b   reaches the handler as POST /a/b
```

A mounted handler is an ordinary route: it runs inside the router's
middleware, so authentication and recovery apply, and `Routes` lists it once
per method with the pattern `/tools/{path...}`. Inside `Group`, the whole
group pattern is stripped. The prefix passed to `Mount` must contain literal
segments: a local `{name}` or `{path...}` wildcard panics. Parent groups can
contain single-segment named wildcards, whose values remain available on the
request passed to the mounted handler:

```go
r.Group("/teams/{team}", func(teams *router.Router) {
	teams.Mount("/files", handler)
})
```

For `GET /teams/a%2Fb/files/report%2Ffinal`, the mounted request has
`PathValue("team") == "a/b"`, `URL.Path == "/report/final"`, and
`URL.RawPath == "/report%2Ffinal"`. Escaped slashes stay within their segments
for a nested `http.ServeMux`. Rewriting uses a copy of the request and URL,
leaves the original URL unchanged, and retains the query. It does not clone the
body or values stored in the context.

Mount covers the same methods as `Any`, excluding TRACE and CONNECT. Like any
route, a `Mount` after `Use` is fine, but `Use` after a `Mount` panics.

## Several routers

Each router is independent. To serve an internal API on a different port, for
example, create a second router and serve it with a second server. The
 [Server](../http-networking/server.md) page shows how.

## Testing routes

`Record` sends a request through the router, including all its middleware, and
returns the response, without starting a server. You own the returned response
body and must close it. Import `net/http/httptest`:

```go
res := r.Record(httptest.NewRequest(http.MethodGet, "/users/42", nil))
defer res.Body.Close()
```

[HTTP tests](../testing/http-tests.md) builds on this.

For every router method and option, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
