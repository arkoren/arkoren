---
title: Access Tokens
description: Issue personal access tokens and device tokens, authenticate API requests with them and revoke them, with the access facade.
order: 31
summary:
  - Enable tokens with a scope catalog, issue personal tokens and read them once.
  - Trade a fresh login for a device token in a mobile app and log the device out.
  - Authenticate API routes with scopes and know what a token can never do.
---

# Access Tokens

Enable opaque, revocable tokens, with the scopes your API understands:

```go
iam.Tokens(func(tokens *apitoken.Builder) {
	tokens.Scopes("posts:read", "posts:write")
})
```

A token's text is `ark_<id>_<secret>`. The id is a public lookup key; the secret
has 130 random bits and only its hash is stored, so a database leak does not reveal
usable tokens. Choose a prefix with `tokens.Prefix("acme")` so secret scanners
recognize your tokens.

## Personal Tokens

A user creates a token for a script or integration. `POST /tokens` needs a
browser session and a fresh login:

```json
{ "name": "CI deploy", "scopes": ["posts:write"], "expires_at": "2026-12-31T00:00:00Z" }
```

It answers `201` with the token and its secret, shown once:

```json
{ "token": { "id": "K3Q7...", "kind": "personal", "name": "CI deploy", "scopes": ["posts:write"] }, "secret": "ark_K3Q7..._..." }
```

An empty name, a scope outside the catalog or an expiry in the past (or beyond
`tokens.MaxTTL(d)`) is `422`. `GET /tokens` lists the user's tokens without
secrets and `DELETE /tokens/{id}` revokes one (`404` for another user's).

## Authenticate Requests

Clients send `Authorization: Bearer <token>`. `iam.Middleware()` authenticates
it next to sessions, and routes check scopes as usual:

```go
api := routes.With(iam.Middleware(), auth.Require())
api.With(gate.Require(gate.HasScopes("posts:write"))).Post("/api/posts", createPost)
```

A bearer value that does not start with the prefix is left for another
authenticator, such as [OIDC Tokens](oidc-bearer.md); a malformed, unknown,
expired or revoked token is `401` with a Bearer challenge. A token also ends when
its user is deleted or disabled, or when the user's version changes, which a
password reset does.

A token never carries factors and is never a browser session. `auth.RequireLevel`
and `guard.RequireFresh` reject it, and every credential route of the facade
refuses it, so a token can neither change a password, enroll a second factor nor
mint more tokens, even on a router that accepts both.

## Mobile Login

A first-party app logs in once and keeps a token, not a cookie. It uses a cookie
jar only for the login:

1. `POST /login` (and `POST /login/challenge` when a second factor is needed).
2. `POST /device-tokens` with `{"device": "Pixel 9"}` trades that login for a
   device token and ends the session. The login must be at most five minutes
   old, and a silent re-login does not extend that; otherwise the answer is
   `403`, log in again.
3. Every later call sends `Authorization: Bearer ...`.
4. To log out, `DELETE /device-tokens/current` revokes the presented token.

A device token carries every scope of the catalog and does not expire by itself.
Creating tokens and exchanging a login for a device token both need a verified
email (`403` email-unverified). A password change revokes the user's device
tokens but keeps their personal tokens unless you call
`iam.RevokeTokensOnPasswordChange()`; a reset revokes all of them.
Apps that sign in with Google or Apple use the native routes of
[Access Connections](access-connections.md) in step 1.

For every function and option, see
[the `apitoken` API reference](https://pkg.go.dev/arkoren.dev/ark/access/apitoken).
