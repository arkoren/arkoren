---
title: Describing Operations
description: Describe request bodies, parameters, responses, examples, and security without changing handler behavior.
order: 20
---

# Describing Operations

## Introduction

An endpoint's useful contract includes more than its success payload. Describe
what clients send, which failures they can handle, and how they authenticate
beside the handler registration. Start with the router and greeting response
from the [overview](overview.md); all declarations on this page describe behavior
that your handlers and middleware must implement separately.

## Reusing a request schema

If your handler accepts JSON, reuse its [validation schema](../validation/overview.md)
so the body description follows the same representable input rules. Add imports
for `arkoren.dev/core/http/request` and `arkoren.dev/foundation/validate`, then
define the input independently of the route:

```go
type GreetingInput struct {
	Name string `json:"name"`
}

var greetingInput = validate.Struct(func(object *validate.StructBuilder[GreetingInput]) {
	object.Field("Name", func(name *validate.StringBuilder[string]) {
		name.Required()
	})
})
```

The handler must still decode and validate the request explicitly:

```go
func createGreeting(writer http.ResponseWriter, incoming *http.Request) error {
	input, err := request.ParseJSON(incoming, greetingInput)
	if err != nil {
		return err
	}
	return response.JSON(writer, http.StatusOK, Greeting{Message: "Hello, " + input.Name})
}
```

Register the body, success output, and known decoding and validation problems:

```go
routes.Post("/greeting", createGreeting, func(operation *router.OperationBuilder) {
	operation.ID("createGreeting")
	operation.JSONBody(greetingInput).BodyRequired(true)
	operation.JSONResponse(http.StatusOK, validate.Infer[Greeting]())
	operation.Problem(request.ErrInvalidJSON)
	operation.Problem(request.ErrValidationFailed)
	operation.Problem(request.ErrBodyTooLarge)
})
```

`JSONBody` describes `application/json`; `Body(media, schema)` describes another
request media type. Neither parses input. `BodyRequired(true)` marks body presence
as required in the document, not at runtime. Declaring a required property inside
the schema is separate from requiring the body itself.

`Problem` accepts `problem.Details` and describes the router's negotiated RFC 9457
wire model: `application/problem+json`, `application/json`, and plain text at that
problem's status. A zero status becomes `500`. It does not expose private Go
fields or an underlying error cause. It does not discover custom problems from
application code or middleware; declare those explicitly and keep them aligned
with your [error mapping](../http-fundamentals/error-handling.md).

## Describing parameters

Clients need path identifiers, query filters, cookies, or headers independently
of the request body. Within an operation callback for a route such as
`/greetings/{id}`, declare their schemas:

```go
operation.Parameter("id", "path", router.RawSchema(`{"type":"integer","minimum":1}`))
operation.Parameter("language", "query", router.RawSchema(`{"type":"string"}`))
operation.Parameter("X-Request-ID", "header", router.RawSchema(`{"type":"string"}`))
```

Supported locations are `path`, `query`, `header`, and `cookie`. Path parameters
are always required; other locations are optional. There are no settings for
requiring non-path parameters or choosing a serialization style. A schema's
constraints do not change those limits or enforce request parsing.

The exporter checks path declarations against the final path, including group
prefixes. It supplies required string parameters for otherwise undescribed
single-segment wildcards. Explicit declarations replace that inference. Duplicate
names in one location fail export; header names are compared case-insensitively.

## Describing response bodies and headers

Choose the representation the handler actually writes. For a JSON response,
continue using `JSONResponse`. For a bodyless result or redirect, declare the
status without inventing a JSON body. Within its operation callback:

```go
operation.Response(http.StatusSeeOther, "Continue at the returned location")
operation.ResponseHeader(http.StatusSeeOther, "Location", router.RawSchema(`{"type":"string","format":"uri-reference"}`))
```

`Response` accepts statuses from `100` through `599`. An empty description uses
the standard HTTP status text. It preserves representations and headers already
declared for that status. `ResponseHeader` describes a header without setting it
at runtime. Within one response status, header names match case-insensitively
using HTTP's ASCII rules: a later `x-request-id` declaration replaces both the
schema and exported spelling of an earlier `X-Request-ID` declaration, rather
than adding a second header. Headers at other statuses are unchanged. The
handler must still set the actual response headers, and invalid header names
still fail export.

