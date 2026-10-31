---
title: Requests
description: Read path and query parameters, headers, cookies, JSON and XML bodies, and file uploads, with size limits and errors that become responses.
order: 20
---

# Requests

## Introduction

Before a handler can use client input, it needs to read and check it. The
`request` package adds typed parameter parsing and size-limited body decoding
to the standard `*http.Request`. Invalid input returns a problem error that
an Arkoren router can render, so your handler can return the error rather
than format an error response. Standard `net/http` request methods still work.

Import helpers from `arkoren.dev/core/http/request`. In these handler fragments,
`r` is the incoming `*http.Request`, not the router. Focused snippets retain error
results but omit routine checks; return an error before using its result.

## Path parameters

A route such as `/users/{id}` captures part of the path. `r.PathValue`
returns the captured text, or an empty string when the route has no such
wildcard:

```go
id := r.PathValue("id")
```

`request.Param[T]` parses the value as `T`: the `int`/`uint` families through
64 bits, `float32`, `float64`, `bool`, or `string`, including named types such as
`type UserID int64`.
`uintptr` is not a supported URL parameter type, even though JSON can decode it.
If the value is missing, empty, out of range for `T`, or malformed, it returns
a `400 Bad Request` problem that names the parameter, so the client learns which
part of the URL was wrong:

```go
id, err := request.Param[int](r, "id")
```

When a bad value should fall back to a default instead of failing, use
`request.ParamOr`, which infers `T` from the fallback.

## Query parameters

Query parameters work the same way. `request.Query[T]` parses the first value
of a parameter and fails with a `400 Bad Request` problem, and
`request.QueryOr` returns a fallback when the parameter is absent or invalid.
For a plain string, `QueryOr(r, "q", "")` is the raw getter:

```go
sort := request.QueryOr(r, "sort", "name")

limit, err := request.Query[int](r, "limit")
```

`request.HasQuery` reports whether a parameter is present at all, which is
useful for flags such as `?verbose` that carry no value.

> [!NOTE]
> `QueryOr` also falls back for `?sort=`, because an empty value is not a
> valid value for any type. Use `HasQuery` to tell the two cases apart.

For paginated lists, `request.Page` and `request.Cursor` read the page or cursor
and the page size in one call; see [Pagination](../database/pagination.md).

Query parsing follows `net/url`: values are URL-decoded once, and malformed
pairs can be omitted rather than rejecting the whole query. Scalar conversion
follows Go's `strconv`, not JSON Schema: floats accept `NaN` and infinities,
and booleans accept spellings such as `1`. OpenAPI parameter schemas are
descriptive only; a documented `minimum` does not enforce that bound. Apply
application validation for finite numbers, ranges, or stricter URL grammar.

## Headers and cookies

`request.Header` returns a header's value, and `request.HeaderOr` a fallback
when it is missing or empty:

```go
lang := request.HeaderOr(r, "Accept-Language", "en")
```

Cookies follow the same pattern with `request.Cookie`, which returns the whole
cookie or `nil`, and `request.CookieValue` and `request.CookieValueOr`, which
return only its value.

## JSON bodies

> [!WARNING]
> The body helpers do not check the `Content-Type` header. A browser can send a
> cross-site `text/plain` form post with a JSON-looking body. Add the
> [content type middleware](content-type.md) to JSON endpoints so
> such requests are rejected.

To decode a JSON body into a struct, call `request.JSON` with the type you
expect:

```go
type CreatePost struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

input, err := request.JSON[CreatePost](r)
```

Native JSON input failures return `request.ErrInvalidJSON` (`400 Bad Request`),
and a body larger than the limit returns `request.ErrBodyTooLarge`
(`413 Content Too Large`). Return the error before writing the response so the
router can render it.

Decoding uses Go's `encoding/json/v2` defaults: member names are case-sensitive,
duplicate names are rejected, and a second top-level JSON value is rejected.
Native integers reject fractional and exponent spellings, as well as overflow.
These rules differ from permissive or legacy JSON decoders.

By default, unknown fields in the JSON are ignored. Pass `request.Strict()` to
reject them, which helps clients notice typos in field names:

```go
input, err := request.JSON[CreatePost](r, request.Strict())
```

