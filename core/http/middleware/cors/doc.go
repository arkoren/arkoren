// Package cors grants browser access to cross-origin responses. It is not
// authentication or CSRF protection: requests without CORS permission still run
// the next handler, but do not receive access-control response headers.
//
// The default permits any origin without credentials. Configure explicit bare
// ASCII origins before enabling credentials. Invalid origins, host patterns, or
// wildcard-plus-credentials policies panic after all construction callbacks.
// Nil allow lists receive defaults; nonnil empty lists stay empty. Nonpositive
// MaxAge omits the preflight cache header; positive values round down to seconds.
//
// Example:
//
//	routes := router.New()
//	routes.Use(cors.Middleware(func(policy *cors.Builder) {
//		policy.AllowedOrigins("https://app.example.com")
//		policy.AllowCredentials(true)
//	}))
package cors