For a download handler, describe the actual media type and output shape:

```go
operation.MediaResponse(http.StatusOK, "application/octet-stream", router.RawSchema(`{"type":"string","format":"binary"}`))
operation.ResponseHeader(http.StatusOK, "Content-Disposition", router.RawSchema(`{"type":"string"}`))
```

Different media types at the same status describe negotiated representations.
Repeating the same media type replaces its representation. Declarations do not
choose an encoder, add headers, or validate the output. See the
[integration boundaries](built-in-handlers.md#describing-integrations-manually)
before annotating a shipped download or protocol handler.

## Adding request examples

A sample payload helps clients construct a request. Declare its body
representation first, then add a named example in the same callback:

```go
operation.JSONBody(greetingInput).BodyRequired(true)
operation.Example("application/json", "personalGreeting", GreetingInput{Name: "Ada"})
```

`Example` captures the value's JSON encoding immediately. Encoding errors become
export errors, and examples are not checked against their schemas. It adds a
request-body example, not a response example. Calling it before the matching
body media type is declared records an error even if a later declaration adds
that representation.

## Describing authentication

If your API enforces bearer authentication, declare the scheme when exporting
the document. The existing `routes` must contain your registrations:

```go
document, err := routes.OpenAPI("Greeting API", "1.0.0", func(definition *router.OpenAPIBuilder) {
	definition.SecurityScheme("bearer", router.RawSchema(`{"type":"http","scheme":"bearer"}`))
})
```

The same document callback can be passed to `OpenAPIHandler`. In each protected
route's operation callback, reference the declared name:

```go
operation.Security("bearer")
```

This does not install middleware or enforce authentication. Document the actual
[authentication policy](../authentication/overview.md), including relevant error
responses, separately from configuring it. Schemes are not inferred from global
middleware or handler types.

Supported scheme types are HTTP, API-key, and mutual TLS. An HTTP scheme requires
a valid `scheme` token. An API-key scheme requires a nonempty `name` and an `in`
location of `query`, `header`, or `cookie`. For example, a header key definition
is `{"type":"apiKey","name":"X-API-Key","in":"header"}`. OAuth flows and OpenID
Connect scheme definitions are unsupported; do not pass them through this API.

Every `Security(name)` call adds an alternative (OR), not a combined (AND)
requirement. Every referenced scheme must be declared. Nonempty scopes fail
export. There is no document-level servers setting.

## Choosing and checking schemas

Operation bodies, responses, parameters, and headers accept `router.Schema`,
which supplies `JSONSchema() ([]byte, error)`. Published validation schemas
implement that interface. For output types, `validate.Infer[T]()` is convenient,
but custom `MarshalJSON` implementations and interfaces produce open schemas.
Recursive types become open at the repeated level, and pointer nil values are
not modeled. For validation schemas, only representable rules become JSON
Schema; review cross-field rules and domain invariants separately.

If inference cannot describe your wire format, use `router.RawSchema`, a
byte-slice type containing a JSON Schema object or boolean. Arrays, strings, and
null are not accepted as the schema root. The router captures schema bytes when
you declare them. It checks JSON syntax and root shape, not arbitrary JSON Schema
keywords. Schemas remain inline; references are neither fetched nor rewritten.

## Controlling inclusion and export failures

Use `Summary`, `Description`, and `Tags` to help readers navigate operations.
`Description` accepts CommonMark without interpretation or sanitization.
`Deprecated(true)` labels an operation without disabling it or adding a runtime
warning header.

An operation ID is optional, but nonempty IDs must be globally unique across
methods. Omit the ID when one registration expands across several methods, or
register each method with its own ID. Equivalent templates such as `/users/{id}`
and `/users/{name}` cannot coexist in an export, even under different methods.

Leave mounts and catch-all patterns unannotated, or use `Hidden(true)` in their
callback. A visible annotated catch-all cannot be represented and fails export.
`Hidden(true)` excludes a route and its metadata errors from OpenAPI only; it
does not prevent requests or hide the route from `routes.Routes()`.

Invalid declarations do not break request execution. The first declaration
error is retained even if a later setter replaces that value. Check the error
from [offline export](serving.md#exporting-json-without-a-server) before deployment
to find invalid media types, schema errors, duplicate IDs, and path conflicts
without waiting for the first documentation request.

For the complete declaration surface, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
