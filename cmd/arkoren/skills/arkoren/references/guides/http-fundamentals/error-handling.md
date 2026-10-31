---
title: Error Handling
description: How returned errors become Problem Details responses, how to choose the status and fields, map domain errors, and keep details private.
order: 40
---

# Error Handling

## Introduction

An Arkoren router handler can return an error instead of formatting an error
response at every failure site. If the handler has not started its response,
the router turns that error into the response. By default, the response is a
[Problem Details](https://www.rfc-editor.org/rfc/rfc9457) document, the
standard JSON format for HTTP errors:

```json
{
  "type": "about:blank",
  "title": "Not Found",
  "status": 404,
  "detail": "No invoice has this ID.",
  "instance": "/invoices/7"
}
```

This central rendering gives returned errors from your code and Arkoren the
same default shape, including errors for missing routes. Handlers pass errors
outward, and middleware can map them before the router writes the response.
Adapted `net/http` handlers still write their own errors; they have no error
result for the router to render.

The examples use `problem` from `arkoren.dev/foundation/problem`, `router` from
`arkoren.dev/core/http/router`, `net/http`, and `errors`. In handler fragments,
`invoice` is your loaded invoice; `store.Find` is your application's invoice
lookup using the existing `ctx` and `id`.

## Returning a problem

The `problem` package provides ready-made problems for the common statuses:
`NotFound`, `BadRequest`, `Forbidden`, `Conflict`, `UnprocessableContent`,
`InternalServerError`, and `Unauthorized` (which takes a challenge string).
Return one from a handler, and add an explanation for the client in a `Derive`
callback. The detail is sent to the client: use a safe explanation, not raw
database errors or other internal diagnostics:

```go
if invoice.Paid {
	return problem.Conflict().Derive(func(details *problem.Builder) {
		details.Detail("The invoice is already paid.")
	})
}
```

A problem is a value. `Derive` returns a modified copy and
never changes the original, so the templates are safe to share.

### Adding fields

Clients sometimes need more than a sentence, such as the ID of the conflicting
resource. `Builder.Extension` adds an extension member, which appears as a top-level field
in the JSON:

```go
return problem.Conflict().Derive(func(details *problem.Builder) {
	details.Detail("The invoice is already paid.")
	details.Extension("invoice_id", invoice.ID)
})
```

`Builder.Header` sets a response header on the problem, such as `Retry-After`.
It copies the supplied values; passing no values removes the header.

If middleware needs to add metadata to whichever problem a handler returns, use
the request-local response context. Inside a routed handler or middleware,
`incoming` is the current `*http.Request` and `requestID` is an application-generated,
client-safe string:

```go
if metadata, ok := problem.FromContext(incoming.Context()); ok {
	metadata.SetProperty("request_id", requestID)
}
```

Context properties fill missing extensions: explicit problem extensions win,
and context properties cannot replace standard problem members.

The container's methods synchronize its outer maps and header slices.
`Properties()` copies only the outer property map; `Headers()` copies its map
and value slices. HTTP rendering also snapshots these containers, not nested
property values. Nested maps, slices, and other mutable values remain shared:
coordinate their mutation with all readers, including encoding. Keep the
`*problem.ResponseContext` as a pointer; do not copy it after first use because
it contains a mutex.

### Defining your own problems

For errors that clients should recognize, define a problem with its own `Type`
once, at package level, and return it wherever that error occurs:

```go
var ErrInvoicePaid = problem.New(func(details *problem.Builder) {
	details.Type("https://api.example.com/problems/invoice-paid")
	details.Title("Invoice already paid")
	details.Status(http.StatusConflict)
})
```

A client can then branch on the `type` field rather than parsing messages. The
type only has to be a unique URI; it does not need to resolve to a page.

`New()` defaults to status 500 and type `about:blank`. An empty title uses the
final status phrase after all callbacks. `Derive` instead inherits stored members:
changing status does not reset the title. Read members with accessors such as
`Status()` and `Title()`. `Extension(name)` reports presence separately from its
value; `Extensions()` returns a copy of the outer map.

Status helpers such as `NotFound()` also use these constructor defaults, with
their stored type set to `about:blank`. Raw zero values and decoded absent fields
retain their absence.

Callbacks run directly in order on one draft. Omitted callbacks (also expanded
nil/empty slices) make no edits; a supplied nil callback panics. Retained builders
cannot change published problems. Outer extension maps and header slices are
copied, but arbitrary nested extension values remain shared.

To recognize a problem without comparing its user-facing explanation, use
`errors.Is(occurrence, sentinel)`. It compares stored type, status, and title; detail,
extensions, headers, and cause do not change this match. Editing an identity
member changes the match. Cause traversal through `errors.Is` and `errors.As`
continues independently. A value problem matches a `problem.Details` target for
`errors.As`; a pointer matches `*problem.Details`, as with ordinary Go errors.

## Plain errors

A handler can also return any ordinary error, such as one from a database
call. The router then renders a problem with a status it chooses as follows:

- If an error in the chain provides an `HTTPStatus() int` method, its status is
  used.
- If the request context is done, the status is `499`, including when that
  context's deadline expired.
- Otherwise, an error matching `context.DeadlineExceeded` produces
  `504 Gateway Timeout`.
- Otherwise, the status is `500 Internal Server Error`.

The text of a plain error is never sent to the client. A failed query might
contain table names or data, so the client only sees the generic title of the
status, and the full error stays available to your logs and middleware.

## Keeping the cause

When you return a problem because of another error, attach that error with
`Builder.Error`:

```go
invoice, err := store.Find(ctx, id)
if errors.Is(err, database.ErrNoRows) {
	return problem.NotFound().Derive(func(details *problem.Builder) {
		details.Error(err)
	})
}
```

The cause is not included in the response, but it stays in the error chain, so
`errors.Is` and `errors.As` still find it and request logging records it.
Pass nil to `Builder.Error` to clear a previously attached cause.

## Mapping errors to responses

Converting the same domain errors in every handler gets repetitive. Import
`arkoren.dev/core/http/middleware/errormap`,
`arkoren.dev/core/http/request`, `arkoren.dev/core/service/database`, and
`arkoren.dev/core/service/storage`. With an existing router `r`, the
`errormap` middleware does it once for the whole router: it inspects every
error a handler returns and replaces the ones it recognizes:

```go
r.Use(errormap.Middleware(
	errormap.Map(database.ErrNoRows, problem.NotFound()),
	errormap.Map(storage.ErrNotFound, problem.NotFound().Derive(func(details *problem.Builder) {
		details.Detail("No such file.")
	})),
	errormap.Func(request.ValidationProblem),
))
```

`Map` matches an error with `errors.Is` and returns the problem, with the
original error attached as its cause. `Func` runs a function on the error,
which is how validation errors from schemas called directly become `422`
responses. Mappings are tried in order, and the first one that handles an error
wins; errors no mapping handles pass through unchanged.

A custom `Func` can return nil to decline an error, or return its input unchanged.
Matching the input with `errors.Is` does not make a replacement unchanged: a
derived problem with different detail, headers, or cause is handled. Do not mutate
the input. Value-error identity has limits, so return nil to decline reliably,
and use pointer errors when distinguishing replacements matters; see
[`Mapping`](https://pkg.go.dev/arkoren.dev/core/http/middleware/errormap#Mapping).

The middleware copies the ordered mapping slice when you construct it. Later
changes to that slice do not change the policy; mapping functions and their
captured values remain shared.

With this middleware, a store can return `database.ErrNoRows` and the handler
can simply pass it on:

```go
invoice, err := store.Find(ctx, id)
if err != nil {
	return err // 404 when no row was found
}
```

Register `errormap` with `Use` before other middleware, so it also sees the
errors those return.

An adapted native middleware can render an error before it reaches an outer
mapper. A mapper that must shape that HTTP response needs to be inside the
adapter; see [native middleware boundaries](middleware.md#using-nethttp-middleware).

## Errors from Arkoren

Arkoren's own errors are problems too, and they carry a `type` that identifies
them. A malformed JSON body, for example, is reported as:

```json
{
  "type": "tag:arkoren.dev,2026-09:core:http/request:invalid-json",
  "title": "Invalid JSON request body",
  "status": 400,
  "detail": "The request body must contain valid JSON.",
  "instance": "/posts"
}
```

Here the request path is `/posts`; rendering supplies it as `instance`.
These `tag:` URIs are stable identifiers that do not need to resolve to a page.
If you would rather publish your own identifiers for them, remap them with
`errormap.Map`, as for any other error.

## Content negotiation

A problem is rendered as `application/problem+json` by default. A client can
request the same document as `application/json`, or a short text representation
as `text/plain; charset=utf-8`. If no supported representation is acceptable,
the response is `406 Not Acceptable` without the original problem representation.

Missing or wholly invalid `Accept` uses the server's preference order: problem
JSON, ordinary JSON, then UTF-8 text. Invalid ranges are ignored individually.
Only an omitted `q` weight defaults to `1`. An explicit weight takes the form
`0` or `1`, optionally followed by a decimal point and up to three digits;
fractional digits after `1.` must be zeros. A malformed weight invalidates its
range rather than giving it maximum preference.

Media parameters must match the representation actually emitted. Text declares
`charset=utf-8`, so `charset=UTF-8` matches case-insensitively but
`charset=iso-8859-1` does not. Other parameter values match exactly. The JSON
representations declare no media parameters; requiring an unprovided `profile`
or `charset` does not match them.

For each representation, an exact media range outranks a type wildcard, which
outranks `*/*`. Within the same media-range specificity, more matching parameters
take precedence over fewer; higher quality breaks the remaining tie. A selected
`q=0` excludes the representation even if a broader range permits it. For example,
`text/plain;charset=UTF-8;q=0, text/plain;q=1` still excludes the UTF-8 text.
Between acceptable representations, higher selected quality wins; ties follow
the matching ranges' order in `Accept`, then the server's preference order.

### Response headers

The renderer owns `Content-Type`; problem or response-context headers cannot
override the negotiated format. Problem header settings replace all context
values for the same header.

After those replacements, the renderer adds `Accept` to the resulting `Vary`
unless it already contains an `Accept` token, matched case-insensitively, or `*`.
Other remaining tokens are preserved, including on a `406` response. This does
not merge back contextual tokens replaced by an explicit problem header.

### JSON and HTTP status defaults

Plain JSON encoding omits unspecified status. Decoding preserves missing status
as zero and replaces the destination only on success, clearing previous causes
and headers. HTTP rendering completes missing standard members on a response
copy, with missing status defaulting to `500`. A nonzero stored status outside
`100` through `999` also becomes `500`, but only in that HTTP copy; negotiation
can still return `406` if no representation is acceptable.

For example, a stored status of `1000` remains `1000` through `Status()`,
`HTTPStatus()`, plain JSON, and `Defaulted`. `Defaulted` fills missing members;
it does not validate an existing nonzero status. Rendering does not change the
stored problem's matching identity or wrapped cause.

Encoding completes before commitment. A JSON encoding failure falls back to
text only if that text representation is acceptable under the same range and
parameter rules; otherwise rendering returns `406`. A body-write failure after
commitment stops rendering rather than trying another representation.

## Panics

A handler panic needs recovery before it can follow the returned-error path.
Before registering routes on your router `r`, add recovery outside the handlers
and middleware it must protect:

```go
r.Use(router.Recover())
```

An ordinary panic before the response starts becomes an error for normal
rendering, usually a generic `500` problem. `RecoverBuilder.Handler` lets you
choose a different conversion. Setting `Handler(nil)` selects the default
converter; if a supplied converter returns nil, recovery still returns an error
matching `router.ErrRecoverUnexpected`, not success.

If `HTTPMiddleware` renders a recovered error before returning it outward, that
generated problem is not mistaken for output that preceded the panic: the
response is not aborted merely because recovery has now written it. Errors can
be wrapped during this handling; use `errors.Is` and `errors.As` to retain cause
and diagnostic access.

Set `Events(bus)` in the recovery builder callback to publish
[`router.PanicRecovered`](../events/http-events.md#recovered-panics). Its `Err`
and `Stack` intentionally carry sensitive diagnostics, not client-safe data.
Restrict listener output and sanitize anything forwarded to an error tracker.
`http.ErrAbortHandler` is re-raised and publishes no recovery event.

Recovered errors expose `Stack() []byte` through `errors.As` with
`interface{ Stack() []byte }`. Each getter call returns a copy, and the event's
stack storage is separate from the error's. Listeners of the same event still
share its payload bytes: copy `Stack` before mutating it so later listeners do
not see your changes. Copying does not redact either the stack or the error.

## After the response has started

A response is **committed** when a final status is written, explicitly or through
a body write or flush. Informational `1xx` responses other than
`101 Switching Protocols` leave the final response pending; `101` is final for
this purpose. A successful connection hijack also prevents further HTTP error
rendering; a failed hijack does not itself commit the response.

After commitment, an ordinary returned error still reaches outer middleware and
after-response hooks, but the router does not append a replacement problem. A
recovered panic that occurred after commitment instead raises
`http.ErrAbortHandler` so `net/http` can abort the HTTP response. A new panic is
not considered handled merely because its cause contains an older, already
rendered recovery error.

If your handler successfully hijacked the connection, you own and must close
it; recovery does not transfer that ownership back to the HTTP server. Do the
work that can fail before writing the response whenever possible.

For every method, see the [`problem` API reference](https://pkg.go.dev/arkoren.dev/foundation/problem),
the [`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router), and
the [`errormap` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/errormap).
