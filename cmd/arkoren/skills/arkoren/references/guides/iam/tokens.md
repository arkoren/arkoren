---
title: API & Device Tokens
nav: API Tokens
description: Issue scoped personal access tokens for scripts and revocable device tokens for mobile apps, enforce live permission ceilings, and prune expired rows.
order: 90
summary:
  - Define what scopes tokens may hold, either automatically from IAM Permissions or with a custom grant function.
  - Issue personal access tokens, authenticate Bearer API routes, and control how quickly permission revocations take effect.
  - Exchange a fresh mobile login for a device token and schedule periodic pruning of unusable token rows.
---

# API & Device Tokens

Browser sessions work great for web apps, but scripts need a non-interactive credential and mobile apps need a revocable token instead of a browser cookie. IAM supports both with opaque, hashed Bearer tokens:

- **Personal access tokens**: Named, user-scoped credentials created from account settings for scripts and integrations.
- **Device tokens**: Revocable credentials issued to first-party mobile or desktop apps by exchanging a recent login.

Tokens identify their owner and carry scopes, but they **never** become a browser session or carry authentication factors. A stolen API token cannot change a password, enroll two-factor authentication, connect a provider, list other tokens, or mint new tokens.

## Defining the Scope Ceiling

Every token scope (such as `posts:read`) must belong to a **catalog** and fall within its owner's current **ceiling**: the scopes that user is allowed to hold right now.

If you already enabled [Roles & Permissions](permissions.md), simply enable `Tokens()` and IAM wires the ceiling automatically: the catalog is every permission in your permission store, and each user's ceiling is the permissions they currently hold:

```go
identity.Permissions(func(policy *permission.Builder) {
	policy.Roles(Viewer, Editor)
})
identity.Tokens()
```

If you are **not** using IAM's permission store, supply your own grant function (`func(context.Context, string) ([]string, error)`) and scope catalog using `apitoken` from `arkoren.dev/ark/iam/apitoken`:

```go
identity.Tokens(func(tokens *apitoken.Builder) {
	tokens.Grants(grantable, "posts:read", "posts:write")
})
```

Without `Grants` (and without `Permissions`), tokens carry no scopes. Passing a catalog without a grant function fails `iam.New`. `apitoken.AnyScope` is a built-in helper that grants every catalog scope to every user; use it only when your resource-level authorization already enforces all access rules. A literal `"*"` is treated as an ordinary scope name, not a wildcard.

Calling `identity.Tokens()` enables both personal and device tokens. Disable one kind inside the callback with `tokens.DeviceTokens(false)` or `tokens.PersonalTokens(false)` (disabling both fails construction).

## Issuing Personal Access Tokens

Creating a personal token requires a browser session, a [fresh proof](freshness.md), and a verified email. Send `POST /tokens`:

```json
{
  "name": "CI deploy script",
  "scopes": ["posts:read"],
  "expires_at": "2027-01-01T00:00:00Z"
}
```

IAM responds with `201 Created`, returning the token metadata in `token` and the full plaintext secret once in `secret`:

```json
{
  "token": {
    "id": "K3Q7..."
  },
  "secret": "ark_K3Q7..._..."
}
```

Show `secret` to the user immediately, because IAM stores only its SHA-256 hash and can never display it again. Secrets carry 130 random bits formatted as `<prefix>_<id>_<secret>`.

Names must be nonempty and at most 255 bytes. An unknown scope, invalid name, or past expiry returns `422`; requesting a catalog scope above the user's ceiling returns `403` `apitoken:scope-not-granted`.

By default, omitting `expires_at` creates a token with **no expiry date**. To enforce a maximum lifetime (which also applies to device tokens), set `MaxTTL`:

```go
identity.Tokens(func(tokens *apitoken.Builder) {
	tokens.MaxTTL(30 * 24 * time.Hour)
})
```

With `MaxTTL` set, omitted expirations default to that cap, and requesting a later date returns `422`.

## Authenticating API Requests

Clients present tokens in the `Authorization` header:

```http
Authorization: Bearer ark_<id>_<secret>
```

`identity.Middleware()` checks sessions first, then IAM tokens, then any custom authenticators. For an API group that must accept **tokens only** (rejecting browser cookies), mount `identity.Issuer().Authenticator()` directly using `auth` and `gate`:

