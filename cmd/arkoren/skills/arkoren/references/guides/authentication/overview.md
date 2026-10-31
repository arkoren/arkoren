---
title: Authentication Overview
nav: Overview
description: "How Arkoren finds out who sent a request: your own user type, authenticators tried in order, the auth middleware, and required-user routes."
order: 10
---

# Authentication Overview

## Introduction

Most services need to know who sent a request before they decide what to do
with it. A browser carries a session cookie, a script sends an API key, and a
mobile app presents a token issued by an identity provider. **Authentication**
is the step that turns any of these into a user your code can work with.

Arkoren keeps this step small. It has no user model, no user table, and no
built-in login screens. You define the user type, and you write the one
function that looks a user up or verifies a token. Authentication has two
separate steps: `auth.Middleware` tries your authenticators in the order you
choose and stores the first resolved user on the request; `auth.Require`
rejects anonymous requests on protected routes with `401` by default. Middleware
alone leaves public routes open to anonymous requests.

Deciding what a signed-in user may do is a separate step,
[authorization](../authorization/overview.md). This page covers the shared model. The
other pages of this topic cover each way of signing in:
[session login](session-login.md) for browsers, [API tokens](api-tokens.md) for
programs, [OAuth browser login](oauth.md) for external sign-in, and
[OIDC bearer verification](oidc-bearer.md) for provider-issued API access tokens.
For an MCP endpoint, reuse these mechanisms and follow
[Securing MCP](../mcp/securing-mcp.md) for protocol setup and tool availability.

The focused snippets below assume the ordinary success path and leave error
results visible. Handle those errors in your application; checks remain in
examples where they determine whether access is granted.

Examples import `auth` from `arkoren.dev/core/http/auth`, `request`, `response`,
`router`, and `session` from the corresponding `arkoren.dev/core/http/` packages,
and `csrf` and `ratelimit` from `arkoren.dev/core/http/middleware/`. `context`
and `net/http` are standard-library imports. The router and session store are
your existing [HTTP setup](../http-capabilities/sessions.md).

## Your user type

The user is a type from your application, usually a small struct with the
fields that handlers and authorization rules need:

```go
type User struct {
	ID   int
	Name string
	Role string
}
```

Nothing in Arkoren requires a particular field or interface. The `auth`
package is generic over the user type. Use that exact type in authenticators,
handlers, and rules: context lookup does not convert between types, and a
mismatch is treated as a missing user.

## Authenticators

An **authenticator** is a function that receives a request and returns its
user. It has three possible outcomes, and the difference between them is what
lets several kinds of credential live side by side:

- It finds credentials it understands and they are valid: it returns the user.
- It finds no credentials it understands: it returns `auth.ErrNoCredentials`,
  which means "not mine, ask the next one".
- It finds credentials but they are wrong, expired, or malformed: it returns
  an error, and the request stops there.

Arkoren ships two authenticators. A shared `auth.NewSessionIdentity(loadUser)`
definition supplies an `Authenticator()` that reads the identifier stored in the
session by a [login](session-login.md), and `auth.Bearer` reads an
`Authorization: Bearer` header for [API tokens](api-tokens.md). Both take a
function you write that turns the stored identifier or the token into your user.
When neither fits, an authenticator is just a function, so you can write your
own. In this example, `users.ByCertificate` is your application's lookup of a
certificate identity, not an Arkoren helper. Configure the TLS server to verify
client certificates before trusting that identity:

```go
func byClientCert(r *http.Request) (User, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return User{}, auth.ErrNoCredentials
	}

	return users.ByCertificate(r.Context(), r.TLS.VerifiedChains[0][0])
}
```

## The auth middleware

