---
title: Serving AsyncAPI Documentation
nav: Serving Documentation
description: Export offline JSON or explicitly serve a protected AsyncAPI endpoint, with construction and cache behavior made explicit.
order: 50
---

# Serving AsyncAPI Documentation

## Introduction

You can distribute a messaging contract as a build artifact without exposing
an endpoint. If consumers need a live URL, register the document handler on your
existing router. Neither path connects to a broker, discovers producers, samples
traffic, or reads broker credentials.

The examples use `Document` from the
[AsyncAPI overview](overview.md#generating-your-first-document), which includes
the complete typed topic declaration and initial file export.

## Exporting without exposing an endpoint

Call `JSON()` to validate and obtain deterministic bytes for a file, fixture,
or external documentation tool:

```go
data, err := Document.JSON()
```

On success, `data` contains the document. The
[overview's export function](overview.md#generating-your-first-document) writes
these bytes to a generated-artifact path only after validation succeeds. If
validation fails, that function leaves the file untouched; a write failure can
leave a partial file. It overwrites an existing destination, so retain any file
you need before calling it. Review addresses and examples before distributing
the document. No HTTP listener or application startup is needed.

## Serving a protected document

> [!WARNING]
> The document can disclose broker addresses, message shapes, examples, and
> security requirements. Before serving it, restrict the listener or protect the
> route with your existing authentication and authorization middleware. Declaring
> broker security in the document does not protect this HTTP endpoint.

For an HTTP endpoint, explicitly register `Document.Handler()` with your
existing `*router.Router`. On a router already restricted by its listener or
access controls, registration is one line:

```go
routes.Get("/asyncapi.json", Document.Handler())
```

Here `routes` is your existing router from `arkoren.dev/core/http/router`.
The handler serves `application/json`; HEAD returns headers without a body.
It adds no route, authentication, or listener by itself, and needs no separate
lifecycle registration.

If you use middleware rather than a restricted listener, register the route
inside an existing protected group. For example, with your application's
`requireDocsAccess` middleware and the existing `routes` router:

```go
routes.Group("/docs", func(protected *router.Router) {
	protected.Use(requireDocsAccess)
	protected.Get("/asyncapi.json", Document.Handler())
})
```

This exposes `/docs/asyncapi.json` through that middleware. Use this alternative
instead of the earlier ungrouped registration if only protected access is
intended. `requireDocsAccess` is application code, not supplied by AsyncAPI;
[route groups and middleware](../http-fundamentals/routing.md#route-groups) explains the setup.

## Understanding construction and caching

`New` runs all construction callbacks synchronously, once in order. During each
message declaration, it calls the non-nil schema provider once and immediately
copies the returned bytes. Examples are encoded and copied when declared, too.
Raw objects and construction drafts are detached from the published document.
Retaining or later changing these inputs cannot change the contract.

Schema provider errors are retained during construction and reported later by
`JSON()` or the HTTP handler. The first export validates the completed document
graph. `JSON()` and every handler made from the same document share one cached
export, including a cached error. Validation runs once even across concurrent
callers; HTTP requests do not rerun construction or schema providers.

`JSON()` returns a copy of the cached bytes, so editing an export cannot change
later exports or responses. The document is immutable. Build a new document when
the contract changes; there is no cache invalidation or reload method, and no
`Start`, `Stop`, or resource cleanup to register.

## Handling declaration errors

Export in a test or build step so declaration errors do not first appear on a
documentation request. Missing title or version, duplicate IDs or references,
unresolved local references, unsupported schemas, and invalid metadata become
errors from `JSON()` or the handler. References may point to declarations made
later in construction.

The handler returns validation errors before committing headers or a body;
your router's error handling controls the error response. Write failures also
propagate to the router and may occur after a response has begun.

Omitting callbacks is valid. Supplying a nil callback panics, and application
panics are not recovered. Nil schema providers, including typed nil values,
are deferred export errors. A nil or zero `Document` returns an error from
`JSON()`; always construct documents with `New`.

## Choosing a viewer

The package ships no AsyncAPI viewer in v1. Swagger UI renders OpenAPI HTTP
documents, not AsyncAPI messaging documents; see the separate
[OpenAPI documentation](../openapi/overview.md). Use a compatible external
AsyncAPI viewer if you need a browser interface. Review whether it follows
Arkoren's `x-arkoren-logical-payload` annotation before relying on it to display
decoded payload schemas.

For every export and handler option, see the [`asyncapi` API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
