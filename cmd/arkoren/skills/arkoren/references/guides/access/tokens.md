---
title: API Tokens
description: Issue personal access tokens and device tokens, authenticate API requests with them and revoke them, with the access facade.
order: 50
summary:
  - Enable tokens with a scope catalog, issue personal tokens and read them once.
  - Authenticate API routes with bearer tokens and scopes, and know what a token can never do.
  - Trade a fresh login for a device token in a mobile app and log the device out.
---

# API Tokens

Scripts, integrations and mobile apps cannot use a browser cookie. Tokens give
them an opaque, revocable credential that the user created on purpose. Enable
them with the scopes your API understands:

```go
iam.Tokens(func(tokens *apitoken.Builder) {
	tokens.Scopes("posts:read", "posts:write")
})
```

A token's text is `ark_<id>_<secret>`. The id is a public lookup key, and the
secret has 130 random bits and only its hash is stored, so a database leak does
not reveal usable tokens. Choose a prefix of 2 to 16 lowercase letters or digits
with `tokens.Prefix("acme")` so secret scanners recognize your tokens; tokens
issued under the old prefix stop being recognized when you change it. Without a
scope catalog, tokens carry no scopes. For the core token model, see
[API Tokens](../authentication/api-tokens.md).

## Personal Tokens

A user creates a token for a script or integration. `POST /tokens` needs a
browser session, a fresh login and a verified email (`403` `email-unverified`):

```json
{ "name": "CI deploy", "scopes": ["posts:write"], "expires_at": "2026-12-31T00:00:00Z" }
```

The answer is `201` with the token and its secret, which is shown once:

```json
{
  "token": { "id": "K3Q7...", "kind": "personal", "name": "CI deploy", "scopes": ["posts:write"] },
  "secret": "ark_K3Q7..._..."
}
```

A name is required, at most 255 bytes. An empty name, a scope outside the catalog
or an expiry in the past is `422`. `expires_at` is optional. To cap lifetimes, set
`tokens.MaxTTL(d)`: a longer expiry is `422`, and a token without one expires
after the cap.

`GET /tokens` lists the user's tokens, newest first, without secrets. Each entry
carries `id`, `kind`, `name`, `scopes`, `created_at`, `last_used_at` (accurate to
about five minutes) and `expires_at`. `DELETE /tokens/{id}` revokes one and
answers `204`, or `404` for another user's token. Both need a browser session.

## Authenticating Requests

Clients send `Authorization: Bearer <token>`. `iam.Middleware()` authenticates it
next to sessions, and routes check scopes as usual:

```go
api := routes.With(iam.Middleware(), auth.Require())
api.With(gate.Require(gate.HasScopes("posts:write"))).Post("/api/posts", createPost)
```

A bearer value that does not start with the prefix is left for another
authenticator, such as [OIDC bearer tokens](../oauth/bearer-tokens.md). A
malformed, unknown, expired or revoked token is `401` with a Bearer challenge. A
token also ends when its user is deleted or disabled, or when the user's version
changes, which a password reset does.

> [!NOTE]
> A token never carries factors and is never a browser session. `auth.RequireLevel`
> and `guard.RequireFresh` reject it, and every credential route of the facade
> refuses it, so a token can neither change a password, enroll a second factor nor
> mint more tokens, even on a router that accepts both.

## Mobile Login

A first-party app logs in once and keeps a token, not a cookie. It uses a cookie
jar only for the login:

1. `POST /login`, and `POST /login/challenge` when a second factor is needed.
   Apps that sign in with Google or Apple use the
   [native routes](connections.md#native-login) here instead.
2. `POST /device-tokens` with `{"device": "Pixel 9"}` trades that login for a
   device token and ends the session. The answer is `201` with the token and its
   secret, as for a personal token.
3. Every later call sends `Authorization: Bearer ...`.
4. To log out, `DELETE /device-tokens/current` revokes the presented token and
   answers `204`.

The login must be at most five minutes old, and a silent re-login does not extend
that; otherwise the answer is `403` and the user logs in again. A device token
carries every scope of the catalog and does not expire by itself, so the app must
revoke it at logout. Exchanging a login for a device token needs a verified email,
like creating a personal token.

A password change, enabling two-factor and a password reset revoke all the user's
tokens, and so does a deleted account. `iam.KeepPersonalTokens()` makes a password
change and enabling two-factor [revoke only device tokens](accounts.md#credential-changes),
for applications whose users depend on long-lived personal tokens. Tokens publish
`apitoken.Issued` and `apitoken.Revoked` events.

For every function and option, see
[the `apitoken` API reference](https://pkg.go.dev/arkoren.dev/ark/access/apitoken).