To accept more than one kind of credential on the same routes, give
`auth.Middleware` the authenticators in priority order. Here `loadUser` is your
[session lookup](session-login.md#loading-the-user), and `verifyToken` is your
[token verifier](api-tokens.md#accepting-bearer-tokens). The first one
that returns a user wins, and the user is stored in the request's context:

```go
identity := auth.NewSessionIdentity(loadUser)
r.Use(auth.Middleware(
	identity.Authenticator(),
	auth.Bearer(verifyToken),
))
```

When every authenticator returns `auth.ErrNoCredentials`, none has resolved a
user. The request is not rejected: it continues as an **anonymous**
request, so public pages keep working. When an authenticator returns any other
error, the middleware returns that error and the handler never runs. If the
bearer authenticator is reached, a bad token produces `401` rather than falling
through, provided your verifier does not return `auth.ErrNoCredentials` for it.

The order of the authenticators matters only when a request carries several
kinds of credential. A browser that also sends a bearer header is
authenticated by whichever authenticator comes first.

### Middleware order

Authentication reads the session, and much of what follows reads the user, so
the middleware is registered in this order. Here `byUser` is your
`func(*http.Request) string` rate-limit key function; it reads the authenticated
user and uses a separate anonymous key when none is present:

```go
identity := auth.NewSessionIdentity(loadUser)
r.Use(session.Middleware(store))
r.Use(csrf.Middleware())
r.Use(auth.Middleware(identity.Authenticator()))
r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.KeyFunc(byUser)
}))
```

The session comes first because `identity.Authenticator()` reads from it. CSRF protection
comes before authentication, because a cookie that browsers send on their own
must never authenticate a forged cross-site request, as explained in
[CSRF protection](../browser-security/csrf-protection.md). Rate limiting by user comes after,
because it needs the user. Routes come last.

## Requiring a signed-in user

`auth.Require` rejects anonymous requests. Apply it to the routes, or the
group of routes, that need a user:

```go
private := r.With(auth.Require[User]())
private.Get("/me", showProfile)
private.Put("/me", updateProfile)
```

By default, an anonymous request receives a `401 Unauthorized` Problem Details
response with these shared members:

```json
{
  "type": "tag:arkoren.dev,2026-09:core:http/auth:unauthenticated",
  "title": "Unauthenticated",
  "status": 401,
  "detail": "Authentication is required to access this resource."
}
```

HTTP requires a `401` to say how to authenticate, in a `WWW-Authenticate`
header. The default is `Bearer`, which suits APIs. If your service uses
another scheme, pass your own response with `auth.Builder.ErrorResponse` and
`problem.Unauthorized`, which sets the header for you. `ErrorResponse` replaces
the problem and its headers, so it can also select a different status; its zero
value restores the default. HTTP rendering can add `instance` from the request
path. That member is not a sanitized diagnostic identifier.

To count or log anonymous requests that reach protected routes, pass the event
bus with `auth.Builder.Events`:

```go
private := r.With(auth.Require[User](func(authenticator *auth.Builder) {
	authenticator.Events(app.Events())
}))
```

Each anonymous rejection then publishes `http.auth.rejected` with `Method`,
including when `ErrorResponse` selects a status other than `401`.
`auth.RejectedEvent.Path` has been removed because paths can contain identifiers
or credentials. Remove `Path` reads from existing listeners; the payload carries
no user, query, or raw cause, and no replacement request path is populated.

The event context still retains request context values. Custom HTTP methods are
client-controlled too; classify or filter them before logging when needed. Do
not treat context values or HTTP problem instances as generally sanitized just
because the event payload omits paths. There is no deduplication, so a client
decides how many events you receive: keep the listener cheap and
put the [rate limiting middleware](../abuse-prevention/rate-limiting.md) in front of
`auth.Require`. [Built-in Events](../events/built-in-events.md#authentication-and-authorization)
describes the payload.

> [!NOTE]
> A browser receiving a `401` shows the JSON body, not a login form. For pages
> that people open directly, redirect anonymous visitors to your login page in
> a small middleware of your own instead of `auth.Require`.

## Reading the user

In a handler, `request.User` returns the user of the request:

```go
func showProfile(w http.ResponseWriter, r *http.Request) error {
	user, err := request.User[User](r)
	if err != nil {
		return err
	}

	return response.JSON(w, http.StatusOK, user)
}
```

On an anonymous request, `request.User` returns `auth.ErrUnauthenticated` (`401`),
the problem behind `auth.Require`'s default response. A custom `ErrorResponse`
does not change this lookup error. On a route behind `auth.Require` using the
same exact user type, the user is always there.

Outside a handler, in code that only receives a context,
`auth.FromContext` returns the user and whether one was found.

### Use one user type everywhere

The user is stored under its exact type. If the middleware stores a `User` and
a handler asks for a `*User`, the lookup finds nothing, and the request is
treated as anonymous. This fails closed rather than granting access to the
wrong person, but it is confusing to debug. Define one helper in your
application and call it everywhere:

```go
func CurrentUser(r *http.Request) (User, error) {
	return request.User[User](r)
}
```

## Choosing what to store

Store an identifier in the session, rather than a snapshot of the user's roles
or permissions. When your session loader or token verifier looks up the current
user on each request, a new role or a deactivated account takes effect on the
next request, at the cost of one lookup per request. If that lookup becomes
expensive, cache it, and delete the cached entry when the user changes.

[API tokens](api-tokens.md#self-contained-tokens) describe the other choice,
tokens that carry the user's details themselves, and the trade-off it makes.

## Testing

Tests do not need to log in. Put a user directly in the request's context with
`auth.NewContext`, or build the router with an authenticator that accepts a
fixed test token. [Auth tests](../testing/auth-tests.md) shows both, and how
to check your own authenticators with the shared conformance suite.

For every function and option, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth).
