---
title: Authentication Overview
nav: Overview
description: Resolve credentials into your application's identity and choose which routes require it.
order: 10
summary:
  - Use one application-owned identity type and reject invalid accounts before publishing it.
  - Combine authenticators deliberately and distinguish optional identity from required access.
  - Read the request identity and customize anonymous rejections without exposing sensitive diagnostics.
---

# Authentication Overview

Authentication establishes who sent a request. Arkoren resolves a session or
bearer credential into a Go value that your application owns; it supplies no
user table, registration flow, or login screen. [Authorization](../authorization/overview.md)
then decides what that identity may do.

Use [Session Login](session-login.md) for passwords and browser sessions,
[API Tokens](api-tokens.md) for application-issued keys, [OAuth Login](oauth.md)
for external browser sign-in, or [OIDC Tokens](oidc-bearer.md) for provider-issued
JWTs. All four use the same request identity model.

Focused snippets keep error results visible but omit routine propagation.
Checks remain where failure changes authentication or access. Handle all errors
in your application.

## Define Identity

Choose one identity type for handlers and policies. An invoice application might
use this local account value:

```go
type User struct {
	ID       int
	Name     string
	Disabled bool
}
```

No field or interface is required by Arkoren. Your verifier or loader must
reject unknown and disabled accounts before returning success. Returning
`User{}, nil` still authenticates a request; `auth.Require` checks presence,
not whether fields are nonzero. The same applies to a typed nil pointer returned
without an error.

Identity lookup uses the exact Go type. `User`, `*User`, and a separately defined
type with identical fields are different identities; Arkoren does not convert
between them.

## Resolve Credentials

An **authenticator** is a `func(*http.Request) (User, error)` that verifies
credentials and returns your identity. Import `auth` from
`arkoren.dev/core/http/auth`. Its result controls whether another authenticator
can try:

| Result | Middleware behavior |
| --- | --- |
| User and nil error | Store that user and stop trying authenticators. |
| `auth.ErrNoCredentials`, including wrapped errors | Try the next authenticator. |
| Any other error | Stop the request without calling its handler. |

Return `ErrNoCredentials` only when the request has no credentials that this
authenticator handles. A presented invalid bearer token must return a different
error. Do not trust a client-supplied user ID or header as proof of identity.

For routes accepting both browser sessions and API keys, use your existing
`loadUser` (`func(context.Context, int) (User, error)`) and `verifyToken`
(`func(context.Context, string) (User, error)`):

```go
identity := auth.NewSessionIdentity(loadUser)
r.Use(auth.Middleware(
	identity.Authenticator(),
	auth.Bearer(verifyToken),
))
```

Here `r` is your existing router. This browser group already needs
[session and CSRF middleware](session-login.md#enable-login) before authentication.
The first success wins: a valid session in this order prevents inspection of a
simultaneously supplied bearer header. An error stops the chain; it does not
trigger fallback. Prefer separate browser and API groups when they need different
credential policies.

## Choose Route Access

Authentication middleware permits anonymous requests when every authenticator
returns `ErrNoCredentials`. This lets public pages optionally personalize their
response. To require a user for private routes, add `auth.Require` after the
authentication middleware:

```go
private := r.With(auth.Require[User]())
private.Get("/invoices", listInvoices)
```

`listInvoices` is your existing handler. An anonymous request receives a `401`
Problem Details response with `WWW-Authenticate: Bearer`; an authenticated
request reaches the handler. This proves identity presence, not permission to
read every invoice. Apply your [authorization rules](../authorization/overview.md)
separately.

For browser pages, a `401` does not display a login form. If you want a login
redirect, write middleware that checks `auth.FromContext[User]` and redirects to
a fixed local login path. Validate any return target rather than redirecting to
an arbitrary request parameter. See [middleware scope](../http-fundamentals/middleware.md)
for applying different behavior to different route groups.

## Read Identity

Handlers retrieve the exact type stored by authentication. With `request` and
`response` from their `arkoren.dev/core/http/` packages and standard-library
`net/http`, read the local user before returning a profile:

```go
user, err := request.User[User](r)
if err != nil {
	return err
}

return response.JSON(w, http.StatusOK, user)
```

`w` and `r` are the handler's response writer and request. Missing identities
return `auth.ErrUnauthenticated`, including when a different identity type is
present. In context-only code, `auth.FromContext[User](ctx)` returns the value
and a presence boolean instead.

Tests can use `auth.NewContext(ctx, User{ID: 42})` without performing a login.
That helper trusts the supplied value; keep it in trusted code, not in a path
that copies unverified request input. [Auth Tests](../testing/auth-tests.md)
also covers testing the actual authenticators.

## Handle Rejections

To change the anonymous response, configure `auth.Builder.ErrorResponse` in
`auth.Require[User](...)`. It replaces the whole problem and its headers, so
include the appropriate authentication challenge if you keep status `401`.
A zero response restores the default. This setting does not change the error
returned by `request.User` or failures returned by an authenticator.

If you need anonymous-rejection events, supply your existing event bus:

```go
private := r.With(auth.Require[User](func(required *auth.Builder) {
	required.Events(app.Events())
}))
```

Every rejection publishes `http.auth.rejected`, even with a custom status.
Its payload contains `Method`, not a request path, identity, query, or cause.
HTTP methods and event context values can still be untrusted or sensitive;
normal Problem Details rendering can include the request path as `instance`.
Keep listeners cheap and apply [rate limiting](../abuse-prevention/rate-limiting.md)
before the required-user middleware when needed. No event bus means no event.

For all functions and options, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth).
