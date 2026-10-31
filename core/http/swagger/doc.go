// Package swagger serves a local Swagger UI page and pinned static assets for
// an application-owned OpenAPI specification URL. It supports OpenAPI 3.2 through
// Swagger UI 5.34.0; the specification remains a separate endpoint.
//
// Register the returned router.Handler explicitly. Register the exact page path
// as well as the wildcard to avoid a router-generated slash redirect:
//
//	endpoint, err := swagger.Handler("/docs", "/openapi.json")
//	if err != nil {
//		log.Fatal(err)
//	}
//	routes := router.New()
//	routes.Get("/docs", endpoint)
//	routes.Get("/docs/{path...}", endpoint)
//
// For the root base path "/", register "/{path...}". A reverse proxy prefix
// must be included in the explicit base path; no forwarded headers are trusted.
// Protect documentation routes with the application's normal middleware when
// the API is private. The browser fetches the specification and any referenced
// schemas; this package performs no network requests or lifecycle work.
//
// The initializer is an external same-origin script, with no inline scripts or
// CDN dependencies. CSP must allow same-origin scripts, Swagger UI's inline styles
// and data images, and connections to the chosen specification and API origins.
// Remote validation and query-string configuration are disabled. The handler
// neither sets nor weakens an application's CSP headers.
//
// Dependencies are the standard library and http/router. The embedded upstream
// JavaScript and CSS are distributed under Apache-2.0; assets/PROVENANCE records
// their version, source, checksums, and compatibility evidence. There is no npm
// installation or third-party Go dependency at build or runtime.
package swagger
