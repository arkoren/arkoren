---
title: API Tokens
description: Issue restricted personal tokens, authenticate API requests, and exchange a recent mobile login for a revocable device token.
order: 90
summary:
  - Limit token scopes to both an application catalog and the owner's current permissions.
  - Create, use, list, and revoke personal tokens without giving them credential-management access.
  - Exchange mobile sessions for device tokens and schedule cleanup of unusable token rows.
---

# API Tokens

A script needs a credential it can send without keeping an interactive login
session. A mobile app can also log in once, then retain a revocable credential
instead of a cookie. IAM supports both through opaque API tokens:
**personal tokens** are named and scoped by the user; **device tokens** are
issued by exchanging a recent login.

Tokens identify their owner but never become a session or carry authentication
factors. They cannot change a password, enroll two-factor, list other tokens,
or create more tokens.

## Define the Ceiling

A **scope** names an API permission, such as `posts:read`. The application
declares a catalog of names and a grant function that returns which catalog
scopes a subject may hold now. Assume your authorization layer provides
`grantable`, a `func(context.Context, string) ([]string, error)`.

Import `apitoken` from `arkoren.dev/ark/iam/apitoken` and enable tokens
in your existing constructor:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Tokens(func(tokens *apitoken.Builder) {
		tokens.Grants(grantable, "posts:read", "posts:write")
	})
})
```

The constructor shows token configuration only; retain your password or
provider login configuration. Apply the added migrations before using it.
A catalog without a grant function is invalid. Without a catalog, tokens
carry no scopes.

For example, if Ada's grant function returns only `posts:read`, she can create
a read-only token, but cannot request `posts:write`. A device token receives
all catalog scopes she may hold. Names returned outside the catalog are ignored.

`apitoken.AnyScope` is an alternative grant function that gives every user
every catalog scope. Use it only when your resource-level authorization
already limits those scopes to the owner's permissions. A literal `"*"`
returned by your own function is merely a scope named `"*"`, not a wildcard.

Both token kinds are enabled by `Tokens()`. In its callback,
`DeviceTokens(false)` removes the exchange and current-device revocation routes;
`PersonalTokens(false)` removes personal-token creation. Listing and individual
revocation remain. Disabling both makes construction fail.

## Create a Token

Personal-token creation requires a session, a
[fresh proof](freshness.md#renew-a-proof), and a verified email.
From Ada's account settings, send `POST /tokens`:

```json
{
  "name": "Read posts",
  "scopes": ["posts:read"],
  "expires_at": "2027-01-01T00:00:00Z"
}
```

The response is `201`, with metadata in `token` and the usable text in
`secret`. This abbreviated response shows their relationship:

```json
{
  "token": {
    "id": "K3Q7..."
  },
  "secret": "ark_K3Q7..._..."
}
```

Display the secret once for Ada to copy to her script. IAM stores only
its hash and cannot show it again. The default format is
`ark_<public id>_<secret>`, with 130 random bits in the secret.
Keep it out of logs and source control.

The name must be nonempty and at most 255 bytes. An unknown scope, past expiry,
or invalid name returns `422`; a catalog scope above Ada's grant ceiling
returns `403` `apitoken:scope-not-granted`.

An omitted `expires_at` has **no date expiry by default**. To enforce a maximum,
configure it alongside the catalog:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Tokens(func(tokens *apitoken.Builder) {
		tokens.Grants(grantable, "posts:read", "posts:write")
		tokens.MaxTTL(30 * 24 * time.Hour)
	})
})
```

With that cap, an omitted expiry becomes 30 days after issue, and a later
requested expiry returns `422`. The cap also applies to device tokens.
`MaxTTL(0)` means no cap.

## Authenticate Requests

The script sends the secret as an HTTP header:

```http
Authorization: Bearer ark_<id>_<secret>
```

`identity.Middleware()` tries the session first, then IAM tokens, then any
additional authenticators. For a token-only endpoint, reject cookie-only
callers by installing the issuer's authenticator instead of the combined
facade middleware. Import `auth` and `gate` from `arkoren.dev/core/auth`
and its `gate` subpackage:

```go
api := routes.With(auth.Middleware(identity.Issuer().Authenticator()), auth.Require())
posts := api.With(gate.Require(gate.HasScopes("posts:read")))
posts.Get("/api/posts", listPosts)
```

`routes` is the application's router; `listPosts` must also authorize which
posts the owner can read. On a mixed session/token endpoint, use the facade's
middleware, but do not assume a session identity carries token scopes.

