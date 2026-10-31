# CORS

When a browser frontend uses a different origin from your API, it needs
permission to read the API's responses. `arkoren.dev/core/http/middleware/cors`
sets those Cross-Origin Resource Sharing headers. On an existing `r` router,
allow credentialed requests from one frontend:

```go
r.Use(cors.Middleware(func(policy *cors.Builder) {
	policy.AllowedOrigins("https://app.example")
	policy.AllowCredentials(true)
}))
```

Without a callback, `Middleware()` allows any origin without credentials.
Use explicit trusted origins with credentials. CORS does not replace
[CSRF protection](../csrf/README.md) for cookie-authenticated routes.

Configure bare ASCII origins with a scheme, hostname, and optional numeric port
from 0 through 65535. Use punycode for international hostnames. Paths, userinfo,
query or fragment delimiters, and subdomain patterns such as `https://*.example`
are rejected. Invalid origins and any `"*"` entry combined with credentials panic
after all callbacks; later callbacks can replace intermediate settings.
Use the browser's origin spelling. Matching lowercases configured origins but
does not perform full browser URL canonicalization or remove default ports.

A nil allow list receives defaults. To grant no origins, use a nonnil empty list:
`policy.AllowedOrigins([]string{}...)`. This also remains valid with credentials.
`MaxAge(0)` or a negative value omits the preflight cache header; positive durations
round down to whole seconds without widening the configured duration.
