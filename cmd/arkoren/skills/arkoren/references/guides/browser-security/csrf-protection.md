---
title: CSRF Protection
description: Reject forged cookie-backed browser actions with request-header checks and narrowly scoped cross-origin exceptions.
order: 40
summary:
  - Protect cookie-backed actions with browser-header checks and understand which requests still pass.
  - Trust exact browser origins or isolate independently verified callbacks without granting resource access.
  - Combine request-origin checks with media-type checks and keep token-only boundaries distinct.
  - Observe blocked requests safely and test that rejection stops application work.
---

# CSRF Protection

A hostile page can submit a request to your application without knowing the
visitor's session cookie. If cookie scope and `SameSite` rules allow it, the
browser attaches that cookie automatically. Accepting the request solely because
it has a valid session can let the hostile page change the visitor's settings.
This is **cross-site request forgery** (CSRF).

Arkoren's `csrf` middleware uses Go's `http.CrossOriginProtection` to check
browser-supplied headers before application work. It needs no form tokens and
stores nothing in the session. It does not replace authentication or the
resource authorization inside your handlers.

## Protect Cookie Actions

Put cookie-authorized actions behind the middleware, before authentication and
handlers. With a fresh router `routes`, import `arkoren.dev/core/http/router`
and `arkoren.dev/core/http/middleware/csrf`. Here `requireAccount` is your
authentication middleware and `savePreferences` is your existing authorized
handler:

```go
routes.Group("/account", func(account *router.Router) {
	account.Use(csrf.Middleware(), requireAccount)
	account.Patch("/preferences", savePreferences)
})
```

Later group policy examples replace this `Use` call; configure the chosen
policy before registering the group's routes.