Ordinary errors from custom JSON or text decoder methods, body reads, and other
operational work are not automatically bad-input problems. They retain their
causes for server-side handling. A custom decoder must explicitly identify an
intended input failure with an input problem or an appropriate native JSON
error; do not turn arbitrary callback errors into field advice. Existing HTTP
problems keep their identity. The router keeps ordinary error text private;
see [error handling](error-handling.md#plain-errors).

### Validating the body

Usually you want to check the input as well as decode it. Import
`arkoren.dev/foundation/validate`. `request.ParseJSON`
decodes the body and validates it against a schema in one call:

```go
var submission = validate.Struct(func(object *validate.StructBuilder[CreatePost]) {
	object.Field("Title", func(title *validate.StringBuilder[string]) {
		title.Trim()
		title.Required()
		title.Max(120)
	})
	object.Field("Body", func(body *validate.StringBuilder[string]) {
		body.Required()
	})
})

input, err := request.ParseJSON(r, submission)
```

Schema violations become a `422 Unprocessable Content` problem matching
`request.ErrValidationFailed`, with an `errors` member keyed by JSON Pointer.
Pointers use JSON member names, not Go selectors: `json:"a/b~name"` becomes
`/a~1b~0name`. `ParseJSON` returns the schema's normalized and defaulted copy,
so `input.Title` is already trimmed. `ValidateJSON` checks the schema but returns
the decoded value rather than the schema's prepared copy.
[Validation](../validation/overview.md) explains schemas in depth.

Native semantic decoding failures also become 422 violations, using code
`decode`; strict unknown members keep code `unknown`. Syntax errors, including
duplicate names, stay 400. Native decoding stops at its first semantic failure,
so later members are not decoded. Schema checks still run on the partial value,
but presence-rule violations are suppressed. Fix the reported value and resubmit
to see later decoding errors. Schema infrastructure errors pass through instead
of becoming validation feedback.

### Decoding hints and migration

Native error metadata describes the Go output type, not a field's required JSON
representation. A `decode` violation therefore has no `params.expected`.
Its optional `params.default_representation` is only a default-type hint, never
advice about which wire kind to send. For example, `json:"count,string"` accepts
`"42"` and rejects `42`, even though an integer's default hint is `integer`.
Binary slices and arrays, timestamps, and text-decoded values retain a default
`string` hint. Opaque custom JSON methods, unsupported Go kinds, and
`time.Duration` have no hint.

> [!NOTE]
> Migration: native JSON semantic violations now use code `decode` and optional
> `params.default_representation` instead of `type` and `params.expected`.
> Treat the new parameter as a default-representation hint, never a required
> wire kind. Add a `decode` entry to your validation message catalog, using a
> generic message that requires neither parameter. Without that entry, the
> message stays English. Schema-generated codes, including `type`, and the strict
> unknown-member code `unknown` are unchanged.

This changes violation metadata, not the top-level Problem Details `type` or
HTTP status. The 422 problem identity and native JSON Pointer are preserved.
Native semantic causes remain available through `errors.Is` and `errors.As`,
without an intermediate 400 problem identity in the chain. The client-facing generic message
does not contain native error text, submitted values, or Go implementation type
names. Keep decoder causes in server-side diagnostics rather than copying them
into a problem's detail or a translation. See
[validation message catalogs](../validation/schemas-and-helpers.md#locale-helpers)
for localization.

### JSON-v2 representation limits

The Go 1.27 JSON-v2 API supports `omitzero`, `omitempty`, `string`, `case`, and
`embed` tag options; `omitzero` and `omitempty` do not affect decoding. The
`string` option applies only to a field's top-level numeric value, not numbers
inside a composite field. This API does not support `format:*` options such as
`format:array`; the helpers do not add them. `time.Duration` has no default JSON
representation. Choose an application decoder or an explicitly represented DTO
field and convert it in your application.

Native decoding support does not imply schema support for every layout. The
validator rejects explicit field registration using JSON `embed`, `inline`, or
`unknown` options rather than inventing field paths. `embed` is a native JSON-v2
option; `inline` and `unknown` are not supported tag options in this API. Construct
schemas during application setup so unsupported layouts fail before requests.

### XML and raw bodies

`request.XML`, `ValidateXML`, and `ParseXML` decode the first XML element only;
they do not check trailing elements. Native syntax and numeric-conversion
failures remain `request.ErrInvalidXML` (400), including with the validation
helpers: XML errors have no JSON Pointer to turn into field feedback. Ordinary
application decoder and transport errors retain their operational causes.
`request.Strict()` is unsupported for XML and returns
`request.ErrStrictUnsupported` without reading the body.

`request.Bytes` and `request.String` read the body without decoding it; `String`
does not validate UTF-8. A nil `r.Body` is empty input, both directly and through
the [body limit middleware](body-limit.md): raw reads return no data, while
JSON and XML return their existing malformed-body problems, not a panic.
An oversized declared length can still be rejected by middleware before decoding.

## Body size limits

Every body helper limits how much it reads, 10 MiB by default. This bounds
the body bytes read, not the total memory used by decoding or application
work. For a single call, pass a different limit:

```go
input, err := request.JSON[CreatePost](r, request.MaxSize(1<<20)) // 1 MiB
```

To set one limit for a whole router or group, use the
[body limit middleware](body-limit.md). Its limit is a ceiling:
`MaxSize` can lower it for a call, but never raise it. `request.Unlimited()`
removes only the helper's own cap; it cannot remove an upstream reader's limit.

A body can only be read once. Call one body helper per request.

## File uploads

`request.Multipart` parses a `multipart/form-data` request, the format browsers
use for forms with files. It takes the maximum size of the whole request:

```go
form, err := request.Multipart(r, 5<<20) // 5 MiB

name := form.Value("name")
avatar := form.File("avatar") // *multipart.FileHeader, or nil
```

Up to 8 MiB of file content stays in memory; larger uploads spill to temporary
files. A request that is too large, is not multipart, or is malformed returns a
`413`, `415`, or `400` input problem. Body-read and temporary-file I/O failures
remain operational errors rather than client validation feedback; existing HTTP
problems keep their identity and causes.

When `Multipart` parses a form inside a `router.Handler` lifecycle, it registers
one after-response cleanup hook. Outside that lifecycle, you own cleanup and
must call `form.RemoveAll()` after using the uploads. A form already parsed by
other code keeps its existing cleanup owner; reusing `r.MultipartForm` does not
transfer ownership. Later successful calls reuse the parsed form and ignore the
new size argument. Do not retry parsing after an error: the body is consumed.

`RemoveAll` removes disk-backed temporary storage, not memory-backed file data.
It is safe to call repeatedly. Headers for successfully removed disk files can
no longer be opened; memory-backed files remain readable. Consume and close
uploads before cleanup, and persist files before handing work to a background
job. A removal error can leave files behind.

Automatic after-response cleanup is best-effort: its error is not returned to
the handler. If you need to observe removal failures, call `RemoveAll` yourself
and handle its error before committing the response. If body parsing succeeds
but query parsing fails, the helper attempts temporary-file cleanup before
returning; a cleanup failure is retained alongside the parsing cause.

To check an uploaded file's size, extension, and actual content type, use
`validate.File` in a schema; [Validation](../validation/advanced.md#validating-files) shows
how. To store it, pass it to the [file storage](../file-storage/overview.md)
service.

## Running code after the response

Middleware sometimes needs to act just before final response commitment or when
request handling finishes. `request.Hooks` requires the context installed by
`router.Handler.ServeHTTP` (including normal routed requests). On an arbitrary
request without that context, it panics with `request.ErrNoHooksMiddleware`;
there is no non-panicking public lookup. Register hooks only where router wiring
guarantees that context:

```go
request.Hooks(r).AfterResponse(func(err error) {
	// err is the handler's returned error, not proof of client delivery
})
```

`BeforeWriteHeader` runs once, before the final status, and can still edit
headers. Informational `1xx` statuses other than `101` do not run that hook or
commit the final response; `101 Switching Protocols` does. A body write or flush
can commit an implicit `200` first. `BeforeWrite` observes an attempted body
write, which can still fail afterward.

After-response callbacks run in reverse registration order when the router
finishes handling, including while unwinding a panic. They are not delivery
acknowledgments, and a nil error does not prove the client received the response.
A successfully hijacked connection can outlive these callbacks: the handler owns
its closure and shutdown, not the router or hook registry. See
[response commitment and ownership](responses.md#when-a-response-counts-as-written).

For every helper and option, see the
[`request` API reference](https://pkg.go.dev/arkoren.dev/core/http/request).
