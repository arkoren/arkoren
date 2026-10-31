---
title: Testing Authentication
nav: Testing Auth
description: Test routes that need a signed-in user with the request context or a test authenticator, and check your own authenticators.
order: 30
---

# Testing Authentication

## Introduction

Routes behind authentication need a user, and creating a real one in every test
would be slow and noisy: register, log in, carry a cookie. Arkoren's
[authentication](../authentication/overview.md) keeps the user in the
request context, which gives tests two shortcuts. A handler test can put a user
there directly. A router test can swap the production authenticator for one
that trusts a test token. For your own authenticators, the `authtest` package
checks that they follow the rules every authenticator shares.

The snippets run in your application's tests. `User` is your own user type with
`ID` and `Name` fields, and `me` is your authenticated HTTP handler. Import
`context`, `net/http`, `net/http/httptest`, `arkoren.dev/core/http/auth`,
`arkoren.dev/core/http/request`, and `arkoren.dev/core/http/router` as needed.
Never enable the fixed-token authenticator below outside tests.

## Putting a user in the context

When you test a handler that reads the current user, add the user to the
request's context with `auth.NewContext`:

```go
req := httptest.NewRequest(http.MethodGet, "/me", nil)
req = req.WithContext(auth.NewContext(req.Context(), User{ID: 1, Name: "Ada"}))

res := router.Handler(me).Record(req)
if res.StatusCode != http.StatusOK {
	t.Fatalf("status = %d, want 200", res.StatusCode)
}
```

The handler reads the user with `request.User[User](r)` exactly as in
production. This is the fastest option, and the right one when the test is
about what the handler does with the user, not about how the user signed in.

## Using a test authenticator

To test the whole router, including `auth.Require` and authorization, build it
with an authenticator that accepts a fixed token. `auth.Bearer` turns a
verification function into an authenticator:

```go
authenticator := auth.Bearer(func(ctx context.Context, token string) (User, error) {
	if token != "test-token" {
		return User{}, auth.ErrUnauthenticated
	}
	return User{ID: 1, Name: "Ada"}, nil
})

r := router.New()
r.Use(auth.Middleware(authenticator))
r.With(auth.Require[User]()).Get("/me", me)
```

Requests with `Authorization: Bearer test-token` are now signed in as Ada,
and requests without the header are anonymous, so `auth.Require` rejects them:

```go
req := httptest.NewRequest(http.MethodGet, "/me", nil)
req.Header.Set("Authorization", "Bearer test-token")

res := r.Record(req)
if res.StatusCode != http.StatusOK {
	t.Fatalf("status = %d, want 200", res.StatusCode)
}
anonymous := r.Record(httptest.NewRequest(http.MethodGet, "/me", nil))
if anonymous.StatusCode != http.StatusUnauthorized {
	t.Fatalf("anonymous status = %d, want 401", anonymous.StatusCode)
}
```

To make this easy, let the function that builds your router take the
authenticators as an argument. Production passes the session and API key
authenticators, and tests pass the test one.

## Testing the real login flow

Keep at least one test that goes through your actual login route with a
session, to check that logging in, staying logged in, and logging out work
together. It needs a test server and a cookie jar, as shown in
[HTTP tests](http-tests.md#requests-across-several-calls). Other tests can use
the shortcuts above.

For a focused session-state test, import `arkoren.dev/core/http/session`.
`session.New(expiresAt, map[string]any)` is
the low-level initializer; it does not replace middleware in a cookie-flow
test. Use the same typed [session fields](../http-capabilities/sessions.md#reading-and-writing-values)
as production writers and readers. Initial in-memory values must already have
those Go types: `Get` does not convert a test map through JSON to fit a field.
Share the production `auth.NewSessionIdentity(load)` definition across its
authenticator and login/logout calls. Check errors from `Put`, `Delete`,
`identity.Login`, and `identity.Logout` before asserting the resulting state.
For a custom context slot, allocate one `session.NewHandle()` and attach the
test session with `session.NewContextFor(ctx, handle, current)`; configure that
same handle on the identity and request lookup. Zero handles select the default
slot. A nil session pointer is absent, not a usable test session.

## Checking an authenticator

An **authenticator** turns a request into a user. If you write your own, for
example for a custom API key scheme, `authtest.Run` checks that it behaves like
every other authenticator: a request without credentials returns
`auth.ErrNoCredentials` and passes the middleware anonymously, a request with
valid credentials stores a user, and a request with rejected credentials fails
with `auth.ErrUnauthenticated`. You provide three functions that build each
kind of request. Here `apiKeyAuthenticator` is your implementation; the
application test helpers `anonymousRequest`, `validRequest`, and `forgedRequest`
each take `*testing.T` and return a fresh `*http.Request` for that case:

Import `testing` and `arkoren.dev/core/http/auth/authtest` for the suite:

```go
func TestAPIKeyAuthenticator(t *testing.T) {
	authtest.Run(t, apiKeyAuthenticator, anonymousRequest, validRequest,
		authtest.WithInvalid[User](forgedRequest),
	)
}
```

`authtest.WithUser` adds a check on the user that the valid request resolves
to.

For `authtest` and the other test helpers, see the
[testing API reference](overview.md).
