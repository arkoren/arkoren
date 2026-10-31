---
title: Body Size Limit
nav: Body Limit
description: Cap the size of request bodies for a router or a group, before the handler reads them, and allow larger uploads only where you need them.
order: 60
---

# Body Size Limit

## Introduction

A request body can be as large as a client wants to make it. Without a limit,
one request could fill the server's memory or disk. The `request` body helpers
already stop reading after 10 MiB, but a single limit for every route is rarely
right: an API that accepts small JSON documents should refuse a 9 MiB body,
while an upload route may need much more.

The `bodylimit` middleware sets the limit for a whole router or group.

## Setting a limit

Import `arkoren.dev/core/http/middleware/bodylimit`. With your existing
[router](../http-fundamentals/routing.md) `r`, add the middleware before routes,
with the limit in bytes:

```go
r.Use(bodylimit.Middleware(func(limit *bodylimit.Builder) {
	limit.Limit(1 << 20)
})) // 1 MiB
```

Without `Builder.Limit`, it uses the same 10 MiB default as the body helpers.
Zero or negative limits also select that default; they do not disable the cap.

## How the limit is enforced

The middleware checks the limit in two places. When a request declares its
size with `Content-Length` and that size is over the limit, the request is
rejected with `413 Content Too Large` before the handler runs. Otherwise, the
middleware wraps the body so that reading past the limit fails. This also
covers requests without a declared size, such as chunked uploads. The body
helpers turn that read failure into the same `413` problem; code that reads
`r.Body` directly must handle the native `*http.MaxBytesError` itself. Returning
that raw error does not give it the helper's HTTP problem identity. The streaming
limit is discovered only if downstream code reads far enough; the middleware
does not pre-read the body. This bounds input bytes, not decoded memory, disk
usage from later processing, or application work.

The limit is also stored in the request context, where every body helper finds
it and uses it instead of its default.

### Empty bodies and request ownership

A nil `r.Body` is empty input, not a panic, including when composed with the
[request helpers](requests.md#xml-and-raw-bodies). `Bytes` and `String` return no
data; JSON and XML decoding return their existing 400 malformed-body problems.
The declared-length check still runs first: even a nil body with a
`Content-Length` above the limit is rejected with 413 before the handler runs.

The middleware installs its context and body wrapper on a shallow request copy;
it does not replace the caller's body or context. It neither reads nor closes
the body itself. A non-nil body stream is still shared: downstream reads consume
it, and closing the wrapped body closes the original stream.

Return input problems before committing a response. If the handler has already
written a final status or body, a later body-read failure cannot replace it with
a 413 or 400 problem. Existing HTTP problem identities and operational failures
keep the [request helpers' error boundaries](requests.md#json-bodies).

## Larger limits for some routes

The limit is a ceiling. Inside it, `request.MaxSize` can lower the limit for a
call, but neither `MaxSize`, `request.Unlimited()`, nor a second `bodylimit`
middleware can raise it. This is deliberate: a limit set for the whole router
cannot be bypassed by one careless handler.

To accept larger bodies on some routes, use a fresh router without the global
limit above and apply limits per group. Import `arkoren.dev/core/http/router`;
`createPost` and `upload` are your existing `router.Handler` functions:

```go
r.Group("/api", func(api *router.Router) {
	api.Use(bodylimit.Middleware(func(limit *bodylimit.Builder) {
		limit.Limit(1 << 20)
	}))
	api.Post("/posts", createPost)
})

r.Group("/uploads", func(uploads *router.Router) {
	uploads.Use(bodylimit.Middleware(func(limit *bodylimit.Builder) {
		limit.Limit(100 << 20)
	}))
	uploads.Post("/", upload)
})
```

Register the middleware before any middleware that reads the body.

A [webhook receiver](../http-capabilities/webhooks.md#limiting-the-body-size) has its own size limit; this middleware stays a ceiling above it.

For every option, see the [`bodylimit` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/bodylimit).
