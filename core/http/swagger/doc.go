// Package swagger serves a local Swagger UI page and pinned static assets for
// an application-owned OpenAPI specification URL. It supports OpenAPI 3.2 through
// Swagger UI 5.34.0; the specification remains a separate endpoint.
//
// [New] builds an immutable [Viewer] and [Viewer.Register] adds its four exact
// GET routes ("/", "/swagger-ui.css", "/swagger-ui-bundle.js" and
// "/initializer.js") to a router group, with no catch-all:
//
//	viewer, err := swagger.New("/openapi.json")
//	if err != nil {
//		log.Fatal(err)
//	}
//	routes := router.New()
//	routes.Get("/openapi.json", routes.OpenAPIHandler("Billing", "1.0"), router.OpenAPIOperation)
//	routes.Group("/docs", viewer.Register)
//
// Register annotates its four routes, so they appear in the router's OpenAPI
// document with their media types, caching headers and the page's redirect.
//
// The page links its assets with relative URLs, so it works under any prefix,
// including one added by a reverse proxy; no forwarded headers are trusted. A
// request for "/docs" is redirected with a relative 308 to "docs/". The viewer
// has no per-route handler methods because the assets are coupled to the page
// by fixed relative names. A relative specification URL is resolved by the
// browser against the page URL, not by this package; prefer a root-relative or
// absolute URL. Protect documentation routes with the application's normal
// middleware when the API is private, for example
// routes.With(auth.Middleware(authenticator)).Group("/docs", viewer.Register).
// The browser fetches the specification and any referenced schemas; this
// package performs no network requests or lifecycle work.
//
// The initializer is an external same-origin script, with no inline scripts or
// CDN dependencies. CSP must allow same-origin scripts, Swagger UI's inline styles
// and data images, and connections to the chosen specification and API origins.
// Remote validation and query-string configuration are disabled. The viewer
// neither sets nor weakens an application's CSP headers.
//
// Dependencies are the standard library and http/router. The embedded upstream
// JavaScript and CSS are distributed under Apache-2.0; assets/PROVENANCE records
// their version, source, checksums, and compatibility evidence. There is no npm
// installation or third-party Go dependency at build or runtime.
package swagger
