---
title: Responses
description: Return ordinary handler results as JSON, text, HTML, bytes, or redirects without hiding response failures.
order: 30
summary:
  - Encode JSON and templates before committing the response.
  - Choose explicit output media types and validate user-controlled redirect targets.
  - Distinguish preparation failures from write failures after commitment.
---

# Responses

Once a handler has finished its work, send the result through its supplied
`http.ResponseWriter`. The `response` helpers set the format and status and
return any error, so the helper can be the handler's final call. Finish fallible
application work first: a returned error cannot replace a response already started.

Import `net/http` and `arkoren.dev/core/http/response`. These fragments assume an
Arkoren handler with writer `w` and incoming request `r`. For data delivered over
time rather than one complete result, use [Streaming Responses](streaming-responses.md).

## Return JSON

Give clients a deliberate response shape rather than serializing an internal
model with private fields. For an existing invoice ID:

```go
type InvoiceCreated struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

return response.JSON(w, http.StatusCreated, InvoiceCreated{
	ID:     invoiceID,
	Status: "draft",
})
```

The helper encodes using JSON-v2, sets `Content-Type: application/json`, and
writes the document followed by a newline. Encoding finishes before the final
status is written. An encoding failure therefore leaves the router free to
render an error response; a later network-write failure does not.

Native collection and pagination values use the same helper. Lazy collections
are materialized for encoding: they must be finite and fit in memory. A source
error leaves the response uncommitted, but work already performed by that source
is not rolled back. `response.XML` likewise buffers its encoded document before
writing and uses `application/xml`.

## Send Text

For a plain acknowledgment, use an explicit text response:

```go
return response.String(w, http.StatusOK, "Invoice accepted")
```

This sets `text/plain; charset=utf-8`, replacing an existing media type. It does
not validate or transform the supplied text. If no body is needed, write only
the final status:

```go
return response.Status(w, http.StatusNoContent)
```

The `204` is still a committed response. An informational status such as `103`
is not final; send a final response afterward. An Arkoren handler returning
`nil` without a final response defaults to `204`, unlike an adapted native
handler's `200` default.

## Render HTML

Use `html/template` when a page contains user-provided values. With an existing
parsed `*html/template.Template` named `invoicePage`, pass values as data, not
as template source:

```go
type InvoicePage struct {
	Reference string
}

return response.HTMLTemplate(w, http.StatusOK, invoicePage,
	InvoicePage{Reference: reference},
)
```

Contextual HTML escaping protects the reference when the template uses ordinary
strings. Do not mark untrusted values as trusted template HTML. Rendering
finishes in a buffer before commitment; execution failures return without
starting the response. Success sets `text/html; charset=utf-8`.

`response.HTML` writes its input unchanged: reserve it for trusted HTML you
produced yourself, never concatenated user input. `StringTemplate` uses
`text/template` without HTML escaping and sets `text/plain; charset=utf-8`.
Both template helpers replace a pre-existing media type. For another format,
render into your own buffer and send the bytes with `Raw`.

## Send Bytes

For already encoded content, set a known media type and use `Raw`. With an
existing PNG byte slice `preview`:

```go
w.Header().Set("Content-Type", "image/png")
return response.Raw(w, http.StatusOK, preview)
```

`Raw` keeps an explicit `Content-Type`; otherwise it uses
`application/octet-stream` rather than sniffing bytes. `Bytes` always sets
`application/octet-stream`, replacing an earlier media type. Do not let an
untrusted upload choose an active response type such as HTML. See
[file downloads](../file-storage/uploads-and-downloads.md) for storage transfer
and download handling rather than loading large files into a response buffer.

## Redirect Safely

For a fixed application-owned path after a successful POST:

```go
return response.Redirect(w, http.StatusSeeOther, "/invoices")
```

`Redirect` sets `Location` and the supplied status without a response body. It
does not validate the target. A user-controlled return path instead needs
`SafeRedirect`; import `arkoren.dev/core/http/request` to read it:

```go
next := request.QueryOr(r, "next", "/invoices")
return response.SafeRedirect(w, http.StatusSeeOther, next)
```

Only a relative path starting with exactly one `/` is accepted. Absolute URLs,
network-path targets such as `//other.example`, backslashes, and URL parse
failures return `response.ErrUnsafeRedirect` (`400`) before commitment. The
router renders that error; the helper does not write the rejection itself.
`ValidateRedirectTarget` provides the same check when validation must happen
before another operation.

## Response Commitment

A response is **committed** when a final status is written, explicitly or
implicitly through a body write or flush. Informational `1xx` statuses other
than `101 Switching Protocols` leave the final response pending. `101` is final
for commitment tracking.

Buffered encoding and template preparation can fail while the response is still
pending. Once a final status is written, returned errors still reach outer
middleware and after-response hooks, but the router neither replaces the
response nor appends a problem body. Accepted bytes cannot be undone, even if a
later write fails. [Error Handling](error-handling.md#committed-failures) explains
panic recovery at this boundary; [Streaming Responses](streaming-responses.md#committed-failures)
explains live-output failures.

Always use the supplied writer for normal HTTP output. Writing through an
unwrapped native writer bypasses hooks and commitment tracking. A successful
connection hijack suppresses further error rendering and the default `204`;
a failed hijack does not itself commit. You own a hijacked connection, including
closing it and arranging shutdown. Router recovery does not take it back.

For all output helpers, see the
[`response` API reference](https://pkg.go.dev/arkoren.dev/core/http/response).
