---
title: Authentication Tests
description: Test identity-dependent handlers, protected routes, login sessions, and custom authenticators at distinct boundaries.
order: 30
summary:
  - Separate identity-dependent handler tests from authentication and authorization enforcement.
  - Use test-only credentials while retaining coverage of real login and session persistence.
  - Check custom authenticators with fresh request factories and shared contract assertions.
---

# Authentication Tests

A handler that uses the current user and an endpoint that enforces authentication need different tests. Inject an identity when the question is what the handler does for that user. Send credentials through authentication middleware when the question is who can reach the handler. Neither shortcut replaces a test of your actual login mechanism.

The fragments below run inside tests with `t *testing.T`. Import `context`, `net/http`, `net/http/httptest`, `arkoren.dev/core/auth`, and `arkoren.dev/core/http/router` as needed. The examples use this test identity:

```go
type testUser struct {
	ID int
}
```

## Inject Identity

For a handler test, attach an identity whose record holds the exact user type the handler reads with `request.User[testUser]`. With your handler named `showProfile`:

```go
incoming := httptest.NewRequest(http.MethodGet, "/me", nil)
ctx := auth.NewContext(incoming.Context(), auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
	principal.Record(auth.UserRecord{Subject: "42", Value: testUser{ID: 42}})
}))
res := router.Handler(showProfile).Record(incoming.WithContext(ctx))
```

Register response-body cleanup and assert the returned profile, following [HTTP Tests](http-tests.md#record-requests). This bypasses credential verification intentionally. It proves neither that anonymous requests are rejected nor that one user may access another user's data. Keep [authorization checks](../authorization/overview.md) in the route tests that exercise those decisions.

User types must match exactly: a `testUser` value does not satisfy a lookup for `*testUser`, and `request.User` reports a mismatch as `auth.ErrUnauthenticated`. A handler that only reads `request.Identity` needs no record.

## Exercise Enforcement

When testing the router's authentication and authorization path, supply a test-only authenticator instead of production credentials. This verifier accepts one fixed token; never enable it outside test code:

```go
authenticate := auth.Bearer(func(ctx context.Context, token string) (auth.Identity, error) {
	if token != "profile-test-token" {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	return auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Record(auth.UserRecord{Subject: "42", Value: testUser{ID: 42}})
	}), nil
})
```

Pass it into your route factory so tests reuse production middleware ordering and route registration. The essential wiring, with your `showProfile` handler, is:

```go
routes := router.New()
routes.Use(auth.Middleware(authenticate))
routes.With(auth.Require()).Get("/me", showProfile)
```

Send a valid token, register body cleanup, and assert the expected profile as well as HTTP 200:

```go
incoming := httptest.NewRequest(http.MethodGet, "/me", nil)
incoming.Header.Set("Authorization", "Bearer profile-test-token")
res := routes.Record(incoming)
```

In separate cases, omit the header and send a rejected token. With the default configuration both return HTTP 401, but through different paths: missing credentials pass `auth.Middleware` anonymously and `auth.Require` rejects the request; rejected credentials stop at authentication middleware. Assert the challenge header when it is part of your endpoint contract, and assert that protected side effects did not happen.

For an authenticated user without permission, assert your application's authorization denial separately. A successful identity lookup is not permission to access a resource.

## Preserve Login Coverage

Keep a test that submits valid credentials to the real login route, carries its session cookie to a protected route, logs out, and verifies subsequent rejection. [Multi-request HTTP tests](http-tests.md#continue-flows) explains cookie transfer and when to use a test server. Include invalid credentials and session persistence failure cases relevant to your application.

Share the production `auth.NewGuard(provider, store)` between authentication, login, and logout. Check errors from `guard.Begin`, `guard.Logout`, and session field mutations before asserting state; see [Session Login](../authentication/session-login.md) and [Save Changes](../http-capabilities/sessions.md#save-changes).

For a focused session-state test, `session.New(expiresAt, values)` creates a low-level session. Initial values must already have the exact Go types expected by your [typed fields](../http-capabilities/sessions.md#store-values); this initializer does not round-trip values through JSON. It does not test cookies, storage, or middleware persistence. A nil session pointer is absent, not usable state.

If production uses a custom session slot, allocate one `session.NewHandle()` and share it across session middleware, the identity, and request lookup. Attach test state with `session.NewContextFor(ctx, handle, current)`. Separate handles select separate context slots; they do not isolate cookies or storage. Zero handles select the default slot.

## Check Authenticators

For your own authenticator, `authtest.Run` checks the public authentication contract. Import `arkoren.dev/core/auth/authtest`. Here `apiKeyAuthenticator` is your implementation, and each application helper returns a fresh `*http.Request` carrying the credentials and context for its case:

```go
func TestAPIKeyContract(t *testing.T) {
	authtest.Run(t, apiKeyAuthenticator, anonymousRequest, validRequest,
		authtest.WithInvalid(rejectedRequest),
		authtest.WithIdentity(func(t *testing.T, identity auth.Identity) {
			if identity.Subject() != "42" {
				t.Fatalf("subject = %q, want 42", identity.Subject())
			}
		}),
	)
}
```

The suite checks that absent credentials yield `auth.ErrNoCredentials`, valid credentials resolve and store an identity, and rejected credentials yield `auth.ErrUnauthenticated` and stop middleware. `WithInvalid` enables rejection checks; without it those checks are skipped. Add application tests for credential expiry, revocation, and backend failures beyond that shared contract.

See the [`auth` API reference](https://pkg.go.dev/arkoren.dev/core/auth) and [`authtest` suite](https://pkg.go.dev/arkoren.dev/core/auth/authtest).
