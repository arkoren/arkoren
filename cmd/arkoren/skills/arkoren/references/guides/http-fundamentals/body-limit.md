---
title: Body Limits
description: Set request-body ceilings before parsing and scope larger inputs without weakening ordinary endpoints.
order: 60
summary:
  - Cap declared and streamed body bytes before request parsing.
  - Give larger endpoints separate scopes rather than trying to override an outer ceiling.
  - Distinguish middleware defaults, per-call limits, and raw-reader failure handling.
---

# Body Limits

A body cap bounds how many input bytes a request can make your application
read. Choose a ceiling for the endpoint before decoding: a small invoice JSON
request should not inherit the space allowance needed by an upload route.
The `bodylimit` middleware applies that ceiling before handlers and other
body-reading middleware.

## Set a Ceiling

With a fresh [router](routing.md) named `routes`, import
`arkoren.dev/core/http/middleware/bodylimit` and allow at most 64 KiB:

```go
routes.Use(bodylimit.Middleware(func(limit *bodylimit.Builder) {
	limit.Limit(64 << 10)
}))
```

Register routes afterward. Without a configured limit, the middleware uses
10 MiB, matching the request body helpers' default. Zero and negative
middleware limits also select 10 MiB; they do not disable the cap or require
an empty body.

The limit bounds input bytes, not decoded memory, temporary-file usage from
later processing, or application work. Use validation and processing limits
where decoded input can expand or trigger expensive operations.

## Handle Rejections

If `Content-Length` exceeds the ceiling, the middleware returns
`request.ErrBodyTooLarge` (`413`) before calling the next layer. Otherwise it
wraps the body with `http.MaxBytesReader`: unknown-length or understated bodies
fail only when downstream code reads far enough. The middleware does not
pre-read or verify an unread remainder.

The [request helpers](requests.md#decode-json) convert a native limit-read error
to the same `413` problem. If your code reads `r.Body` directly, handle
`*http.MaxBytesError` yourself. In a raw-reader fragment using `io`, `errors`,
`net/http`, and `arkoren.dev/core/http/request`:

```go
data, err := io.ReadAll(r.Body)
var tooLarge *http.MaxBytesError
if errors.As(err, &tooLarge) {
	return request.ErrBodyTooLarge
}
```

Other read failures remain errors to handle before using `data`. Returning the
raw `MaxBytesError` alone does not give it the helper's HTTP problem identity.
Return parsing and size errors before committing a response; after output
starts, neither a `413` nor a malformed-input problem can replace it.

A nil body is empty input, including with request helpers: raw reads return
no data, while JSON and XML return their malformed-body problems. The
declared-length check still runs first, even for a nil body.

Middleware puts the cap in context and replaces the body only on a shallow
request copy. It does not replace the caller's context/body fields, read the
body, or close it. The underlying non-nil stream remains shared: downstream
reads consume it, and closing the wrapper closes that stream.

## Scope Larger Inputs

An outer ceiling cannot be raised by another `bodylimit` middleware,
`request.MaxSize`, or `request.Unlimited`. Put larger routes outside the
smaller scope from the start. On a fresh router without the global cap above,
with existing `createInvoice` and `uploadAttachment` handlers:

```go
routes.Group("/invoices", func(invoices *router.Router) {
	invoices.Use(bodylimit.Middleware(func(limit *bodylimit.Builder) {
		limit.Limit(64 << 10)
	}))
	invoices.Post("/", createInvoice)
})
```

The ordinary endpoint now has its own cap. Add the upload as a separate scope,
not a child of that group:

```go
routes.With(bodylimit.Middleware(func(limit *bodylimit.Builder) {
	limit.Limit(12 << 20)
})).Post("/attachments", uploadAttachment)
```

That route allows 12 MiB, subject to any additional parser limit. Apply the
middleware before anything that reads the body. A webhook receiver also has
its own [body policy](../webhooks/delivery-policies.md#body-limits); any surrounding
middleware remains a ceiling above it.

## Tighten One Read

Inside a cap, a handler can ask its decoder to accept less. With an existing
`CreateInvoice` input struct:

```go
input, err := request.JSON[CreateInvoice](r, request.MaxSize(16<<10))
```

`MaxSize(0)` permits only empty input, unlike middleware `Limit(0)`.
A negative per-call size selects the 10 MiB default. Options apply in order;
a later size option replaces an earlier per-call setting, but no option
removes the upstream reader's cap.

`Unlimited()` removes the helper's own cap and ignores a stored context limit,
not a ceiling already installed on the body. Use it only when memory use is
bounded another way; uncapped raw reads let a hostile client exhaust memory.

For configuration and per-call options, see the
[`bodylimit`](https://pkg.go.dev/arkoren.dev/core/http/middleware/bodylimit) and
[`request`](https://pkg.go.dev/arkoren.dev/core/http/request) API references.
