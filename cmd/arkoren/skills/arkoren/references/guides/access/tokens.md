---
title: API Tokens
description: Issue personal access tokens and device tokens, authenticate API requests with them and revoke them, with the access facade.
order: 50
summary:
  - Enable tokens with a scope catalog and a grant function, issue personal tokens and read them once.
  - Cap each user's token scopes, choose how often the cap is re-checked and switch a token kind off.
  - Authenticate API routes with bearer tokens and scopes, and know what a token can never do.
  - Trade a fresh login for a device token in a mobile app and log the device out.
---

# API Tokens

Scripts, integrations and mobile apps cannot use a browser cookie. Tokens give
them an opaque, revocable credential that the user created on purpose. Enable
them with the scopes your API understands and a function that says which of
them each user may hold. `apitoken` is `arkoren.dev/ark/access/apitoken`:

```go
iam.Tokens(func(tokens *apitoken.Builder) {
	tokens.Grants(apitoken.AnyScope, "posts:read", "posts:write")
})
```

`Grants` takes the function first and the scope catalog after it. The function
is required with a catalog, so a token can never be given more than its owner
may hold; [Scope Ceiling](#scope-ceiling) explains how to write one.
`apitoken.AnyScope` grants every catalog scope to every user, which is right only
when the scopes never confer more than the user's own permissions already do.

A token's text is `ark_<id>_<secret>`. The id is a public lookup key, and the
secret has 130 random bits and only its hash is stored, so a database leak does
not reveal usable tokens. Choose a prefix of 2 to 16 lowercase letters or digits
with `tokens.Prefix("acme")` so secret scanners recognize your tokens; tokens
issued under the old prefix stop being recognized when you change it. Without
`Grants`, tokens carry no scopes.

Both kinds of token are on by default. `tokens.PersonalTokens(false)` removes
`POST /tokens`, and `tokens.DeviceTokens(false)` removes `POST /device-tokens`
and `DELETE /device-tokens/current`; the listing and revocation routes stay
whenever tokens are enabled. The handler of a disabled kind panics at
composition, and `New` fails with `ErrInvalidConfig` when both are off.

## Personal Tokens

A user creates a token for a script or integration. `POST /tokens` needs a
browser session, a fresh login and a verified email (`403` with `account:email-unverified`):

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
or an expiry in the past is `422`. A scope of the catalog that the user may not
hold is `403` (`apitoken:scope-not-granted`). `expires_at` is optional. To cap lifetimes, set
`tokens.MaxTTL(d)`: a longer expiry is `422`, and a token without one expires
after the cap. The cap applies to device tokens too, but only when you set it:
with no `MaxTTL` (the default) a token without an expiry never expires by date.

`GET /tokens` lists the user's tokens, newest first, without secrets. Each entry
carries `id`, `kind`, `name`, `scopes`, `created_at`, `last_used_at` (accurate to
about an hour) and `expires_at`. `DELETE /tokens/{id}` revokes one and
answers `204`, or `404` for another user's token. Both need a browser session.

## Scope Ceiling

The function you pass to `Grants` receives the subject and returns the catalog
scopes that user may hold now. Return what the user's role allows; names outside
the catalog are ignored. Only `AnyScope` stands for the whole catalog, through a
private marker that your grant function has no reason to return, so a literal `"*"` in your
permission set is just a scope named `"*"`, which grants nothing unless your
catalog lists it:

```go
tokens.Grants(func(ctx context.Context, subject string) ([]string, error) {
	role, err := roles.Of(ctx, subject)
	if err != nil {
		return nil, err
	}
	if role == "admin" {
		return []string{"posts:read", "posts:write", "users:write"}, nil
	}

	return []string{"posts:read", "posts:write"}, nil
}, "posts:read", "posts:write", "users:write")
```

The result is a ceiling. A personal token may carry any subset of it, a device
token carries all of it, and a request for anything else is refused when the
token is created. The ceiling applies again when a token authenticates: the
token's scopes are the ones it was issued with, narrowed to what the user may
hold at that moment. A user who loses the admin role therefore loses `users:write` on
their existing tokens. `GrantTTL` sets how often that check runs:

| `GrantTTL` | The grant function runs | A change applies |
| --- | --- | --- |
| zero (the default) | on every request authenticated with a token | on the next request |
| positive, such as `time.Minute` | at most about once per user per duration, cached in the facade's store | within that duration |
| negative | only when a token is created | never: the scopes are fixed at creation |

```go
tokens.GrantTTL(time.Minute)
```

Use the default when the function is cheap or reads data you already loaded. Use a
duration when it queries a database on every request and a short delay is
acceptable. Use a negative value only when scopes should be a snapshot that the
user keeps until they revoke the token. Whatever the mode, a password reset or
any change of the user's version ends every token, and an error from the grant
function or the store fails the request with `401` instead of falling back to
the stored scopes.

## Authenticating Requests

Clients send `Authorization: Bearer <token>`. `iam.Middleware()` authenticates it
next to sessions, and routes check scopes as usual, with `gate` from
`arkoren.dev/core/auth/gate`:

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
   device token and ends the session. The device name must not be empty after
   trimming whitespace and at most 255 bytes, or the answer is `422`. The answer is
   `201` with the token and its secret, as for a personal token.
3. Every later call sends `Authorization: Bearer ...`.
4. To log out, `DELETE /device-tokens/current` revokes the presented token and
   answers `204`.

The login must be at most five minutes old, and a silent re-login does not extend
that; otherwise the answer is `403` and the user logs in again. A device token
carries every catalog scope the user may hold when it is issued. It stops working after 90 days without use,
which you change with `tokens.DeviceIdleTTL(d)` (a negative value turns the limit
off), and the app should still revoke it at logout. The idle time counts from the
last use, or from creation for a token never used. The last use is written at most
once an hour, so a token can lapse up to an hour early, and every request after a
lapse is `401` with no way to revive it: the user logs in again. `MaxTTL`, when
set, also ends a device token at a fixed date. Exchanging a login for a device token needs a verified email,
like creating a personal token.

A password change, enabling two-factor and a password reset revoke all the user's
tokens, and so does a deleted account. `iam.KeepPersonalTokens()` makes a password
change and enabling two-factor [revoke only device tokens](accounts.md#credential-changes),
for applications whose users depend on long-lived personal tokens. Tokens publish
`apitoken.Issued` and `apitoken.Revoked` events.

## Pruning

A token that can no longer authenticate still has a row. Expired tokens, tokens
that a version change of the user ended and device tokens that nobody uses would
stay in the table forever, so delete them on a schedule. `iam.Prune` removes, for
every user, tokens past `expires_at`, tokens issued before the user's current
version (a password reset or your own rotation; all tokens of a user who no longer
exists), device tokens whose last use is older than the idle limit, and device
tokens that were never used and were created before it. A disabled user's tokens
stay, because enabling the user brings them back. With `tokens.DeviceIdleTTL`
negative, the idle device tokens stay. Finding the stale versions reads each user
that holds a token once per run. Personal tokens are never pruned for idleness. It deletes
only rows that already answer `401`, so it changes no behavior, and it is safe to
run at any time and from several instances. Without tokens enabled it does nothing
and returns `nil`.

`iam.Prune` has the shape `func(context.Context) error`, so it is a
[scheduled task](../scheduled-tasks/calendar-schedules.md) as it stands. Run it
daily; it keeps the table small and does not need to run often. `schedule` is
`arkoren.dev/core/service/schedule`:

```go
err := scheduler.Add(schedule.NewEntry("access-prune", iam.Prune, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Hour(3))
}))
```

An error stops that run at the failing delete and the next run repeats the work.
The listing at `GET /tokens` shows expired tokens until a new token is issued or
`Prune` runs, but never a token that a version change ended: it can never
authenticate again, so the list leaves it out.

For every function and option, see
[the `apitoken` API reference](https://pkg.go.dev/arkoren.dev/ark/access/apitoken).
