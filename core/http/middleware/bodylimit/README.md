# Body limit

`arkoren.dev/core/http/middleware/bodylimit` caps request body sizes for a router or group.

On an existing `r` router, limit uploads to 1 MiB:

```go
r.Use(bodylimit.Middleware(func(limiter *bodylimit.Builder) {
	limiter.Limit(1 << 20)
})) // 1 MiB
```

A declared `Content-Length` over the limit gets 413 before the handler runs; streamed bodies fail
the read once past the limit. The request body helpers (`request.JSON`, `request.Bytes`, ...) use
this limit instead of their 10 MiB default.

Without a callback, `Middleware()` uses `request.DefaultMaxBodySize`.
Zero or negative `Limit` values also select that default; they do not disable the ceiling.
The wrapped body is a hard ceiling: request options and nested middleware cannot
raise it. Mount routes that accept larger bodies outside this middleware.

A nil `Body` is empty input, so `request.JSON` returns `request.ErrInvalidJSON` (400).
The declared `Content-Length` is still checked first.
The middleware replaces the context and body on a request copy, leaving the caller's
request fields unchanged. A non-nil body stream remains shared: downstream reads
consume it, and closing the wrapped body closes the original stream.

See the [Body Limit guide](../../../../cmd/arkoren/skills/arkoren/references/guides/http-fundamentals/body-limit.md)
for request-helper behavior and streaming limits.