```go
api := routes.With(auth.Middleware(identity.Issuer().Authenticator()), auth.Require())
api.With(gate.Require(gate.HasScopes("posts:read"))).Get("/api/posts", listPosts)
```

Malformed, unknown, expired, revoked, or version-outdated IAM tokens return `401` with a `Bearer` challenge. Tokens with a different prefix fall through to other authenticators, such as an [OIDC bearer verifier](../oauth/bearer-tokens.md). Disabled or deleted users cannot authenticate with tokens.

To help secret scanners recognize your app's tokens, set a custom 2–16 character lowercase alphanumeric prefix with `tokens.Prefix("acme")`. Changing the prefix stops recognizing tokens issued under the previous prefix.

## Rechecking Permissions on Requests

When a token authenticates, IAM intersects its originally issued scopes with its owner's current grant ceiling. If a user loses `posts:write`, their existing tokens lose it too; gaining a new permission never widens a token that was issued without it.

Control how often IAM queries the grant ceiling with `tokens.GrantTTL(d)`:

| Value | Behavior |
| --- | --- |
| `0` (default) | Checks on every token-authenticated request; revocations apply immediately. |
| Positive duration | Caches the ceiling per subject in `store`; revocations apply after the TTL expires. |
| Negative duration | Checks at creation only; revocations require revoking the token. |

If you use IAM's permission store with `policy.CacheTTL`, leave `GrantTTL` at `0` so revocations do not wait through two stacked caches. Grant-lookup or cache errors fail authentication closed with `401`.

## Mobile Device Tokens

First-party mobile and desktop apps use a temporary cookie jar only for login, then trade that session for a device token:

1. Complete password login (plus any second-factor challenge) or [mobile native login](federated-sign-in.md#mobile-native-login).
2. Within **5 minutes** of that login, send `POST /device-tokens` with the login cookie:

```json
{
  "device": "Ada's iPhone"
}
```

IAM responds with `201` and the same `token`/`secret` payload as a personal token, **ends the browser session**, and grants the token every catalog scope the user currently holds. The user must have a verified email, and the device name must be 1–255 trimmed bytes. If the login is older than 5 minutes, IAM returns `403` `no-step-up-method`; end the session and [start a fresh login](freshness.md#requiring-a-new-login).

Device tokens expire automatically after **90 days without recorded use** by default (personal tokens have no idle limit). Configure this with `tokens.DeviceIdleTTL(d)` (`0` uses 90 days; a negative value disables idle expiry). Because last-used timestamps are written at most once per hour, the cutoff can occur up to an hour early, and a lapsed token cannot be revived.

To sign a device out, send `DELETE /device-tokens/current` with that device's Bearer token (`204`). This endpoint accepts Bearer tokens only (either personal or device tokens), never a session cookie.

## Listing, Revoking, and Pruning

From account settings, a browser session can manage all of a user's tokens without needing a fresh proof:

- `GET /tokens` returns token metadata (newest first, never secrets), including kind, name, scopes, creation time, optional expiry, and optional last-use time. Tokens from an outdated credential version are hidden.
- `DELETE /tokens/{id}` revokes a single token (`204`, or `404` if unknown or owned by another user).

Password changes and enabling two-factor revoke all tokens by default unless you configure [`KeepPersonalTokens`](accounts.md#credential-change-revocation). Password resets and credential-version rotations always invalidate every token. Token issuance and revocation emit `apitoken.Issued` and `apitoken.Revoked`.

IAM never starts background cleanup goroutines on its own. Schedule `identity.Prune` daily with [Scheduled Tasks](../scheduled-tasks/calendar-schedules.md) to remove expired, version-outdated, deleted-user, and idle-lapsed device tokens:

```go
prune := schedule.NewEntry("access-prune", identity.Prune, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Hour(3))
})
err = scheduler.Add(prune)
```

`identity.Prune` is safe to repeat or run concurrently, returns `nil` when tokens are disabled, and leaves disabled users' tokens intact in case those accounts are re-enabled.

Next, learn how to inspect accounts, manage active sessions, suspend or delete users, and subscribe to [Sessions & Accounts](accounts.md).

For all token options and types, see the [`apitoken` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/apitoken).