The policy rejects unsafe cross-origin browser requests before either inner
layer runs. Protect login and other cookie-backed actions too, not only routes
that already require a signed-in user. If authentication uses sessions, install
[session middleware](../http-capabilities/sessions.md#enable-sessions) before
it; the CSRF checker itself does not need a session. Session changes still follow
the normal [save behavior](../http-capabilities/sessions.md#save-changes).

## Understand Checks

`GET`, `HEAD`, and `OPTIONS` always pass. Never use these methods for logout,
deletion, or other state changes: those actions would bypass this protection.
For other methods, the checker uses this order:

1. If `Sec-Fetch-Site` is nonempty, `same-origin` and `none` pass. Other values,
   including `same-site`, fail unless an explicit exception applies. Sharing a
   parent domain is not enough to trust another origin.
2. Otherwise, a nonempty `Origin` passes if its parsed host, including any port,
   equals the request's `Host`. This fallback does not compare schemes; HTTPS and
   a deliberate [HSTS policy](secure-headers.md#enable-hsts) help address the
   older-browser boundary.
3. If both headers are missing or empty, the request passes as a possible
   same-origin or non-browser request.

Browser pages cannot freely set these headers, but server clients can supply or
omit them. Passing the checker is never proof of client identity. Modern
browsers provide the fetch-context header; do not assume old clients implement
it or enforce the session cookie's default `SameSite=Lax` behavior.

The Host fallback uses the request as it reaches the application, not
`X-Forwarded-Host`. Configure reverse proxies to preserve the intended public
host and browser security headers rather than replacing them with internal or
client-selected values.

A blocked request returns `csrf.ErrBlocked` without calling the next layer.
With the default router renderer and an acceptable JSON representation, the
client receives a `403` problem with this identity:

```json
{
  "type": "tag:arkoren.dev,2026-09:core:http/middleware/csrf:blocked",
  "title": "Cross-Origin Request Blocked",
  "status": 403,
  "detail": "The request was rejected because its origin or fetch context did not meet security requirements."
}
```

The router's [error negotiation](../http-fundamentals/error-handling.md#negotiate-error-output)
still applies, including `406` if the client excludes every supported format.

## Trust Browser Origins

If your portal intentionally sends account changes to a separate API origin,
trust its exact browser origin rather than every subdomain. Replace the default
CSRF registration in the account group with:

```go
account.Use(csrf.Middleware(func(protection *csrf.Builder) {
	protection.TrustedOrigins("https://portal.example.com")
}), requireAccount)
```

An exact matching `Origin` passes the CSRF check even for cross-site fetch
context. Use `scheme://host[:port]` without a path; malformed origins panic
during middleware construction. This is permission to pass this checker, not
permission to change an account. The authenticated visitor still needs resource
authorization, and the portal needs a separate [CORS policy](cors.md#send-credentials)
to read credentialed API responses. Cookies must also permit the request.

## Isolate Exceptions

Some routes intentionally accept cross-site posts. Keep a signature-verifying
webhook receiver outside the protected browser group rather than weakening the
group. On a fresh router without global CSRF middleware, `receivePayment` is
your configured, signature-verifying handler:

```go
routes.Post("/callbacks/payment", receivePayment)
routes.Group("/account", func(account *router.Router) {
	account.Use(csrf.Middleware(), requireAccount)
	account.Patch("/preferences", savePreferences)
})
```

The callback must perform its own [webhook verification](../webhooks/receiving-webhooks.md).
A browser login callback accepting cross-site `POST` likewise needs its login
flow's callback checks. Being outside the group provides no verification.

If scoping cannot isolate a verified callback, supply a native checker with one
narrow bypass. Import `net/http` and use this alternative global policy:

```go
engine := http.NewCrossOriginProtection()
engine.AddInsecureBypassPattern("POST /callbacks/payment")

routes.Use(csrf.Middleware(func(protection *csrf.Builder) {
	protection.Protection(engine)
}))
```

The bypass disables CSRF checks for matching requests; it does not verify them.
Patterns follow `http.ServeMux` syntax and precedence, and malformed or
conflicting patterns panic. Broad path patterns can silently expose unrelated
actions, so keep the method and callback path explicit.

The provided checker remains shared. Configuring `TrustedOrigins` adds origins
to that same instance; later native changes affect future requests. Do not
treat it as an isolated copy or modify it casually after publication.

## Check JSON Inputs

JSON syntax alone is not a browser boundary: a hostile page can send JSON-looking
bytes labeled `text/plain` without a CORS preflight. If your account group accepts
only JSON, add `arkoren.dev/core/http/middleware/contenttype` and use this policy
before body decoding:

```go
account.Use(csrf.Middleware(), contenttype.Middleware(), requireAccount)
```

The default media-type policy accepts `application/json` and `+json` types,
rejecting disallowed form and plain-text declarations. Missing headers on empty
requests can pass; see [empty-request checks](../http-fundamentals/content-type.md#check-empty-requests).
The media-type check neither validates the JSON contents nor replaces CSRF or
resource authorization. Keep CSRF protection when allowing browser form formats.

An API authenticated exclusively by an explicit bearer token in `Authorization`
does not have the automatic-cookie credential risk: browsers do not attach that
header on their own. You can omit CSRF middleware for that token-only boundary.
If the same API also accepts session cookies, retain it. Non-browser token clients
without browser headers pass; browser token clients are still subject to the
header checks and any configured trusted origins.

## Observe Rejections

To receive rejection notifications, pass your existing `*event.Bus` named `bus`
in a construction callback, keeping authentication after the checker:

```go
account.Use(csrf.Middleware(func(protection *csrf.Builder) {
	protection.Events(bus)
}), requireAccount)
```

Each blocked request emits `csrf.Blocked` with its method, path, `Origin`, and
native error. With no bus, nothing is published. Listeners run synchronously and
cannot change the rejection. Paths can contain personal identifiers, and origin
and error values are diagnostic, potentially untrusted data; review them before
logging or exporting them. A rejection can indicate either an attack or a
misconfigured browser policy, not proof of either.

If clients need different failure wording, `Builder.ErrorResponse` accepts a
`problem.Details` value. Derive from `csrf.ErrBlocked` to preserve the `403` status
and stable problem identity, and keep internal causes out of client-facing text.

## Test Rejection

An ordinary `httptest.NewRequest` lacks browser headers and passes the checker.
Test a cross-site write explicitly and make any downstream execution fail the
test. With `net/http`, `net/http/httptest`, and `t *testing.T`:

```go
handler := csrf.Middleware()(func(http.ResponseWriter, *http.Request) error {
	t.Fatal("rejected request reached application work")

	return nil
})

incoming := httptest.NewRequest(http.MethodPatch, "/account/preferences", nil)
incoming.Header.Set("Sec-Fetch-Site", "cross-site")
incoming.Header.Set("Origin", "https://untrusted.example")
response := handler.Record(incoming)
```

Register cleanup for the recorded response body and assert the rejection:

```go
t.Cleanup(func() {
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
})
if response.StatusCode != http.StatusForbidden {
	t.Fatalf("status = %d, want 403", response.StatusCode)
}
```

For your configured routes, also test that blocked requests leave stored data
unchanged, trusted origins can reach the intended policy layers, and authentication
and authorization still reject visitors without permission.

For configuration and events, see the [`csrf` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/csrf).
