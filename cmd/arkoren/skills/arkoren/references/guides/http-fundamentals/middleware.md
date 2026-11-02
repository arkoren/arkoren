---
title: Middleware
description: Scope and order request policies, write wrappers, and preserve lifecycle behavior across native HTTP adapters.
order: 50
summary:
  - Apply middleware at router, group, or route scope with an explicit execution order.
  - Write request-local policies and register response hooks before commitment.
  - Preserve context and understand where native middleware renders returned errors.
---

# Middleware

Middleware wraps a handler to apply a policy to several requests: it can reject
input, establish request state, or observe the result. Choose both its scope and
its position. A wrapper sees only the handlers and middleware inside it, so an
incorrect order can hide failures or leave work unprotected.

These examples use `router` from `arkoren.dev/core/http/router` and a fresh
router `routes`. Call `Use` before registering routes through that router or
its derived routers; a later call panics. Configuration alternatives below are
independent, not calls to append after earlier route examples.

## Choose Scope

For behavior shared by every route, attach middleware to the router. Import
`arkoren.dev/core/http/middleware/secureheaders`:

```go
routes.Use(router.Recover(), secureheaders.Middleware())
```

Recovery protects the layers inside it; secure headers apply to the requests
that reach that middleware. At router scope, unmatched `404` and `405` requests
also traverse the stack.

For a billing-only policy, group existing `listInvoices` and `createInvoice`
handlers under `requireBilling`, your `router.Middleware`:

```go
routes.Group("/billing", func(billing *router.Router) {
	billing.Use(requireBilling)
	billing.Get("/invoices", listInvoices)
	billing.Post("/invoices", createInvoice)
})
```

The group inherits parent middleware without adding its policy to unrelated
routes. To protect one registration, use `With` instead:

```go
routes.With(requireBilling).Post("/invoices", createInvoice)
```

`With` leaves the parent's stack unchanged. All handler and middleware captures
remain shared across concurrent requests; scoping a stack does not copy captured
services or mutable state.

## Order Policies

Middleware runs in the order passed to `Use`: the first wrapper sees the
request first and the returned result last. For example, with existing
`[]errormap.Mapping` named `mappings`, import `errormap`, `requestlog`,
`bodylimit`, and `contenttype` from `arkoren.dev/core/http/middleware/`:

```go
routes.Use(
	errormap.Middleware(mappings...),
	requestlog.Middleware(),
	router.Recover(),
	secureheaders.Middleware(),
	bodylimit.Middleware(),
	contenttype.Middleware(),
)
```

The mapper sees errors from all inner layers. Logging sees recovered errors;
recovery sees panics from the policy middleware and handler. Body limits precede
anything that reads the body. An outer logger in this arrangement sees the
original error before the mapper replaces it; choose observation placement
deliberately if you need mapped errors instead.

Place [request tracing](../observability/request-tracing.md) outside logging
when logs need its trace context. Place [CORS](../browser-security/cors.md)
before authentication or body policies when preflights should terminate before
those checks. A policy outside security headers can reject a request before the
headers run, so choose that boundary consciously.

## Write Policies

A `router.Middleware` takes a handler and returns a handler. Construction runs
when the chain is built, not per request. Keep request-local values inside the
returned function. This example uses `net/http`,
`arkoren.dev/core/http/request`, and `arkoren.dev/foundation/problem`:

```go
func requireWorkspace(next router.Handler) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if request.Header(r, "X-Workspace") == "" {
			return problem.BadRequest().Derive(func(details *problem.Builder) {
				details.Detail("A workspace header is required.")
			})
		}
		return next(w, r)
	}
}
```

Register it before the routes it protects. Missing input returns a `400`
problem without calling the handler; otherwise the handler's error passes
outward. This checks presence only. Validate the workspace and enforce
[authorization](../authorization/overview.md) before using it as a trusted tenant.

