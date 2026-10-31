// Package csrf provides Cross-Site Request Forgery protection middleware.
// It uses net/http.CrossOriginProtection with no trusted cross-origin exceptions
// unless configured. Safe methods GET, HEAD, and OPTIONS always pass and must not
// change state. Requests without Sec-Fetch-Site or Origin headers also pass;
// cross-origin protection is not authentication or authorization.
//
// Install the same-origin policy before registering routes:
//
//	routes := router.New()
//	routes.Use(csrf.Middleware())
//
// With [Builder.Events], every blocked request publishes [Blocked]
// ("http.csrf.blocked") with the method, path, Origin header, and native error.
// Listeners run synchronously; review client-controlled and potentially sensitive
// diagnostic values before forwarding them outside the process.
package csrf
