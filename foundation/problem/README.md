# Arkoren Problem

Clients need a consistent error format rather than a new response shape for each
endpoint. `arkoren.dev/foundation/problem` is a dependency-free Go implementation of RFC 9457,
Problem Details for HTTP APIs. A `Details` value implements both `error` and
`http.Handler`, so it can represent and serve a structured HTTP error response.

## Installation

```sh
go get arkoren.dev/foundation/problem
```

## Usage

Define a reusable error, then derive request-specific details without changing
the template. Import `arkoren.dev/foundation/problem` and the standard-library
`net/http` package:

```go
var ErrNotFound = problem.New(func(details *problem.Builder) {
	details.Type("https://api.example.com/problems/not-found")
	details.Title("Resource Not Found")
	details.Status(http.StatusNotFound)
})

func handler(w http.ResponseWriter, r *http.Request) {
	ErrNotFound.Derive(func(details *problem.Builder) {
		details.Extension("resource_id", "123")
	}).ServeHTTP(w, r)
}
```

The handler serves HTTP 404 with a `resource_id` extension. Store or return
construction callbacks as `problem.BuilderFunc`, an alias for
`func(*problem.Builder)`. Both `New` and `Details.Derive` accept this type.

Only put client-safe text in titles, details, and extensions. Attach internal
causes with `details.Error(err)` so `errors.Is` can inspect them without exposing
their text in the response.

`New()` defaults status to 500, type to `about:blank`, and an empty title to the
final status phrase after all callbacks. `Derive` inherits stored fields without
normalization: changing status does not replace an inherited title. Read standard
members with `Type()`, `Title()`, `Status()`, `Detail()`, and `Instance()`.

When served, unset `Type`, `Status`, `Title`, and `Instance` members default to
`"about:blank"`, `500`, its HTTP status text, and the request URL path,
respectively. `Detail` is never inferred from a wrapped error.

Callbacks run directly, once, synchronously, in order on one draft. Omitted or
expanded nil/empty callback slices request no edits; a supplied nil callback
panics. Retaining a builder cannot modify the published value. Outer extension
maps and header slices are copied, but arbitrary nested extension values remain
shared. `Extension(name)` returns a value and presence flag (including stored
nil); `Extensions()` returns an independent outer map.

JSON decoding preserves missing status as zero and replaces the destination only
on success, clearing old causes and headers. Plain JSON omits zero status; HTTP
rendering completes defaults and includes a status matching the response code.
Construction and decoding retain nonzero statuses without validation. When a
stored status falls outside `net/http`'s three-digit range (100–999), only
`ServeHTTP` renders it as 500. Accessors, plain JSON, `Defaulted`, and
`errors.Is` still observe the original stored identity.

## Status problems

Status helpers such as `NotFound()` use `New`, so their stored type is
`about:blank`, not an absent type. They match equivalent `New` constructions
through `errors.Is`, which compares the exact stored type, status, and title.
Deriving a cause or detail does not change that identity. Raw zero values and
decoded absent members still retain their absence until HTTP defaulting.

Standard HTTP status problems are directly returnable from an Arkoren handler or
servable as an `http.Handler`:

```go
func handler(w http.ResponseWriter, r *http.Request) error {
	return problem.NotFound()
}

func protected(w http.ResponseWriter, r *http.Request) {
	problem.Unauthorized(`Basic realm="api"`).ServeHTTP(w, r)
}
```

`problem.Unauthorized` requires a caller-supplied, non-empty, syntactically
valid HTTP authentication challenge. It preserves that challenge in the
`WWW-Authenticate` response header.

Response headers are immutable, non-serialized response state. In a `Derive`
callback, use `Header` to replace copied values, `AddHeader` to append a value,
or `RemoveHeader` to remove it. `Header(name)` also removes it:

```go
problem.BadRequest().Derive(func(details *problem.Builder) {
	details.Header("Cache-Control", "no-store")
	details.AddHeader("Vary", "Accept-Language")
})
```

