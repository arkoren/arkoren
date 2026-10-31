# Content Type

`arkoren.dev/core/http/middleware/contenttype` is opt-in middleware that rejects
requests whose body is not an allowed media type. Its default response is
`415 Unsupported Media Type` as RFC 9457 problem details. Use it on JSON endpoints that are
not behind `arkoren.dev/core/http/middleware/csrf`: browsers send a cross-site `text/plain` form post without a CORS
preflight, and `request.JSON` does not check the Content-Type.

On an existing `r` router, allow JSON and URL-encoded forms:

```go
r.Use(contenttype.Middleware(func(content *contenttype.Builder) {
	content.Allowed("application/json", "application/x-www-form-urlencoded")
}))
```

By default `application/json` and every `+json` type (such as `application/problem+json`) are allowed.
Matching ignores case and parameters like `charset`. A request with a body must match, and a missing
Content-Type is rejected. A request without a body passes, except an unsafe-method request (POST, PUT,
PATCH, DELETE, QUERY) that sets a disallowed Content-Type.

Use `Middleware()` without a callback for the JSON-only policy.

`Builder.ErrorResponse` replaces the rejection problem. Only its complete zero
value restores the default. A nonzero problem decoded without a status retains
its supplied members and receives the problem renderer's default HTTP 500;
choose an explicit status when you intend a different response.

With `Builder.Events`, every content-type rejection publishes `Rejected`, even
when the custom problem changes its response status. Notifications are not
deduplicated. Keep listeners cheap and review paths and client-selected media
types before forwarding them outside the process.

See the [Content Type guide](../../../../cmd/arkoren/skills/arkoren/references/guides/http-fundamentals/content-type.md)
for browser-request constraints and middleware placement.
