# CSRF

Cookie-authenticated routes need protection against requests sent by other
sites. `arkoren.dev/core/http/middleware/csrf` provides that protection with Go's
`http.CrossOriginProtection`. On an existing `r` router, allow a trusted partner
origin in addition to same-origin requests:

```go
r.Use(csrf.Middleware(func(protection *csrf.Builder) {
	protection.TrustedOrigins("https://partner.example")
}))
```

Without a callback, `Middleware()` adds no trusted cross-origin exceptions.
GET, HEAD, and OPTIONS always pass and must not change state. Requests without
`Sec-Fetch-Site` or `Origin` also pass. This is not authentication or authorization.

Use `csrf.Builder.Protection` to supply your own `http.CrossOriginProtection`
(for bypass patterns); `csrf.Builder.TrustedOrigins` origins are added to it.
`csrf.Builder.ErrorResponse` replaces the default `csrf.ErrBlocked` problem.

`Builder.Events` reports each blocked request synchronously. Its concrete path,
Origin header, and native error can contain sensitive or client-controlled data;
review those values before forwarding notifications outside the process.

See the [CSRF Protection guide](../../../../cmd/arkoren/skills/arkoren/references/guides/browser-security/csrf-protection.md)
for trusted origins, bypass policies, and browser-security limits.
