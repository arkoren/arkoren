---
title: Routing
description: Register request handlers, capture path values, group routes, and mount standard HTTP handlers.
order: 10
summary:
  - Register error-returning handlers and distinguish their response ownership from native handlers.
  - Match path parameters and organize routes with scoped middleware.
  - Mount existing HTTP handlers and inspect or test registered routes.
---

# Routing

A router connects an HTTP method and path to the handler that answers the
request. Arkoren uses Go's `http.ServeMux` matching rules and adds error-returning
handlers, route groups, and middleware. Create the router in your existing
application; [Running the Server](../http-networking/server.md) owns serving it.

## Register Handlers

Import `net/http`, `arkoren.dev/core/http/router`, and
`arkoren.dev/core/http/response`. A status endpoint can return its response
helper's error directly:

```go
routes := router.New()
routes.Get("/status", func(w http.ResponseWriter, r *http.Request) error {
	return response.String(w, http.StatusOK, "ready")
})
```

`GET /status` returns `200` with the text `ready`. An Arkoren handler returns an
error instead of writing an error response at every failure site. Before the
final response starts, the router renders that error, normally as Problem
Details. After output starts, it cannot replace the response. Keep work that can
fail before the final response helper; [Error Handling](error-handling.md) explains
status selection and client-safe detail.

Returning `nil` without a final response produces `204 No Content`. An adapted
`net/http` handler instead owns its output and defaults to `200` when it writes
nothing. `router.New` does not install panic recovery; add
[`router.Recover()`](error-handling.md#panics) before routes when needed.

Use `Post`, `Put`, `Patch`, or `Delete` for other ordinary methods. With an
existing `invoiceStatus` handler, register a chosen set together:

```go
routes.Methods([]string{http.MethodGet, http.MethodHead}, "/invoices/status", invoiceStatus)
```

`Any` covers standard methods except TRACE and CONNECT; `Method` accepts an
explicit method string. GET matching also follows the standard library's HEAD
rules. Optional route metadata describes an [OpenAPI contract](../openapi/overview.md),
not runtime decoding or validation.

Finish registration before serving requests, even for an empty router. Keep the
pointer from `New`; use `Clone`, `With`, or `Group` rather than copying a router
value. Focused fragments in these guides keep error results visible but omit
routine propagation; complete handlers must handle errors before using results.

## Capture Parameters

A wildcard captures one path segment. With your existing `showInvoice` handler:

```go
routes.Get("/invoices/{id}", showInvoice)
```

Inside that handler, use `r.PathValue("id")` for a string. For a numeric ID,
import `arkoren.dev/core/http/request` and parse before looking anything up:

```go
id, err := request.Param[int64](r, "id")
if err != nil {
	return err
}
```

A malformed or missing value returns a `400` problem. Parsing establishes a
value, not permission to access the invoice; authorize the resource separately.
[Requests](requests.md) covers query and body inputs.

A final wildcard such as `/assets/{path...}` captures the remaining path,
including slashes. Treat captured paths as untrusted input, not safe filesystem
paths. A catch-all also serves its parent path unless an explicit parent route
already exists; register that parent first.

Non-root routes match both trailing-slash forms. The root `/` matches only the
root. A literal route such as `/invoices/new` can coexist with
`/invoices/{id}` because it is more specific. Duplicate or ambiguous patterns
panic during registration; patterns containing `..` segments are rejected too.

## Group Routes

A group gives related handlers a common prefix and middleware scope. Here
`requireBilling` is your `router.Middleware`, and `listInvoices` and
`showInvoice` are existing `router.Handler` functions:

```go
routes.Group("/billing", func(billing *router.Router) {
	billing.Use(requireBilling)
	billing.Get("/invoices", listInvoices)
	billing.Get("/invoices/{id}", showInvoice)
})
```

Both routes run through `requireBilling`; routes outside the group do not.
`With(requireBilling).Get(...)` scopes extra middleware to one registration.
`Grouped` scopes middleware without adding a prefix, while `Clone` returns a
sub-router with an independent middleware stack and the same route registry.

Call `Use` before registering routes through that router or any derived router.
A later `Use` panics rather than silently leaving earlier routes unprotected.
Sub-router middleware does not alter its parent's stack. See
[Middleware](middleware.md) for ordering and shared-state rules.

## Unmatched Requests

A missing path produces a `404` problem. A matching path with an unsupported
method produces a `405` problem with `Allow`. Both pass through the served
router's middleware, so rejection responses can carry security and CORS headers.
The fallback middleware chain is built on its first request and reused; its
captured values must support concurrent requests.

## Mount Native Handlers

For an existing `http.Handler` named `metrics`, adapt one endpoint:

```go
routes.Get("/metrics", router.HTTP(metrics))
```

The native handler writes its own errors: there is no returned error for the
router to render. Native middleware has a separate
[adapter boundary](middleware.md#native-middleware).

If an existing handler `tools` has its own paths, mount the entire handler:

```go
routes.Mount("/tools", tools)
```

The exact prefix and everything below it reach `tools`, with the prefix removed:
`/tools` and `/tools/` become `/`, and `/tools/jobs` becomes `/jobs`. Mounts
inherit middleware and cover the same methods as `Any`.

The local mount prefix must contain literal segments, not wildcards. Parent
groups may contain single-segment wildcards; those path values remain available
to the handler. Mounting strips the whole group prefix and preserves query
parameters and escaped path-segment boundaries. It copies the request and URL,
not the body or mutable context values. A mount remains an opaque catch-all for
OpenAPI: omit operation metadata or mark it hidden.

## Inspect Routes

`Routes` returns a detached eager collection in registration order, including
registrations from the whole router tree. To inspect only GET registrations:

```go
gets := routes.Routes().Filter(func(_ int, route router.Route) bool {
	return route.Method == http.MethodGet
})
```

The snapshot does not change with later registrations, and editing it cannot
change dispatch. Implicit trailing-slash variants are not separate entries.
Handler names and source locations can disclose application layout; restrict
access to any published inventory.

To exercise dispatch and middleware without starting a server, import
`net/http/httptest`:

```go
res := routes.Record(httptest.NewRequest(http.MethodGet, "/status", nil))
defer res.Body.Close()
```

You own the returned body. [HTTP Tests](../testing/http-tests.md) shows assertions;
`Record` tests the in-memory pipeline, not network delivery.

For all registration and lookup methods, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