`ServeHTTP` owns `Content-Type`; it replaces any stored value with the
negotiated Problem Details, JSON, or text content type. It also adds `Accept`
to `Vary` after applying your headers, unless an existing case-insensitive
`Accept` token or `*` already covers negotiation. Other `Vary` values are retained.

## Content negotiation

`ServeHTTP` considers `application/problem+json`, `application/json`, and
`text/plain`, in that server preference order. When no syntactically valid
`Accept` value is supplied, it selects `application/problem+json`. For a
valid header, the most-specific matching range controls a representation; an explicit
`q=0` excludes it even when a less-specific wildcard permits it. If the client
excludes every supported representation, `ServeHTTP` sends HTTP 406 without
rendering the original problem. If a JSON extension value cannot be marshaled,
the response falls back to plain text only when `text/plain` is acceptable;
otherwise it sends HTTP 406.

An omitted `q` means quality 1; a malformed weight invalidates that range.
Weights use HTTP's 0–1 grammar with at most three decimal places, not arbitrary
floating-point notation. If every range is invalid, the normal server order
applies. Media parameters must match the actual representation. The text
renderer emits `text/plain; charset=utf-8`, so another required charset is not
acceptable. A parameterized matching range outranks an otherwise equal plain
range, including a parameterized `q=0` exclusion. Unprovided JSON profile
parameters do not match either JSON renderer.

Encoding finishes before response headers are committed. A body-write error
stops rendering without attempting another representation after commitment;
the `http.Handler` interface cannot return that write error to the caller.

## Validation errors

`problem` has no validation type. `arkoren.dev/foundation/validate` produces
`validate.Errors`, and `request.ValidationProblem` in the core module turns
them into a 422 problem with an `errors` extension. The Arkoren router does
not render a raw `validate.Errors`; it answers 500.

## Request response context

Middleware can attach one mutable `ResponseContext` with RFC 9457 extension
members and baseline response headers. Create it, attach it to the request, and
then mutate it with per-field methods:

Here `incoming` is the existing `*http.Request` passed to your middleware:

```go
metadata := problem.NewResponseContext()
metadata.SetProperty("request_id", "123")
metadata.SetHeader("Cache-Control", "no-store")
metadata.AddHeader("Vary", "Accept-Language")

incoming = incoming.WithContext(problem.NewContext(incoming.Context(), metadata))
```

`Properties()` copies only the outer map. Replacing an entry in that snapshot
cannot change the context, but mutating a nested map or slice can. Coordinate
nested mutation with all readers, including response encoding; the container's
mutex does not protect nested values. `Headers()` copies both the outer
`http.Header` and its value slices. Do not copy a `ResponseContext` after use.

When served, standard RFC 9457 member names (`type`, `title`, `detail`,
`status`, and `instance`) from a response context are ignored. Explicit
`Builder.Extension` values take precedence over same-named context properties.
Context headers are written first; `Builder.Header` and
`Builder.AddHeader` replace every contextual value for the same field.
`ServeHTTP` owns `Content-Type` and adds the required `Accept` variance after
these replacements, so contextual or problem headers cannot disable either.

## Features

- RFC 9457 standard members plus top-level extensions via `Builder.Extension`.
- Immutable published values, allowing reusable package-level problem templates.
- Wrapped native errors via `Builder.Error`, available through `errors.Is` and
  `errors.As` but never serialized.
- `errors.Is(err, template)` matches a problem with the same type, status and
  title as `template`, ignoring detail, members, headers and cause.
- Content negotiation for `application/problem+json`, `application/json`, and a
  plain-text fallback.
- Request-scoped properties and response headers through `ResponseContext`.

## Development

Go 1.27 or later is required. Run the checks directly:

```sh
go test -race ./...
go vet ./...
govulncheck ./...
```

## License

MIT. See [LICENSE](LICENSE).
