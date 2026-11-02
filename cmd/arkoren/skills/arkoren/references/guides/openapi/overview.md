---
title: OpenAPI Overview
nav: Overview
description: Describe an HTTP route and export its contract independently of request handling or documentation serving.
order: 10
summary:
  - Describe a typed JSON response beside its route and export OpenAPI 3.2.1 without a server.
  - Distinguish descriptive metadata from request validation, response writing, and access control.
  - Understand which routes appear and when operation metadata is captured.
---

# OpenAPI Overview

Clients need a description of your HTTP API without having to discover its behavior
through requests. Add an **operation**, the description of one HTTP method and
path, beside a [route registration](../http-fundamentals/routing.md). The router
exports those descriptions as an OpenAPI document without running your handlers.

An annotation describes behavior; it does not implement it. Your handler still
decodes input, applies validation and authorization, and writes its response.
Exporting the document and [serving documentation](serving.md) are separate tasks.

## Describe Output

For a greeting endpoint, use a named response type so the description can follow
the JSON shape your handler writes. These examples use `net/http`,
`arkoren.dev/core/http/response`, `arkoren.dev/core/http/router`, and
`arkoren.dev/foundation/validate`:

```go
type Greeting struct {
	Message string `json:"message"`
}
```

The handler returns that type through the ordinary response helper:

```go
func greet(writer http.ResponseWriter, incoming *http.Request) error {
	return response.JSON(writer, http.StatusOK, Greeting{Message: "Hello"})
}
```

Register it with an operation callback on your existing `routes` router:

```go
routes.Get("/greeting", greet, func(operation *router.OperationBuilder) {
	operation.Summary("Read a greeting")
	operation.JSONResponse(http.StatusOK, validate.Infer[Greeting]())
})
```

The operation describes a `200` response with `application/json` content.
`validate.Infer[Greeting]()` supplies the output type's JSON Schema; it neither
checks response values nor adds runtime validation. Use
[request schemas](operations.md#describe-input) when the endpoint also accepts
validated input.

## Export JSON

After registering your API routes, generate the document:

```go
document, err := routes.OpenAPI("Greeting API", "1.0.0")
```

On success, `document` contains deterministic OpenAPI **3.2.1** JSON, including
`GET /greeting` and its declared response. The supplied `1.0.0` is your API's
version, not the OpenAPI specification version. No listener, application startup,
request, or browser UI is required.

Check `err` before saving or distributing the bytes. Generation checks the
supported document structure and declarations; it does not compare them with
handler behavior. The [serving guide](serving.md#export-json) explains export
failures and live document caching.

The focused fragments in this group retain error results but omit routine error
propagation. Complete implementations must handle those errors before using
their results.

## Contract Boundaries

Only annotated, nonhidden registrations appear in OpenAPI. Routes without an
operation callback still work but are omitted; an annotation without responses
does not acquire a guessed success response. Grouped routes use their
[final prefixed paths](../http-fundamentals/routing.md#group-routes).

Operation callbacks run synchronously once during registration, in order on the
same draft. Schema bytes and examples are captured when supplied, and published
metadata is detached from retained drafts. Nil callbacks panic. Finish route
registration before exporting concurrently or serving requests.

Annotations do not parse bodies, set headers or statuses, discover middleware
errors, or enforce security declarations. Keep them aligned with
[request validation](../http-fundamentals/requests.md#validate-input),
[response writing](../http-fundamentals/responses.md), and
[error mapping](../http-fundamentals/error-handling.md).
[Describing Operations](operations.md) covers their declaration limits.

The exporter does not inspect opaque mounted protocols or infer every integration's
contract. Use the [built-in handler guide](built-in-handlers.md) to choose shipped
annotations or describe an integration manually. For broker messages rather than
HTTP operations, use [AsyncAPI](../asyncapi/overview.md); it produces a separate
document, not input for Swagger UI.

For declarations and export methods, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