Malformed, unknown, expired, revoked, or version-outdated IAM tokens receive
`401` with a Bearer challenge. A different prefix falls through to another
authenticator, such as an [OIDC verifier](../oauth/bearer-tokens.md).
A deleted or disabled user cannot authenticate with a token.
IAM tokens cannot pass a positive factor-level or freshness requirement.

If secret scanners need an application-specific prefix, set
`tokens.Prefix("acme")` in the token callback. It must be 2–16 lowercase
letters or digits. Changing it leaves stored rows intact but stops recognizing
tokens issued with the old prefix.

## Recheck Permissions

At authentication, IAM intersects a token's originally issued scopes
with its owner's grant ceiling. Losing `posts:write` therefore removes that
effective scope from existing tokens; gaining a permission does not add it
to a token that never held it.

Choose how quickly permission changes apply with `tokens.GrantTTL(d)`,
in the same token callback as `Grants`:

| Value | Grant lookup | Permission changes apply |
| --- | --- | --- |
| `0`, the default | Every token-authenticated request | Next request |
| Positive duration | Cached per subject in the shared store | After the cached grant expires |
| Negative duration | At creation only | Only after revoking and replacing the token |

Keep the default if your lookup is inexpensive. A short positive cache lifetime
trades lookup cost for delayed permission changes. A negative value lets
demoted users retain the scopes originally issued to their tokens.
Grant-function or cache errors fail authentication with `401`; IAM does
not fall back to a wider saved grant. Credential-version changes invalidate
every token regardless of this setting.

## Exchange a Mobile Login

A first-party app uses a cookie jar temporarily:

1. Complete password login and any challenge, or the provider's
   [native login](federated-sign-in.md#native-login).
2. While that login is at most five minutes old, exchange it for a device token.
3. Store the token in the platform's secure credential storage and use Bearer
   authentication thereafter.

For the exchange, send `POST /device-tokens` with the login cookie:

```json
{
  "device": "Ada's phone"
}
```

The response is `201` with the same `token`/`secret` envelope as a personal
token. It ends the login session, and the token carries every catalog scope
the user may hold. The user needs a verified email.
A blank trimmed device name or one over 255 bytes returns `422`.

An older login returns `403` `no-step-up-method`.
Confirmation or re-login in the same authenticated session does not renew
the exchange window; [start a new login](freshness.md#require-a-new-login).
The window uses application login age, not proof that the provider requested
credentials.

Device tokens stop working after 90 days without recorded use by default.
Set `tokens.DeviceIdleTTL(d)` to change that; zero selects 90 days, and a
negative value disables the idle limit. Idle time starts at creation for a
never-used token. Successful-use timestamps are written at most hourly,
so the idle cutoff can be up to an hour early; failed timestamp writes can
leave an older recorded time. A lapsed token cannot be revived by presenting it.
Any `MaxTTL` date cap still applies. Personal tokens have no idle limit.

To log the device out, send `DELETE /device-tokens/current` with its Bearer
token. `204` means that presented token was revoked; this route does not accept
a session as a substitute. It can revoke either kind of IAM token.

## Manage and Prune

Account settings use the session-only `GET /tokens` to list metadata, newest
first, without secrets. Entries include kind, name, scopes, creation time,
optional expiry, and optional last-use time. Expired rows can remain visible
until cleanup; tokens from an older credential version are hidden.
`DELETE /tokens/{id}` returns `204` for the user's token, or `404` for an
unknown token or someone else's token. Neither operation requires freshness.

Password changes and enabling two-factor revoke all tokens by default.
[Account policy](accounts.md#credential-changes) can retain personal tokens
for those two changes; a password reset or version rotation always ends all
tokens. Issuing and revoking publish `apitoken.Issued` and `apitoken.Revoked`.

IAM starts no cleanup goroutine. Schedule `identity.Prune` daily to delete
expired tokens, tokens from outdated or deleted users, and device tokens past
their idle limit. It removes only unusable rows, is safe to repeat or run
concurrently, and returns nil when tokens are not enabled.

With `schedule` from `arkoren.dev/core/service/schedule`, define the task:

```go
prune := schedule.NewEntry("access-prune", identity.Prune, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Hour(3))
})
```

Then add it to your existing [scheduler](../scheduled-tasks/calendar-schedules.md):

```go
err := scheduler.Add(prune)
```

A failed delete stops the run; the next run retries the remaining work.
Personal tokens are not pruned for idleness, and disabled-user status alone
does not cause version cleanup because re-enabling can restore those tokens.
Finding outdated versions reads each remaining token owner's current record.

Next, add [Federated Sign-In](federated-sign-in.md) if users should sign in through
external accounts.

For issuance, grants, and cleanup, see the
[`apitoken` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/apitoken).
