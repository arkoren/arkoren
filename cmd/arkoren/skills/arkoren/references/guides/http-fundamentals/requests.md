---
title: Requests
description: Read typed request inputs, decode bounded bodies, and preserve input errors and upload ownership.
order: 20
summary:
  - Parse URL values and distinguish invalid input from optional defaults.
  - Decode JSON into request structs and combine decoding with an existing validation schema.
  - Read XML, raw bodies, and multipart forms with explicit size and cleanup boundaries.
---

# Requests

Client input needs parsing before your handler can use it. The `request`
package adds typed URL values and bounded body readers to `*http.Request`.
Input failures return problems that the router can render; operational failures
remain errors for server-side handling.

Import `arkoren.dev/core/http/request`. These fragments run inside an
[error-returning handler](routing.md#register-handlers), where `r` is the incoming
request. Return errors before writing a response or using a failed result.

## Read URL Values

For `/invoices/{id}`, parse the captured value into the type your application
uses. Named scalar types work too:

```go
type InvoiceID int64

id, err := request.Param[InvoiceID](r, "id")
```

`Param` returns a `400` problem for missing, empty, malformed, or out-of-range
values. It supports strings, booleans, integer types other than `uintptr`, and
floating-point types, including named versions. For unparsed text, use
`r.PathValue("id")`. A parsed ID still needs resource authorization.

Query parameters use the same scalar conversion. A required count should fail
when malformed; an optional display choice can use a fallback:

```go
limit, err := request.Query[int](r, "limit")
sort := request.QueryOr(r, "sort", "issued_at")
```

`Query` reads the first value and returns a `400` problem on failure.
`QueryOr` falls back for absent, empty, and invalid values; `ParamOr` does the
same for path values. Use `HasQuery(r, "preview")` to test presence independently
of value, including `?preview` or `?preview=`. Use an allowlist before turning a
sort choice into a database identifier. [Pagination](../queries/pagination.md)
owns `request.Page` and `request.Cursor`.

URL decoding follows `net/url`, including omission of malformed query pairs.
Conversion follows `strconv`: booleans accept `1`, and floats accept `NaN` and
infinities. Apply [validation](../validation/overview.md) for finite numbers,
ranges, or stricter grammar. OpenAPI parameter descriptions do not enforce them.

## Read Request Metadata

To read an optional client language preference:

```go
language := request.HeaderOr(r, "Accept-Language", "en")
```

`Header` returns an empty string when missing; `HeaderOr` also falls back for
empty values. These read text, not a verified identity or negotiated locale;
[locale selection](../http-capabilities/localization.md#select-languages) owns language negotiation.

`Cookie` returns the whole cookie or `nil`; `CookieValue` returns its value,
and `CookieValueOr` supplies a fallback. Cookie and header values remain
untrusted. Use the [session capability](../http-capabilities/sessions.md) rather
than treating an arbitrary cookie value as authentication.

## Decode JSON

JSON-looking bytes can arrive as `text/plain`, including in a cross-site
browser post. Body decoders do not check `Content-Type`; apply
[Content Types](content-type.md) before decoding, and use
[CSRF protection](../browser-security/csrf-protection.md) where cookies authorize
browser requests.

Use a request-specific struct so the endpoint exposes only intended writable
fields. For an invoice draft:

```go
type CreateInvoice struct {
	CustomerID string `json:"customer_id"`
	Reference  string `json:"reference"`
}
```

Decode the struct, rejecting misspelled or extra members when the endpoint has
a fixed input shape:

```go
input, err := request.JSON[CreateInvoice](r,
	request.Strict(),
	request.MaxSize(64<<10),
)
```

This reads at most 64 KiB under the helper's cap. Without options, body helpers
use 10 MiB or the limit stored by [Body Limits](body-limit.md). An upstream
reader's ceiling cannot be raised by a per-call option. Limits bound bytes
read, not decoded memory or later application work. Call one body decoder per
request: the body is a consumed stream, not a reusable value.

The struct limits writable fields; it does not authorize the supplied customer.
Check that the caller may create invoices for `input.CustomerID` before writing
application data.

`JSON` returns `request.ErrInvalidJSON` (`400`) for native input failures and
`request.ErrBodyTooLarge` (`413`) when reading exceeds a cap. Unknown members
are ignored without `Strict`. JSON-v2 defaults make names case-sensitive,
reject duplicate members and a second top-level value, and reject fractions,
exponents, or overflow for native integer fields.

Ordinary custom decoder and body-read errors are not automatically bad input.
They keep their causes; existing HTTP problems retain their identity. A custom
decoder must deliberately identify client-input failures rather than exposing
arbitrary callback diagnostics. [Plain Errors](error-handling.md#plain-errors)
explains the router's private default rendering.

## Validate Input

Decoding establishes a Go value, not a valid invoice. With an existing
`validate.Schema[CreateInvoice]` named `invoiceInput`, combine decoding and
schema preparation:

```go
input, err := request.ParseJSON(r, invoiceInput,
	request.Strict(),
	request.MaxSize(64<<10),
)
```

`ParseJSON` returns the schema's prepared copy, including normalizations and
defaults. `ValidateJSON` checks the value but returns the decoded value instead.
Define schemas during application setup; [Validation](../validation/overview.md)
owns their construction and [definition errors](../validation/errors.md#definition-errors).

Schema violations become `request.ErrValidationFailed` (`422`) with an `errors`
member keyed by JSON Pointer. Pointers use wire member names, not Go selectors:
`json:"a/b~name"` becomes `/a~1b~0name`. Native semantic decoding failures also
become `422` violations with code `decode`; strict unknown members use `unknown`.
Syntax failures, including duplicate names, remain `400`.

Native decoding stops at its first semantic failure. Schema checks run on the
partially decoded value with presence-rule violations suppressed, so a response
is not an exhaustive list of malformed members. Fix the reported value and
resubmit. Schema infrastructure errors take precedence over field feedback;
ordinary application decoder failures remain operational errors.

A `decode` violation has no `params.expected`. Its optional
`params.default_representation` describes the Go type's default, not the required
wire format: an integer tagged `,string` still needs a quoted number. Custom
JSON methods, unsupported kinds, and `time.Duration` have no such hint. Keep
native causes in server diagnostics, not client detail. Use the current
[message catalog guidance](../validation/errors.md#translate-messages) to translate
`decode`, and the [diagnostic ownership rules](../validation/errors.md#diagnostic-ownership)
when recording causes.

Clients migrating from older native-decoding feedback must replace assumptions
about code `type` and `params.expected` with `decode` and the qualified default
hint. This does not change schema-generated codes, the `unknown` code, the
top-level validation problem identity, or its `422` status.

JSON-v2 supports `omitzero`, `omitempty`, `string`, `case`, and `embed` options;
the omission options do not change decoding, and `string` applies to a field's
top-level number, not composite elements. It does not support `format:*` tags.
`time.Duration` needs an explicitly represented input plus application conversion.
Native decoding support does not imply schema support: explicit validation field
registration rejects `embed`, `inline`, and `unknown` layouts. See
[struct validation](../validation/structs-and-reuse.md).

## Read Other Bodies

For a small XML acknowledgment, choose the wire shape explicitly:

```go
type Acknowledgment struct {
	Reference string `xml:"reference"`
}

ack, err := request.XML[Acknowledgment](r, request.MaxSize(16<<10))
```

`XML` decodes only the first element and ignores trailing data. Native XML
syntax and numeric-conversion errors remain `request.ErrInvalidXML` (`400`),
including through `ValidateXML` and `ParseXML`; there is no native JSON Pointer
for field feedback. Operational decoder and read errors retain their causes.

`Bytes` and `String` read without decoding; `String` does not validate UTF-8.
`Strict` is unsupported for XML and raw bodies and returns
`request.ErrStrictUnsupported` before reading. A nil body is empty input: raw
reads return no data, while JSON and XML return their malformed-body problems.
For signed payloads, use the [webhook receiver](../webhooks/receiving-webhooks.md)
rather than decoding before signature verification.

## Parse Uploads

Allow `multipart/form-data` on upload routes before using `Multipart`. Its
size argument caps the whole body, not each file:

```go
form, err := request.Multipart(r, 12<<20)
if err != nil {
	return err
}
attachment := form.File("attachment")
```

`attachment` is a `*multipart.FileHeader`, or `nil` when absent.
`form.Value("reference")` reads the first text value. `Values` and `Files` keep
all submitted values in order. Validate content and size with
[file upload rules](../validation/available-methods.md#file-uploads); storage
transfer belongs in [File Storage](../file-storage/overview.md).

Up to 8 MiB of file content stays in memory; larger files spill to temporary
disk storage. Oversized, non-multipart, and malformed input produce `413`, `415`,
and `400` problems respectively. Transport and temporary-file I/O failures
remain operational errors. Existing HTTP problems preserve their identity.

When this helper parses inside a router handler lifecycle, it registers one
after-response cleanup hook. Outside that lifecycle, call `form.RemoveAll()`
after use. A form parsed elsewhere retains its existing cleanup owner. Successful
later calls reuse the parsed form and ignore their size argument; never retry
after a parsing failure because the stream has been consumed.

Consume and close opened uploads before cleanup; persist them before giving
work to a background job. `RemoveAll` deletes disk-backed temporary files, not
memory-backed data, and is safe to repeat. Removal failures can leave files
behind. Automatic cleanup is best-effort and does not return its error to the
handler; explicitly call `RemoveAll` before commitment if your application must
observe that failure. If query parsing fails after successful body parsing,
temporary-file cleanup is attempted and any cleanup failure stays in the error.

For all readers and body options, see the
[`request` API reference](https://pkg.go.dev/arkoren.dev/core/http/request).