A wrapper can inspect or replace the error from `next`. It cannot replace an
already committed response by returning a different error. For shared domain
translation, prefer [error mapping](error-handling.md#map-domain-errors).

## Use Response Hooks

Header edits made after `next` can be too late. To add timing at the last safe
moment, register a hook before calling the handler. Inside a middleware's
returned function, import `fmt` and `time` alongside the request package:

```go
started := time.Now()
request.Hooks(r).BeforeWriteHeader(func(w http.ResponseWriter, _ int) {
	w.Header().Set("Server-Timing",
		fmt.Sprintf("app;dur=%d", time.Since(started).Milliseconds()))
})

return next(w, r)
```

`BeforeWriteHeader` runs once before the final status, not for informational
`1xx` except `101`. A body write or flush can commit an implicit `200`.
`BeforeWrite` observes an attempted body write, which may still fail afterward.
Do not record raw body contents merely to measure writes.

For work that observes handling completion, register an after-response callback.
With an existing application metrics function `recordOutcome`:

```go
request.Hooks(r).AfterResponse(func(err error) {
	recordOutcome(err)
})
```

Callbacks run in reverse registration order when router handling ends,
including panic unwinding. Their argument is the handler's returned error, not a
delivery acknowledgment; an unrecovered panic can leave it nil. A hijacked
connection can outlive these callbacks and remains the handler's responsibility.

`request.Hooks` requires the context installed by `router.Handler.ServeHTTP`,
including normal routed requests. Outside that lifecycle it panics with
`request.ErrNoHooksMiddleware`; there is no non-panicking public lookup. Only
register hooks where wiring guarantees that lifecycle.

## Preserve Context

Middleware can resolve application state with `router.Provide`. Here
`workspaces.FromHost` validates a hostname against trusted configuration and
`withWorkspace` derives a context containing that result. Import `context`:

```go
routes.Use(router.Provide(func(w http.ResponseWriter, r *http.Request) (context.Context, error) {
	workspace, err := workspaces.FromHost(r.Context(), r.Host)
	if err != nil {
		return nil, err
	}
	return withWorkspace(r.Context(), workspace), nil
}))
```

An error stops the chain. Derive successful contexts from `r.Context()`, not
`Background` or `TODO`; replacing the parent loses cancellation, lifecycle
hooks, and upstream adapter state. The same rule applies to custom middleware
calling `next` with a changed request. Static values can use `ProvideStatic`,
but mutable captured values still need concurrency-safe ownership.

## Native Middleware

For existing `func(http.Handler) http.Handler` middleware named `compress`, use
the adapter:

```go
routes.Use(router.HTTPMiddleware(compress))
```

Creating the adapter does not call `compress`. Its native constructor runs
once per wrapping, normally during registration, not once per request or once
globally. A single `Methods` or `Any` registration shares a chain across its
methods; separate registrations build separate chains. The unmatched-request
fallback builds one additional chain on first use and reuses it. Native handlers
and captures must therefore support concurrent use.

Returned errors are rendered *inside* native middleware while output remains
uncommitted, so it can observe the error status and body. The error also returns
outward for observation. To map that HTTP response, put the mapper inside the
adapter:

```go
routes.Use(
	router.HTTPMiddleware(compress),
	errormap.Middleware(mappings...),
)
```

An outer mapper still receives the returned error but cannot rewrite the
adapter's committed output. An inner mapper sees only the layers it wraps.
A recovered error rendered before earlier output is not treated as a
post-commit panic; an actual panic after earlier output still follows the
[abort rules](error-handling.md#committed-failures).

The adapter tracks writes through `next` and commitment at enclosing native
boundaries, including wrappers hiding `Unwrap`. Output in a native wrapper's
private buffer is not visible until forwarded through a tracked boundary.
Use the supplied writer for normal output; unwrapping it to write bypasses
tracking and hooks. Use `http.ResponseController` for capability operations,
and preserve the incoming context when forwarding requests.

Individual policies belong with their capabilities: [Body Limits](body-limit.md),
[Content Types](content-type.md), [secure headers](../browser-security/secure-headers.md),
[rate limiting](../abuse-prevention/rate-limiting.md), and
[authentication](../authentication/overview.md) explain their setup and boundaries.
For composition and adapters, see the
[`router`](https://pkg.go.dev/arkoren.dev/core/http/router) and
[`request`](https://pkg.go.dev/arkoren.dev/core/http/request) API references.
