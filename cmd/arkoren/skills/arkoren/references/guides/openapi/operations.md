---
title: Describing Operations
description: Align typed requests and responses with explicit parameters, errors, examples, and security declarations.
order: 20
summary:
  - Reuse a typed request schema while keeping runtime decoding and validation explicit.
  - Declare parameters, response media and headers, input problems, and request examples.
  - Describe authentication requirements without confusing declarations with enforcement.
  - Recognize schema export limits and catch invalid declarations before deployment.
---

# Describing Operations

A client needs to know what to send and how to interpret both success and failure.
Operation callbacks record that contract beside your handler registration. Reuse
the handler's input schema and output type where possible, but keep decoding,
validation, and response writing explicit: declarations do not bind or execute
those steps for you.

These examples assume an existing `routes` router and the `Greeting` output type
from the [overview](overview.md). They use `net/http`,
`arkoren.dev/core/http/request`, `arkoren.dev/core/http/response`,
`arkoren.dev/core/http/router`, and `arkoren.dev/foundation/validate`.

## Describe Input

To let a caller choose the greeting's name, define a narrow request type and its
[validation schema](../validation/structs-and-reuse.md):

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

The handler must actually decode and validate that input before producing its
typed response:

```go
func createGreeting(writer http.ResponseWriter, incoming *http.Request) error {
	input, err := request.ParseJSON(incoming, greetingInput)
	if err != nil {
		return err
	}

	return response.JSON(writer, http.StatusOK, Greeting{Message: "Hello, " + input.Name})
}
```

`ParseJSON` returns the parsed, validated input, with schema defaults applied.
An invalid request returns an error instead of reaching the success response.
Now reuse the schema in the operation description:

```go
routes.Post("/greeting", createGreeting, func(operation *router.OperationBuilder) {
	operation.ID("createGreeting")
	operation.JSONBody(greetingInput).BodyRequired(true)
	operation.JSONResponse(http.StatusOK, validate.Infer[Greeting]())
})
```

`JSONBody` describes `application/json`; use `Body(media, schema)` for another
representation. Body presence is optional unless you declare
`BodyRequired(true)`. That flag is separate from a schema's required properties,
and neither declaration rejects requests by itself.

## Describe Failures

The success declaration does not discover the errors returned by `ParseJSON`.
Add its known input problems inside the same registration callback:

```go
operation.Problem(request.ErrInvalidJSON)
operation.Problem(request.ErrValidationFailed)
operation.Problem(request.ErrBodyTooLarge)
```

