---
title: Serving Documentation
description: Export messaging contract files or serve protected JSON with explicit snapshot, caching, and failure behavior.
order: 50
summary:
  - Export a generated file or serve JSON through application-owned access controls.
  - Understand immediate input capture, once-only export validation, and immutable cached results.
  - Handle declaration failures before deployment and account for external viewer limitations.
---

# Serving Documentation

You can distribute an AsyncAPI file without exposing an HTTP endpoint. If
consumers need a URL, explicitly mount the document's handler on your existing
router. Both paths use the same immutable document and cached export; neither
starts messaging infrastructure or discovers runtime contracts.

These examples use `Document` from the [overview](overview.md#document-generation).
Review broker addresses, schema fields, examples, and security requirements
before distributing it. Document metadata is not automatically redacted.

## File Exports

For a generated artifact, obtain validated bytes before opening the destination.
With `os` imported from the standard library, this application-owned helper
leaves the file untouched if document generation fails:

```go
func Export(path string) error {
	data, err := Document.JSON()

	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}
```

Call `Export("asyncapi.json")` from your build command and handle its returned
error. On success, the file contains the same deterministic JSON returned by
`Document.JSON()`. No application startup, HTTP listener, or broker is needed.

Choose a generated-artifact path: `os.WriteFile` overwrites an existing file,
and an I/O failure can leave a partial file. The mode `0600` applies when creating
the file; it does not tighten permissions on an existing file. If publication
must replace an artifact atomically, implement an appropriate temporary-file
and replacement procedure in your build tooling.

## Protected Access

> [!WARNING]
> The document can expose internal broker addresses, message shapes, and example
> data. Before mounting its handler, restrict the listener or require your
> application's documentation authentication and authorization. Declared broker
> security does not protect an HTTP documentation route.

With an existing `routes` router and your application's `requireDocsAccess`
middleware, register the endpoint inside a protected group. Import `router` from
`arkoren.dev/core/http/router`:

```go
routes.Group("/docs", func(protected *router.Router) {
	protected.Use(requireDocsAccess)
	protected.Get("/asyncapi.json", Document.Handler())
})
```

This serves `/docs/asyncapi.json` through that middleware. The middleware is
application code, not supplied by AsyncAPI; [group routes](../http-fundamentals/routing.md#group-routes)
describe how to share access controls. If an existing listener or router already
enforces the required access boundary, you can instead register the handler
directly on that restricted router.

The handler returns `application/json`; a successful GET returns 200 with the
document body. HEAD sends headers without a body and, through Arkoren's router,
returns 204 because the handler does not explicitly commit a status.
`Handler()` alone does not register a route, bind a listener, add authentication,
or require lifecycle hooks. It serves the same cached document as `JSON()`;
it does not rebuild the contract on each request.

## Snapshot Behavior

Construction and export happen at different times:

1. `New` runs supplied callbacks synchronously, once in their supplied order,
   including callbacks for invalid declarations.
2. Each message declaration calls its non-nil schema provider once and copies
   the returned bytes immediately. Each `WireExample` encodes and copies its
   value when declared. Raw objects and retained construction drafts are also
   detached from the published document.
3. The first export checks the completed document graph. `JSON()` and every
   handler from that document share one cached result, including an error.
   Concurrent callers do not repeat validation or schema-provider calls.

Later changes to captured inputs or retained builders cannot change the contract.
Do not mutate inputs concurrently with capture, and keep providers free of network
resolution. The package performs no broker I/O; application callbacks and
providers remain your code.

Every successful `JSON()` call returns a detached copy of the cached bytes.
Changing that slice cannot affect later exports or HTTP responses. The document
has no invalidation or reload method: construct a new one when the contract
changes. There is no `Start`, `Stop`, or cleanup to register.

## Declaration Failures

Export during a test or build step, before exposing the route. Empty title or
version, duplicate IDs or selections, unresolved local references, unsupported
schemas, invalid job/topic definitions, and invalid metadata become errors from
`JSON()` or the handler. Forward references are valid if their targets exist in
the completed document. The first captured declaration error is retained even
if a later declaration would otherwise replace its value.

The handler returns generation errors before setting headers or writing a body,
so your [router's error handling](../http-fundamentals/error-handling.md) controls
the response. Response-write errors also propagate, but can occur after headers
or part of the body have already been sent.

Omitting construction callbacks is valid. Supplying a nil callback panics, and
application panics are not recovered. Nil or typed-nil schema providers instead
become deferred export errors without invoking their methods. A nil or zero
`Document` returns an error from `JSON()`; construct documents with `New`.

## External Viewers

The package includes no AsyncAPI viewer. Use a compatible external viewer if
you need a browser interface, and check whether it displays the
[`x-arkoren-logical-payload` annotation](messages-and-schemas.md#wire-and-logical).
Otherwise, inspect the logical schema component separately from the envelope.

Swagger UI displays OpenAPI HTTP documents, not these messaging contracts.
[OpenAPI serving](../openapi/serving.md) belongs to that separate contract.
Do not upload internal documents to a hosted viewer unless your data-sharing
policy permits exposing their metadata and examples.

For export and handler behavior, see the
[`asyncapi` API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
