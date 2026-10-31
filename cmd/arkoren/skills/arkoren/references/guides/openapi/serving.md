---
title: Serving OpenAPI Documentation
nav: Serving Documentation
description: Export contract JSON, serve it on an explicit route, and add a locally bundled Swagger UI with appropriate access controls.
order: 30
---

# Serving OpenAPI Documentation

## Introduction

Your HTTP contract can be a build artifact, a live JSON endpoint, or an interactive
browser page. Start with the annotated router from the [overview](overview.md),
then choose how consumers obtain its document. Exporting or constructing a handler
does not start a listener, register a route, or add authentication.

## Exporting JSON without a server

If you need a contract for a test, client generator, or release artifact, export
after registering all API routes:

```go
document, err := routes.OpenAPI("Greeting API", "1.0.0")
```

`document` contains deterministic OpenAPI 3.2.1 JSON bytes. Save those bytes using
your build's artifact-writing step, or inspect them in a test. Repeated exports of
the same declarations and configuration produce the same bytes. No request,
application startup, listener, or Swagger UI is needed.

Title and version must be nonempty. Invalid metadata, duplicate operation IDs,
and equivalent path templates return contextual errors rather than a partial
document. Use this export in a test or build step to catch declaration failures
before deployment. The [operation guide](operations.md) covers those validation
boundaries; exporting does not prove that handlers implement their declarations.

Complete registration before exporting concurrently with other work. An offline
export is independent of an `OpenAPIHandler` and does not populate its cache.

## Serving the JSON endpoint

To let clients retrieve the contract, explicitly register the returned handler.
If endpoint names or data shapes are private, use a protected router or restricted
listener. Neither the JSON handler nor Swagger UI adds access controls; protect
both as described under [Protecting the document and browser UI](#protecting-the-document-and-browser-ui):

```go
routes.Get("/openapi.json", routes.OpenAPIHandler("Greeting API", "1.0.0"))
```

Serve `routes` using the [HTTP server](../http-networking/server.md). A successful GET returns
the document with `Content-Type: application/json`. With no operation callback,
the documentation route is omitted from its own export.

`OpenAPIHandler` runs document configuration callbacks when you construct it.
It then uses `sync.OnceValues` to generate the document on its first request and
cache either the bytes or the generation error. Routes added before that first
request are included. Later registrations do not invalidate the cache, and a
cached error is returned on every later request. Finish route registration before
serving requests; recreate the handler if you need a different cached document.
Construction itself does not compile or freeze the router.

The generation cache is in-process memoization, not an HTTP cache policy. The
JSON handler sets no `Cache-Control` header or authentication policy. Apply your
own middleware where those are required. If generation fails, the handler returns
the error for the router's normal [error handling](../http-fundamentals/error-handling.md).

## Adding Swagger UI

If people need to browse the contract or send test requests, add the
`arkoren.dev/core/http/swagger` import. Its factory returns an ordinary
`router.Handler` and an error; handle that error before registration:

```go
viewer, err := swagger.Handler("/docs", "/openapi.json")
routes.Get("/docs", viewer)
routes.Get("/docs/{path...}", viewer)
```

The exact base route serves `/docs`, and the subtree serves `/docs/` and its
assets. Keep the registered prefix equal to the factory's base path. Leave these
opaque UI routes unannotated: the exporter does not traverse them to discover API
operations. Visit `/docs/` after starting your server. The browser fetches the
configured `/openapi.json`; the UI factory does not generate or fetch it.

The UI includes pinned JavaScript and CSS assets rather than loading them from a
CDN. It disables the remote validator and query-string configuration. This avoids
those runtime dependencies, but the browser still needs access to the configured
document and to any API it calls. Embedded assets alone do not make a remote API
available offline.

To change the browser-tab title, add a construction callback:

```go
viewer, err := swagger.Handler("/docs", "/openapi.json", func(page *swagger.Builder) {
	page.Title("Greeting API")
})
```

The default title is `API documentation`. Titles are escaped as plain text; an
empty title stays empty. Callbacks run synchronously once, in order, even when
configuration is invalid. Nil callbacks panic, and retaining a draft cannot
mutate the completed, concurrency-safe handler.

## Choosing paths and document URLs

The UI base path must be a clean absolute URL path without a trailing slash,
except for `/`. Its segments use unreserved ASCII characters. Wildcards, escaped
separators, and queries are not accepted in the factory's base path.

The specification URL must be nonempty. HTTP(S), root-relative, and relative URLs
are accepted; credentials, fragments, scheme-relative URLs, whitespace, and
backslashes return construction errors. Relative URLs resolve against
`basePath + "/"` consistently for both page paths. Request Host and forwarding
headers do not influence that resolution. Prefer a root-relative URL such as
`/openapi.json` when the UI and document share an origin.

The UI handler serves only the page and named assets; unknown paths return `404`.
Only GET and HEAD are accepted. Other methods return `405` with `Allow: GET, HEAD`
if they reach the handler; router method matching can reject them first. HEAD
responses omit the body. HTML and the initializer use `Cache-Control: no-store`;
pinned JavaScript and CSS use `public, max-age=3600`.

## Protecting the document and browser UI

> [!WARNING]
> The document exposes endpoint names and data shapes, and the UI lets callers
> make API requests. Neither handler adds authentication. If this information is
> private, protect both the JSON route and the complete UI subtree with your
> existing middleware, or serve them on a restricted listener like other
> [operator endpoints](../application/health-and-readiness.md).

The browser must be able to authenticate its document fetch as well as its API
requests. Protecting the HTML page alone does not protect the JSON contract.
Swagger UI does not bypass API authentication, authorization, CORS, or other
middleware, and a security declaration does not grant access.

If you configure [Content Security Policy](../browser-security/secure-headers.md),
allow same-origin scripts, inline styles used by Swagger UI, and connections to
the specification and API origins. Allow data images if you use the UI's embedded
images. No inline script permission is required.
Authentication and browser policy must permit the browser's configured document
URL, including a cross-origin URL if you choose one.

The [diagnostic MCP route tools](../observability/exposing-diagnostics.md) inspect
a running router for operators. They do not turn REST endpoints into MCP tools;
publishing OpenAPI does not replace the MCP protocol.

For export and document configuration, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router). For UI configuration and path
validation, see the [`swagger` API reference](https://pkg.go.dev/arkoren.dev/core/http/swagger).
