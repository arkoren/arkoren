// Package contenttype provides opt-in middleware that rejects requests whose
// body media type is not allowed. The default response is 415 Unsupported Media
// Type; Builder.ErrorResponse can replace that problem and its status.
//
// On an existing JSON router, install the default policy before routes:
//
//	routes := router.New()
//	routes.Use(contenttype.Middleware())
//
// Body media types default to application/json and structured +json types.
// Matching ignores case and parameters. See [Middleware] for bodyless requests.
//
// JSON endpoints that are not behind the csrf middleware are otherwise
// reachable by a cross-site form post with a text/plain body, which browsers
// send without a CORS preflight. Requiring a JSON Content-Type closes that
// path, because a cross-site page cannot send one without a preflight.
//
// # Events
//
// With [Builder.Events], [Middleware] publishes [Rejected]
// ("http.contenttype.rejected") with the method, the path, and the media type
// (without parameters) of every content-type rejection, even when a custom
// problem changes the HTTP status. Clients control that rate, so listeners must
// be cheap and a rate limiter should sit in front of the middleware. Paths can
// contain personal identifiers; review notifications before external forwarding.
package contenttype
