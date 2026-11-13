---
title: Authentication Overview
nav: Overview
description: Resolve credentials into an identity and choose which routes require it.
order: 10
summary:
  - Return an auth.Identity from every authenticator and reject invalid accounts before publishing it.
  - Combine authenticators deliberately and distinguish optional identity from required access.
  - Read the request identity and customize anonymous rejections without exposing sensitive diagnostics.
---

# Authentication Overview

Authentication establishes who sent a request. Arkoren resolves a session or
bearer credential into an `auth.Identity`: the user's subject, how they were
authenticated, their scopes, and optionally your own user value. It supplies no
user table, registration flow, or login screen. [Authorization](../authorization/overview.md)
then decides what that identity may do.

Use [Session Login](session-login.md) for passwords and browser sessions,
[API Tokens](api-tokens.md) for application-issued keys, [OAuth Login](oauth.md)
for external browser sign-in, or [OIDC Tokens](oidc-bearer.md) for provider-issued
JWTs. All four use the same request identity model.

Focused snippets keep error results visible but omit routine propagation.
Checks remain where failure changes authentication or access. Handle all errors
in your application.

## Define Your User

An `auth.Identity` is an immutable value that carries no application type, so
packages that do not know your user (jobs, MCP tools, rate limits) can still read
the subject, scopes, and authentication level. Handlers and policies usually also
want your own account value. An invoice application might use:

```go
type User struct {
	ID       int
	Name     string
	Disabled bool
}
```

No field or interface is required by Arkoren. An authenticator attaches the user
to the identity with `auth.NewIdentity(subject, func(principal *auth.IdentityBuilder) { principal.Record(auth.UserRecord{Subject: subject, Value: user}) })`.
The session authenticator below does that for you. Your verifier or loader must
reject unknown and disabled accounts before returning success. Returning an
identity still authenticates a request; `auth.Require` checks presence, not
whether the subject or user fields are meaningful.

User lookup uses the exact Go type. `User`, `*User`, and a separately defined
type with identical fields are different types; Arkoren does not convert between
them, and a mismatch reads as no user.

## Resolve Credentials

An **authenticator** is an `auth.Authenticator`, a `func(*http.Request) (auth.Identity, error)`
that verifies credentials and returns the identity. Import `auth` from
`arkoren.dev/core/auth`. Its result controls whether another authenticator can try:

| Result | Middleware behavior |
| --- | --- |
| Identity and nil error | Store that identity and stop trying authenticators. |
| `auth.ErrNoCredentials`, including wrapped errors | Try the next authenticator. |
| Any other error | Stop the request without calling its handler. |

Return `ErrNoCredentials` only when the request has no credentials that this
authenticator handles. A presented invalid bearer token must return a different
error. Do not trust a client-supplied user ID or header as proof of identity.

For routes accepting both browser sessions and API keys, use your existing
`guard` (an `*auth.Guard`, see [Enable Login](session-login.md#enable-login)) and
`verifyToken` (`func(context.Context, string) (auth.Identity, error)`):

```go
r.Use(auth.Middleware(
	guard.Authenticator(),
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
response. To require an identity for private routes, add `auth.Require` after the
authentication middleware:

```go
private := r.With(auth.Require())
private.Get("/invoices", listInvoices)
```

`listInvoices` is your existing handler. An anonymous request receives a `401`
Problem Details response with `WWW-Authenticate: Bearer`; an authenticated
request reaches the handler. This proves identity presence, not permission to
read every invoice. Apply your [authorization rules](../authorization/overview.md)
separately.

For browser pages, a `401` does not display a login form. If you want a login
redirect, write middleware that checks `auth.FromContext` and redirects to
a fixed local login path. Validate any return target rather than redirecting to
an arbitrary request parameter. See [middleware scope](../http-fundamentals/middleware.md)
for applying different behavior to different route groups.

## Read Identity

Handlers read the identity with `request.Identity(r)`, or the user your
authenticator attached with `request.User[User](r)`. With `request` and
`response` from their `arkoren.dev/core/http/` packages and standard-library
`net/http`, read the local user before returning a profile:

```go
user, err := request.User[User](r)
if err != nil {
	return err
}

return response.JSON(w, http.StatusOK, user)
```

`w` and `r` are the handler's response writer and request. A missing identity,
or one whose user is absent or of a different type, returns
`auth.ErrUnauthenticated`. In context-only code, `auth.FromContext(ctx)` returns
the identity and `auth.UserFromContext[User](ctx)` returns your user, each with
a presence boolean instead.

Tests can use `auth.NewContext(ctx, identity)` without performing a login.
That helper trusts the supplied value; keep it in trusted code, not in a path
that copies unverified request input. [Auth Tests](../testing/auth-tests.md)
also covers testing the actual authenticators.

## Handle Rejections

To change the anonymous response, configure `auth.MiddlewareBuilder.ErrorResponse` in
`auth.Require(...)`. It replaces the whole problem and its headers, so
include the appropriate authentication challenge if you keep status `401`.
A zero response restores the default. This setting does not change the error
returned by `request.Identity`, `request.User` or failures returned by an authenticator.

If you need anonymous-rejection events, supply your existing event bus:

```go
private := r.With(auth.Require(func(required *auth.MiddlewareBuilder) {
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
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/auth).
