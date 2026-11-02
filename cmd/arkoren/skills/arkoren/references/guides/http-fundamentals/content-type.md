---
title: Content Types
description: Reject request media types an endpoint does not accept before decoding its body.
order: 70
summary:
  - Require JSON media types and understand checks for empty and unknown-length requests.
  - Scope form and multipart allowances without weakening unrelated JSON endpoints.
  - Customize rejection problems and observe events without trusting client metadata.
---

# Content Types

A JSON decoder accepts JSON-looking bytes without checking the request's
`Content-Type`. That allows a mislabeled `text/plain` request to reach the same
decoder. A hostile page can send some form or plain-text browser requests
without a CORS preflight, sometimes with your cookies depending on their scope
and `SameSite` policy. Reject formats your endpoint does not accept before
reading the body.

The `contenttype` middleware checks the declared media type, not the contents
or the caller's identity. It complements decoding, [body limits](body-limit.md),
and [CSRF protection](../browser-security/csrf-protection.md); it replaces none
of them.

## Require JSON

On a fresh [router](routing.md) named `routes`, import
`arkoren.dev/core/http/middleware/contenttype`:

```go
routes.Use(contenttype.Middleware())
```

Register JSON routes after the policy. By default it allows `application/json`
and any media type ending in `+json`, such as `application/merge-patch+json`.
Missing, malformed, or disallowed headers on requests carrying data return
`contenttype.ErrUnsupportedMediaType` (`415`) without calling the next layer.
The router renders that problem if output has not started.

Matching ignores case and valid media parameters such as `charset=utf-8`.
Malformed parameters are rejected. An allowed header does not prove valid JSON;
the [request decoder](requests.md#decode-json) still checks bytes and structure.

## Check Empty Requests

The decision uses `ContentLength`, not a speculative read from the body:

- A nonzero length, including unknown length `-1`, requires an allowed header
  regardless of method.
- At length zero, POST, PUT, PATCH, DELETE, and QUERY reject a supplied header
  when it is malformed or disallowed. A missing header passes.
- Other methods at length zero pass without this media-type check.

A bodyless GET therefore does not require `Content-Type`, while an empty POST
declaring `text/plain` is rejected. This prevents a disallowed form format from
bypassing the policy merely because its body is empty. Do not use method or
media type as a substitute for authorization.

## Allow Other Formats

If one endpoint receives multipart uploads, give it a separate policy rather
than adding multipart to every JSON route. On a router without the global JSON
policy above, with an existing `uploadAttachment` handler:

```go
routes.With(contenttype.Middleware(func(content *contenttype.Builder) {
	content.Allowed("multipart/form-data")
})).Post("/attachments", uploadAttachment)
```

`Allowed` replaces the default list. Use `application/x-www-form-urlencoded`
for a form endpoint, or list both it and `application/json` when both formats
are intentional. An entry starting with `+`, such as `+xml`, matches every
media type with that suffix. An empty allowed list restores the JSON defaults;
it does not create a deny-all policy. Narrow defaults to only `application/json`
when the endpoint should not accept other JSON-based media types.

Allowing form media types allows cross-site form requests through this check.
Keep CSRF protection on cookie-authorized browser routes. The multipart parser
still validates boundaries and input separately; see
[Parse Uploads](requests.md#parse-uploads).

## Customize Failures

For a more useful client explanation without changing status or identity,
derive the default problem. Import `arkoren.dev/foundation/problem`:

```go
failure := contenttype.ErrUnsupportedMediaType.Derive(func(details *problem.Builder) {
	details.Detail("Send application/json for invoice drafts.")
})
```

Pass it in place of the earlier middleware registration, before routes:

```go
routes.Use(contenttype.Middleware(func(content *contenttype.Builder) {
	content.ErrorResponse(failure)
}))
```

Only the complete zero `problem.Details{}` selects the default problem. A
nonzero custom problem with an absent status retains its members and receives
the renderer's default `500`, not `415`. Choose an explicit status if changing
the HTTP outcome. [Error Handling](error-handling.md#negotiate-error-output)
owns negotiation and rendering defaults.

## Observe Rejections

With an existing application `app`, add its bus to the same policy construction
callback. Do not stack another policy merely to enable events:

```go
routes.Use(contenttype.Middleware(func(content *contenttype.Builder) {
	content.Events(app.Events())
}))
```

The default nil bus publishes nothing. When configured, every rejection emits
`contenttype.Rejected` (`http.contenttype.rejected`), regardless of a custom
problem's status. Its payload contains method, path, and the lower-case parsed
base media type without parameters. Missing or unparseable base types produce
an empty value; malformed parameters can still leave a parsed base type.

The client controls this metadata, and paths can contain personal identifiers.
Review it before forwarding to external systems. Treat it as untrusted text,
keep listeners cheap, and put [rate limiting](../abuse-prevention/rate-limiting.md) before this
policy when rejection traffic needs a bound. For event payloads, see
[HTTP Events](../events/http-events.md#request-rejections).

For all policy options, see the
[`contenttype` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/contenttype).
