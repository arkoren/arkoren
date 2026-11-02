---
title: Error Handling
description: Turn returned errors into client-safe Problem Details and preserve causes for diagnostics.
order: 40
summary:
  - Distinguish router-selected statuses from explicit problems and custom error renderers.
  - Define stable client problems and map domain failures without exposing diagnostic causes.
  - Understand negotiated error output, panic recovery, and failures after commitment.
---

# Error Handling

An Arkoren handler returns a failure instead of formatting an error response at
each call site. While the final response is pending, the router renders the
error, normally as [Problem Details](https://www.rfc-editor.org/rfc/rfc9457).
Keep fallible work before the response helper: after commitment, returning an
error reports the failure but cannot replace output already sent.

This applies to error-returning handlers, including router-generated `404` and
`405` problems. Adapted `net/http` handlers write their own errors. Import
`net/http`, `errors`, and `arkoren.dev/foundation/problem` for the examples below.

## Plain Errors

You can return an ordinary service error unchanged. The router does not send
its text to the client, since diagnostics can disclose data or implementation
details. Without an error-owned renderer, status selection follows this order:

1. An error in the chain implementing `HTTPStatus() int` supplies its status.
2. Otherwise, a finished request context selects `499`, even for its deadline.
3. Otherwise, an error matching `context.DeadlineExceeded` selects `504`.
4. All other errors select `500`.

`499` is a nonstandard classification; a disconnected client might receive no
response. An error implementing both `error` and `http.Handler` instead renders
itself before commitment. That implementation owns negotiation and client-safe
output; it is not wrapped in another generated problem. `problem.Details`
implements both rendering and status interfaces.

Raw validation errors have no HTTP meaning by themselves. Use
`request.ValidationProblem` or the mapping below to convert violations to
`422`; do not map infrastructure failures into field advice. See
[Classify Failures](../validation/errors.md#classify-failures).

## Return Problems

For a failure the client can act on, return a problem with an explicit safe
explanation. Inside an invoice handler with a loaded `invoice`:

```go
if invoice.Paid {
	return problem.Conflict().Derive(func(details *problem.Builder) {
		details.Detail("A paid invoice cannot be edited.")
	})
}
```

The status helper supplies `409 Conflict`. `Derive` produces a modified value
without changing the template. The response includes `type`, `title`, `status`,
and your `detail`; rendering fills an absent `instance` from the request path.
Other status helpers include `BadRequest`, `NotFound`, `Forbidden`, and
`UnprocessableContent`. `Unauthorized` takes the authentication challenge string.

If another failure caused this result, retain it as a diagnostic cause, not
as the client explanation. Here `err` is an existing application error:

```go
return problem.Conflict().Derive(func(details *problem.Builder) {
	details.Detail("The invoice changed. Reload it before editing.")
	details.Error(err)
})
```

The cause is excluded from the response but remains reachable through
`errors.Is` and `errors.As`. `Error(nil)` clears a cause. Problem `Error()` text
can include that cause, so it belongs in restricted server diagnostics, not in
another response's detail.

## Define Stable Problems

If clients distinguish an application failure, define its identity once:

```go
var ErrInvoiceClosed = problem.New(func(details *problem.Builder) {
	details.Type("https://api.example.com/problems/invoice-closed")
	details.Title("Invoice closed")
	details.Status(http.StatusConflict)
})
```

Return `ErrInvoiceClosed` or derive occurrence-specific details from it. Clients
can branch on `type` rather than parsing a sentence. The type must be a unique
URI reference; it need not resolve to a page. Arkoren's own failures use stable
`tag:arkoren.dev,...` identifiers and can be remapped the same way as domain errors.

`errors.Is(err, ErrInvoiceClosed)` compares stored type, status, and title.
Detail, extensions, headers, and cause do not alter this identity; editing an
identity member does. Cause traversal remains independent. For `errors.As`, a
value problem matches `problem.Details`, while a pointer matches
`*problem.Details`, following ordinary Go rules.

`New` defaults missing status to `500`, type to `about:blank`, and an empty
title to the final status phrase. `Derive` inherits stored members: changing
status alone does not reset an existing title. Callbacks run directly in order;
omitting callbacks makes no edits, but a supplied nil callback panics. Retaining
a builder cannot alter the published value. Outer extension maps and header
slices are copied; nested mutable values remain shared.

## Map Domain Errors

Central mapping keeps HTTP choices out of domain services. With an existing
router `routes` and application sentinel `ErrInvoiceVersion`, import
`arkoren.dev/core/http/middleware/errormap` and
`arkoren.dev/core/http/request`:

```go
routes.Use(errormap.Middleware(
	errormap.Map(ErrInvoiceVersion, problem.Conflict()),
	errormap.Func(request.ValidationProblem),
))
```

Register this before routes and before middleware whose errors it must see.
`Map` uses `errors.Is` and attaches the original error as the replacement's
cause. `Func` performs a custom conversion and owns cause preservation itself;
`ValidationProblem` already retains the cause and leaves non-validation errors
unchanged. Request `Parse*` and `Validate*` helpers perform that conversion already.

Mappings try the original error in order; the first replacement wins. Return
nil to decline reliably. Returning the unchanged input also declines, but
separately constructed equal value errors can be indistinguishable. Do not
mutate inputs, and use pointer errors when replacement identity matters.
Matching through `errors.Is` alone does not make a derived replacement unchanged.
The ordered slice is copied at construction; functions and captures stay shared.

A mapper cannot rewrite a response already rendered by a
[native middleware adapter](middleware.md#native-middleware). Put a mapper
inside that adapter if it must choose the rendered HTTP result; it can then see
only failures from layers inside it.

## Add Client Metadata

Use extensions for small, client-safe fields, not arbitrary diagnostic objects:

```go
return ErrInvoiceClosed.Derive(func(details *problem.Builder) {
	details.Extension("invoice_id", invoice.ID)
	details.Header("Cache-Control", "no-store")
})
```

Extensions appear at the top level of JSON. `Header` copies the supplied values;
passing no values removes that header. Accessors such as `Extension(name)`
report presence separately from its value; `Extensions()` copies the outer map.

For metadata applying to any problem from a request, the router supplies a
request-local response context. With a client-safe application `requestID`:

```go
if metadata, ok := problem.FromContext(r.Context()); ok {
	metadata.SetProperty("request_id", requestID)
}
```

Explicit problem extensions win over context properties; context properties
cannot replace standard members. Context methods synchronize outer maps and
header slices, and rendering snapshots them. Nested maps and slices still need
caller coordination during encoding. Keep the context as a pointer: it contains
a mutex and must not be copied after use.

## Negotiate Error Output

The renderer prefers `application/problem+json`, then `application/json`, then
`text/plain; charset=utf-8`. Missing or wholly invalid `Accept` uses that order.
A valid header excluding all supported representations produces `406` without
the original problem representation.

For each representation, exact ranges outrank type wildcards, which outrank
`*/*`; more matching parameters then outrank fewer, followed by quality. A
selected `q=0` excludes that representation even if a broader range permits it.
Across representations, higher quality wins, then matching-header order, then
server preference. Invalid ranges are ignored individually. An omitted `q`
means `1`; explicit weights must be `0` or `1`, optionally with up to three
fractional digits, all zeros after `1.`.

Parameters must match what is emitted. Text matches UTF-8 charset
case-insensitively, not another charset; other parameter values match exactly.
The JSON representations declare no parameters, so a required `charset` or
`profile` does not match them. For example,
`text/plain;charset=UTF-8;q=0, text/plain;q=1` still excludes UTF-8 text.

The renderer owns `Content-Type`. Problem headers replace context headers of
the same name; afterward `Vary` gains `Accept` unless it already contains that
token case-insensitively or contains `*`. Other surviving tokens remain,
including on `406` responses.

HTTP rendering completes missing standard members on a copy and changes stored
statuses outside `100`–`999` to `500` in that copy only. Stored accessors, plain
JSON, matching identity, and cause remain unchanged. Plain JSON omits an absent
status; decoding preserves absence as zero, replaces the destination only on
success, and clears previous causes and headers. `Defaulted` fills missing
members but does not validate a nonzero status.

Encoding finishes before commitment. If JSON encoding fails, text is used only
when acceptable under the same negotiation rules; otherwise the result is `406`.
A body-write failure stops rendering, not a second representation attempt.

## Panics

Install recovery before registering the handlers and middleware it must protect.
Import `arkoren.dev/core/http/router`:

```go
routes.Use(router.Recover())
```

Before output starts, an ordinary panic becomes an error for normal rendering,
usually a generic `500`. `RecoverBuilder.Handler` supplies a custom conversion;
`Handler(nil)` chooses the default. A converter returning nil still produces
an error matching `router.ErrRecoverUnexpected`, never success.

`RecoverBuilder.Events` publishes `router.PanicRecovered` through your event
bus. Its error and stack contain sensitive diagnostics. Restrict listeners and
sanitize forwarded reports. Recovered errors expose `Stack() []byte` through
`errors.As`; each call copies the stack, separately from the event's storage.
Listeners of the same event share its bytes, so copy before mutating. Copying
does not redact data. `http.ErrAbortHandler` is re-raised without a recovery event.

## Committed Failures

A final status, body write, or flush commits the response. Informational `1xx`
except `101` does not. A successful hijack also prevents further HTTP rendering;
a failed hijack does not itself commit. Use the supplied writer to preserve
[commitment tracking](responses.md#response-commitment).

After commitment, ordinary returned errors still reach outer middleware and
after-response hooks, but no replacement problem is appended. A recovered panic
after earlier output instead raises `http.ErrAbortHandler` so `net/http` can
abort the response. This is distinct from a native adapter rendering a recovered
error before any earlier output: that newly generated problem is not mistaken
for output preceding the panic. Wrapping such an error retains `errors.Is` and
`errors.As`; a new panic is not handled merely because its cause contains an
older rendered recovery.

Hijacked connections remain yours to close and drain. Recovery does not return
ownership to the server. See [Streaming Responses](streaming-responses.md) for
cooperative producers, write deadlines, and live-output failures.

For all constructors and contracts, see the
[`problem`](https://pkg.go.dev/arkoren.dev/foundation/problem),
[`router`](https://pkg.go.dev/arkoren.dev/core/http/router), and
[`errormap`](https://pkg.go.dev/arkoren.dev/core/http/middleware/errormap) API references.