These describe malformed JSON at `400`, decoding or validation violations at
`422`, and an oversized body at `413`. Operational failures can also escape
parsing; this list is not a claim that every failure has one of those identities.
See [request validation](../http-fundamentals/requests.md#validate-input) for the
runtime boundary.

`Problem` accepts `problem.Details` and describes the router's negotiated RFC 9457
wire response at its status: `application/problem+json`, `application/json`, and
`text/plain`. A zero status becomes `500`. The schema does not expose the Go
value's private fields or error cause. Declare application and middleware problems
explicitly, matching your actual [error mapping](../http-fundamentals/error-handling.md).

## Add Parameters

For a route such as `/greetings/{id}`, describe an identifier and optional inputs
independently of the body. Inside its operation callback:

```go
operation.Parameter("id", "path", router.RawSchema(`{"type":"integer","minimum":1}`))
operation.Parameter("language", "query", router.RawSchema(`{"type":"string"}`))
operation.Parameter("X-Request-ID", "header", router.RawSchema(`{"type":"string"}`))
```

The supported locations are `path`, `query`, `header`, and `cookie`. Path parameters
are always required; all other parameters are optional in the document. There is
no setting to require a non-path parameter or choose its serialization style.
Schema constraints do not change these limits or parse the incoming value.

Export checks path parameter names against the final path, including group
prefixes. Undescribed single-segment wildcards receive required string parameters;
an explicit declaration supplies the schema instead. Duplicate names within one
location fail export, with case-insensitive comparison for header parameters.

## Describe Responses

Choose declarations that match what the handler writes. `JSONResponse` describes
a JSON body; `Response` describes a status without adding a body. For a redirect,
declare its status and destination header:

```go
operation.Response(http.StatusSeeOther, "Continue at the returned location")
operation.ResponseHeader(http.StatusSeeOther, "Location", router.RawSchema(`{"type":"string","format":"uri-reference"}`))
```

Statuses must be integers from `100` through `599`; there are no status-range or
`default` response declarations. An empty description uses standard HTTP status
text. `Response` preserves existing bodies and headers at that status; it does not
remove them to make a previously declared response bodyless.

For a binary download, describe the media type rather than inventing a JSON body:

```go
operation.MediaResponse(http.StatusOK, "application/octet-stream", router.RawSchema(`{"type":"string","format":"binary"}`))
operation.ResponseHeader(http.StatusOK, "Content-Disposition", router.RawSchema(`{"type":"string"}`))
```

Different media types at one status describe alternative representations;
repeating a media type replaces its representation. Request and response media
types must parse as a type/subtype. These declarations do not select an encoder,
set headers, negotiate content, or validate output. Check the actual behavior of
[downloads and other integrations](built-in-handlers.md#manual-contracts), including
[streaming responses](../http-fundamentals/streaming-responses.md).

Response header names match case-insensitively within one status. A later
`x-request-id` replaces an earlier `X-Request-ID`, including its exported spelling
and schema; other statuses are unaffected. Invalid header names fail export.

## Request Examples

To show clients a valid greeting request, declare the body representation before
adding its named example:

```go
operation.JSONBody(greetingInput).BodyRequired(true)
operation.Example("application/json", "personalGreeting", GreetingInput{Name: "Ada"})
```

The example is for the request body, not a response. Its JSON encoding is captured
immediately, but it is not validated against the schema. Encoding failures become
export errors. Adding an example before its media type records an error even if
you declare that body representation later.

## Declare Security

If your routes enforce bearer authentication, name that scheme when exporting:

```go
document, err := routes.OpenAPI("Greeting API", "1.0.0", func(definition *router.OpenAPIBuilder) {
	definition.SecurityScheme("bearer", router.RawSchema(`{"type":"http","scheme":"bearer"}`))
})
```

Then reference it in each protected route's operation callback:

```go
operation.Security("bearer")
```

This declares a requirement; it does not install authentication or authorization.
Configure the [actual authentication policy](../authentication/overview.md)
separately and declare its relevant error responses. Middleware does not supply
schemes or requirements automatically. Pass the same document configuration to
`OpenAPIHandler` if you also serve the JSON.

The supported schemes are HTTP, API-key, and mutual TLS. HTTP requires a valid
`scheme` token. API-key requires a nonempty `name` and an `in` location of `query`,
`header`, or `cookie`; a header name must be an HTTP token. For example:

```json
{
  "type": "apiKey",
  "name": "X-API-Key",
  "in": "header"
}
```

OAuth flows and OpenID Connect scheme definitions are unsupported. Scheme names
use letters, digits, `.`, `_`, or `-`; unsupported fields are rejected except
`x-` extensions. Standard supported fields must be strings.

Every referenced scheme must be declared. Repeated `Security` calls describe
alternatives (OR), not a combined requirement (AND). Nonempty scopes fail export.
The document API has no server URL setting.

## Schema Limits

Bodies, responses, parameters, and headers accept `router.Schema`, an interface
with `JSONSchema() ([]byte, error)`. Published validation schemas implement it,
but export includes only their representable rules. Cross-field comparisons,
conditions, custom checks, normalizers, and clock-based rules remain runtime
behavior; see [JSON Schema](../validation/json-schema.md).

`validate.Infer[T]()` describes a Go type's JSON output without applying value
rules. Interfaces and opaque custom `MarshalJSON` implementations produce open
schemas unless a supported explicit type provider describes their shape.
Recursive types become open at the repeated level, and pointer nil values are
not modeled. Types without a JSON representation produce inference errors.

If inference cannot describe your wire shape, supply `router.RawSchema`. Its root
must be a JSON object or boolean, not an array, string, or null. The router checks
syntax and root shape, not arbitrary JSON Schema keywords. Schema bytes are
captured at declaration time and remain inline; references are neither rewritten
nor fetched. Successful export is not proof of equivalence with your runtime
validator or encoder.

## Check Declarations

Use `Summary`, `Description`, and `Tags` to identify and organize operations.
Descriptions pass CommonMark through without interpretation or sanitization.
`Deprecated(true)` labels an operation but does not disable it or add a warning
header.

Nonempty operation IDs must be unique across the whole document. If one
registration expands to several methods, omit its ID or register each method
separately with a unique ID. Equivalent templates such as `/users/{id}` and
`/users/{name}` also conflict, even under different methods. `QUERY` has its OpenAPI
3.2 method field; `CONNECT` and extension methods appear in `additionalOperations`.

Leave mounts and catch-all registrations unannotated, or set `Hidden(true)`.
Visible annotated opaque paths fail export. Hiding an operation suppresses its
metadata errors too, but does not prevent requests or remove it from `Routes()`.

Invalid metadata does not stop route execution. The first declaration error is
retained even if a later setter replaces the invalid value. Run
[offline export](serving.md#export-json) before deployment to catch those errors,
invalid media types, duplicate IDs, and path conflicts rather than discovering
them on the first documentation request.

For all declaration methods, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
