---
title: Content Type Checks
nav: Content Type
description: Reject request bodies whose media type an endpoint does not accept, which also blocks cross-site form posts to JSON APIs.
order: 70
---

# Content Type Checks

## Introduction

A JSON API expects JSON. The `request` body helpers decode whatever bytes
arrive, without looking at the `Content-Type` header, so a request that claims
to be a form or plain text but contains JSON would still be accepted. That
matters for security: a web page on another site can make a browser send a
`text/plain` or form-encoded `POST` to your API without a CORS preflight,
and the browser may attach your site's cookies when their scope and `SameSite`
rules allow it.

The `contenttype` middleware rejects requests whose body has a media type the
endpoint does not accept.

## Usage

Import `arkoren.dev/core/http/middleware/contenttype`. With an existing
[router](../http-fundamentals/routing.md) `r`, add the middleware before the
routes that accept JSON:

```go
r.Use(contenttype.Middleware())
```

By default, it allows `application/json` and any type with the `+json`
suffix, such as `application/merge-patch+json`. If a request carries a body,
the middleware rejects it with `415 Unsupported Media Type` when its
`Content-Type` is disallowed, malformed, or missing. Bodyless requests follow
the rules below; a bodyless `GET` does not need this header.

## What is checked

Only requests that could carry data are checked. A request with a body must
declare an allowed type. A request without a body passes, unless it uses a
method in the checked set (`POST`, `PUT`, `PATCH`, `DELETE`, `QUERY`) and declares a type
that is not allowed: an empty cross-site form post still says `text/plain`,
and is rejected. `GET` and `HEAD` requests without a body always pass.

Matching ignores case and valid parameters such as `charset=utf-8`. Malformed
parameters are still rejected.

## Accepting other types

For a router without the global JSON policy above, restrict a login route to
form posts instead. `login` is your existing `router.Handler`:

```go
r.With(contenttype.Middleware(func(content *contenttype.Builder) {
	content.Allowed("application/x-www-form-urlencoded")
})).Post("/login", login)
```

`Allowed` replaces the default list; add `application/json` and `+json` too if
the route should accept both forms and JSON. An entry that starts with `+`, such as `+xml`, allows every type with that
suffix. File uploads use `multipart/form-data`, which you must allow on the
upload routes.

> [!NOTE]
> A JSON-only policy blocks cross-site form posts with disallowed media types.
> Allowing form media types also allows those bodies through this check. It does
> not replace [CSRF protection](../browser-security/csrf-protection.md) for routes
> that browsers call with cookies; use both.

## Choosing the rejection response

If clients need a different explanation, replace the rejection problem with
`Builder.ErrorResponse`. To keep the default 415 status and problem identity,
derive from `contenttype.ErrUnsupportedMediaType`. Import
`arkoren.dev/foundation/problem` for the derivation callback. In place of the
earlier middleware registration, before adding routes:

```go
failure := contenttype.ErrUnsupportedMediaType.Derive(func(details *problem.Builder) {
	details.Detail("Send JSON for this endpoint.")
})
r.Use(contenttype.Middleware(func(content *contenttype.Builder) {
	content.ErrorResponse(failure)
}))
```

Rejected requests now carry that explanation. Only a complete zero
`problem.Details{}` selects the middleware's default 415 problem. A nonzero
custom problem decoded without a status keeps its supplied members; the problem
renderer completes the absent status as 500 instead of replacing it with 415.
Choose an explicit status when you intend a different HTTP outcome. See
[error responses](error-handling.md) for problem rendering and customization.

## Observing rejections

To log or count rejected requests, pass the event bus from your existing
`*application.Application` named `app`. Use a fresh router, or configure the
earlier middleware call this way before registering routes:

```go
r.Use(contenttype.Middleware(func(content *contenttype.Builder) {
	content.Events(app.Events())
}))
```

If you already selected a custom rejection problem, add `Events` to that same
construction callback rather than placing another content-type middleware around it.

Each content-type rejection then publishes `http.contenttype.rejected`, regardless
of the status selected by `Builder.ErrorResponse`. Its payload contains the method,
path, and lower-case base media type, without parameters. The media type is empty
when the header is missing or its base type cannot be parsed. Malformed parameters
can still leave a parsed base type in the event even though the request is rejected.
The client chooses that value, so treat it as untrusted text. Every rejected
request publishes, so keep the listener cheap and put the
[rate limiting middleware](../abuse-prevention/rate-limiting.md) in front.
[HTTP Events](../events/http-events.md#request-rejections) describes the payload.

For every option, see the [`contenttype` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/contenttype).
