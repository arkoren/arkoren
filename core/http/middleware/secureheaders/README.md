# Secure Headers

`arkoren.dev/core/http/middleware/secureheaders` sets standard HTTP security headers for Arkoren handlers.

On an existing `r` router, restrict page resources to the same origin:

```go
r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
	headers.ContentSecurityPolicy("default-src 'self'; frame-ancestors 'none'")
}))
```

Without a callback, `Middleware()` uses its recommended security headers but
omits HSTS. Enable HSTS deliberately with `StrictTransportSecurity` only after
establishing your deployment's HTTPS policy; see the
[Secure Headers guide](../../../../cmd/arkoren/skills/arkoren/references/guides/browser-security/secure-headers.md).
