---
title: OIDC Bearer Verification
nav: OIDC Bearer Tokens
description: Verify provider-issued JWT access tokens for an API, require the exact identity type, and map verified claims to an application user.
order: 50
---

# OIDC Bearer Verification

## Introduction

An API client can present a JWT access token issued by your identity provider.
Your service verifies that token and resolves the identity for the request;
it does not need a browser login route or a session. Use `oauthauth.Client.Verify`
with [`auth.Bearer`](api-tokens.md#accepting-bearer-tokens) for this task.

This guide accepts existing tokens. The provider or a separately implemented
client flow acquires and refreshes them. Arkoren does not provide a full OAuth
authorization server, token endpoint, or client acquisition flow. Plain OAuth2
providers such as the GitHub preset do not support `Verify`; opaque tokens need
an application verifier or introspection integration instead.

## Preparing the verifier

Install `arkoren.dev/driver/oauth` and import
`oauth "arkoren.dev/driver/oauth/oauthauth"`. Register your API audience at the
provider and explicitly configure the audiences intended for this API. Prefer
distinct API and browser-client audiences; do not treat audience overlap as proof
of token purpose. With an existing `cipher` (`*crypto.Encrypter`) and registered
`clientID`, construct a verifier:

```go
client, err := oauth.New(
	oauth.OIDC("https://id.example.com/realms/app"),
	cipher,
	clientID,
	func(client *oauth.ClientBuilder) {
		client.Audiences("https://api.example.com")
	},
)
```

Handle the constructor error before using `client`. The constructor requires
a non-nil encrypter and nonempty client ID even for bearer-only use. It does
not perform discovery. No redirect URL or client secret is needed for this
verification path.

Import `application` from `arkoren.dev/core/application` for lifecycle wiring.
Register discovery before serving requests, using your existing application:

```go
compose := func(composer *application.Composer) {
	composer.Startup(client.Start)
}
```

`Start` discovers the provider; signing-key retrieval may occur during token
verification. `Verify` checks signature, expiry, issuer, a nonempty final subject
after provider-specific mapping, and whether at least one token audience matches
a configured audience. Without `Audiences`, it
returns `ErrNoAudiences`; before successful discovery it returns `ErrNotStarted`.
It also rejects tokens containing a `nonce` claim or a `typ` claim equal to
`ID` (case-insensitive) in the JWT payload. These are purpose heuristics, not a
universal access-token profile: an unmarked JWT with an accepted audience can
pass. The adapter neither requires every provider to use `at+jwt` nor rejects
audience overlap by itself.

Choose an audience policy that excludes browser ID tokens and enforce the
provider's additional token-purpose rules in your application verifier. If those
rules need checks beyond `client.Verify`, wrap it with those checks or use a
provider-specific verifier. No generic marker check substitutes for that policy,
and a verified token still needs application authorization.

When native discovery or the native token verifier returns an error after
cancellation or a deadline, the returned chain retains that native error and makes
`context.Canceled` or `context.DeadlineExceeded` identifiable with `errors.Is`.
Configuration exits and local claim/policy denials are not automatically joined
with context errors. A cached verification need not fail on an ended context.
Do not dump native error chains into logs: they can include provider-controlled
claims. Optional
[token-rejection events](oauth.md#events) carry only an opaque bounded error,
not that native cause.

The same started OIDC client can handle
[browser login and API verification](oauth.md#protecting-an-api).
`ClientBuilder.Audiences` applies only to `Verify`; browser callbacks always
require the positional `clientID` passed to `oauth.New` in the ID token's `aud`.
For combined use, also configure the browser redirect URL and credentials required
by the provider.

## Requiring an identity

Import `auth` from `arkoren.dev/core/http/auth` and `request` from
`arkoren.dev/core/http/request`. `api` is your existing router group. The
`context` package used by the mapping example is from the standard library.

For an API that works directly with provider identities, keep the exact type
returned by `Verify` through authentication, required-user checks, and handlers.

The examples below assume its checks and your audience configuration satisfy the
provider's access-token policy. If additional purpose checks are required, pass
your application verifier to `auth.Bearer` in place of `client.Verify`.

```go
api.Use(auth.Middleware(auth.Bearer(client.Verify)))
api.Use(auth.Require[oauth.Identity]())
```

`auth.Middleware` alone allows a request with no credentials to continue
anonymously. `auth.Require[oauth.Identity]()` closes that path. Invalid tokens
become `401` with `WWW-Authenticate: Bearer error="invalid_token"` through
`auth.Bearer`; the response does not expose the verifier's error message.

Read the same type in a handler:

```go
identity, err := request.User[oauth.Identity](r)
if err != nil {
	return err
}
```

`oauth.Identity`, `*oauth.Identity`, and your application's `User` are different
context keys. Asking for a different type produces a missing-user result, not
a conversion. Scope policy for this identity belongs in
[Writing Policies](../authorization/policies.md#token-scopes), and resource
visibility belongs in [Row-Level Access](../authorization/row-access.md).
`oauth.HasScopes(required...)` captures an independent copy of the required
scope names, so later edits to your input slice cannot weaken an existing rule.
It checks an identity's scopes; it does not authenticate a request or grant
resource access by itself.

## Mapping to an application user

If handlers need your local user, wrap verification with a lookup by issuer
and subject. Here `users.ByIdentity` is your application's existing lookup,
returning `User`; `User.Scopes` is a request-local `[]string` field. Import
`slices` from the standard library for the scope copy:

```go
verifyUser := func(ctx context.Context, token string) (User, error) {
	identity, err := client.Verify(ctx, token)
	if err != nil {
		return User{}, err
	}
	if identity.Issuer == "" || identity.Subject == "" {
		return User{}, auth.ErrUnauthenticated
	}

	user, err := users.ByIdentity(ctx, identity.Issuer, identity.Subject)
	if err != nil {
		return User{}, err
	}
	user.Scopes = slices.Clone(identity.Scopes)

	return user, nil
}
```

The example's `client` and `users` are injected collaborators in the enclosing
application. The lookup must
return an error other than `auth.ErrNoCredentials` for an unknown or disabled
account, so a presented token fails instead of falling through to another
authenticator. Do not mutate a shared cached user when attaching token scopes.

Wire `auth.Bearer(verifyUser)` with `auth.Require[User]()`, not
`auth.Require[oauth.Identity]()`. Check `User.Scopes` with a
[User-typed scope policy](../authorization/policies.md#token-scopes);
`oauth.HasScopes` expects `oauth.Identity` and therefore cannot read this user.
Token scopes limit the client's authority and do not replace the user's
resource permissions.

Never link accounts by email alone. Generic `OIDC` passes through the email
claim even if it is unverified; see the
[provider email behavior](oauth.md#mapping-identities-to-users) before trusting
an address.

## Using the verifier with MCP

Apply the same bearer and required-user middleware outside an MCP HTTP mount.
The Arkoren CLI sends an existing bearer API key, JWT, or OAuth access token;
it does not acquire or refresh one. Protocol metadata, challenges, endpoint
admission, tool availability, and resource checks are separate concerns in
[Securing MCP](../mcp/securing-mcp.md). Authentication alone does not authorize
every advertised tool or every resource it accesses.

For every function and option, see the
[`oauthauth` API reference](https://pkg.go.dev/arkoren.dev/driver/oauth/oauthauth).
