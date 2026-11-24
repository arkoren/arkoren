---
title: Sessions & Accounts
nav: Sessions & Accounts
description: Inspect the signed-in account, manage active sessions, configure session policy, suspend or delete users, and listen to security events.
order: 100
summary:
  - Enable GET /account to expose IAM's view of the current user to your front end.
  - List and revoke active browser sessions, and configure shared session lifetime and cookie settings.
  - Control credential-change revocation, suspend or delete users cleanly, and subscribe to security events.
---

# Sessions & Accounts

Once users can sign in, they need to see who they are signed in as, inspect their active sessions, and sign out devices they no longer recognize. Your application also needs clean administrative controls to suspend or delete accounts and react to security-sensitive changes.

IAM separates **login records** (stored in your cache) from **users and credentials** (stored in your database). Ending one session does not revoke API tokens or credentials; deleting an account removes everything together.

## Inspecting the Current Account

Front ends often need a lightweight endpoint that returns who is signed in, whether their email is verified, what roles they hold, and whether two-factor is active. Enable `GET /account` with `identity.Account(...)`:

```go
identity.Account(func(view *iam.AccountBuilder) {
	view.Permissions(true)
	view.TwoFactor(true)
	view.Login(true)
})
```

`GET /account` responds with `200` for both browser sessions and API tokens (`401` for anonymous requests or pending two-factor logins). Without calling `identity.Account(...)`, the route is not mounted.

By default, the response contains `subject` and the user's email facts (`email`, `email_verified`, plus `verified_at` and `created_at` with the built-in directory). Each optional builder method adds one section:

| Option | Fields added |
| --- | --- |
| `Permissions(true)` | `roles` and `permissions` (same as `GET /me/permissions`) |
| `TwoFactor(true)` | `two_factor` boolean (whether an authenticator is confirmed) |
| `Login(true)` | `login` object: `session`, `level` (`none`, `single`, `multi`), and core `methods` (`pwd`, `otp`, `hwk`, `swk`) |

With everything enabled for a built-in user signed in with a password and TOTP:

```json
{
  "subject": "3f9c2a7e1b8d4c6fa0e5d7b9c1a3f2e4",
  "email": "ada@example.com",
  "email_verified": true,
  "verified_at": "2026-11-02T09:14:00Z",
  "created_at": "2026-10-28T17:40:12Z",
  "roles": ["editor"],
  "permissions": ["posts:read", "posts:write"],
  "two_factor": true,
  "login": { "session": true, "level": "multi", "methods": ["pwd", "otp"] }
}
```

For an API token request, `login` reports `{"session": false, "level": "none", "methods": []}`. Calling `Permissions(true)` without `identity.Permissions` or `TwoFactor(true)` without `identity.TwoFactor` is ignored with one warning.

IAM never encodes your application's profile fields (like display name or billing plan). To combine `iam.AccountView` with your own profile data in a custom `GET /me` handler, call `identity.Account(r.Context())`; it works even when `GET /account` is not mounted. See [Building Your Own `/me` Endpoint](own-users.md#building-your-own-me-endpoint).

## Managing Active Sessions

Every IAM facade mounts three session-management routes automatically (browser session required, no freshness check needed so users can react to suspicious activity immediately):

- `GET /sessions` returns a `200` JSON array of the user's active logins (`id`, `created_at`, `seen_at`, `current`, plus `user_agent` and `ip` by default). Treat `user_agent` and `ip` as personal data, and note that `seen_at` can lag by five minutes or more.
- `DELETE /sessions/{id}` ends one session (`204`, even for an unknown ID). Users can revoke another device or their current session.
- `DELETE /sessions` ends every active session *except* the caller's (`204`).

These endpoints manage browser sessions only. Mobile logins that were exchanged for a device token are managed through the [token routes](tokens.md#listing-revoking-and-pruning).

## Session Lifetime and Cookies

IAM keeps the guard and session middleware synchronized on one absolute session lifetime, **24 hours** by default. Change it in one place:

```go
identity.SessionLifetime(7 * 24 * time.Hour)
```

This sets the maximum lifetime; idle expiry, cache eviction, manual revocation, or credential changes can still end a session sooner. To customize cookie settings such as the cookie name, idle lifetime, or renewal, use `identity.Session(...)` with `session` from `arkoren.dev/core/http/session`:

```go
identity.Session(func(cookies *session.Builder) {
	cookies.Name("__Host-acme.session")
})
```

Keep required `__Host-` cookie invariants (secure, root path, no domain). IAM applies its shared lifetime and session handle after your `Session` and `Guard` callbacks so they stay consistent. See [Sessions](../http-capabilities/sessions.md) for cookie and storage behavior.

## Credential-Change Revocation

When a user secures their account, IAM revokes older sessions and credentials by default so an attacker cannot keep using stolen access:

| Event | Sessions | Tokens |
| --- | --- | --- |
| Password change | Ends other sessions; keeps caller | Revokes all |
| Enable two-factor | Ends other sessions; keeps caller | Revokes all |
| Password reset | Rotates version; ends all sessions and pending logins | Invalidates all |
| Provider takeover of unverified account | Rotates version; invalidates prior sessions and pending logins | Deletes all |

If your users rely on long-lived personal access tokens for scripts and you explicitly accept the risk that an attacker with the old password could have minted one, preserve personal tokens across password changes and two-factor enrollment:

```go
identity.KeepPersonalTokens()
```

Device tokens are still revoked, and `KeepPersonalTokens()` never exempts any token from a password reset, version rotation, or account deletion.

## Suspending Users

For temporary suspension with the built-in directory, call `identity.Directory().Disable(ctx, subject)` (and `Enable(ctx, subject)` to restore access). A disabled user is immediately rejected on their next session, pending-login, token, or provider check without deleting their data.

Disabling alone does **not** bump the user's credential version: existing unexpired tokens or sessions would work again if the account is re-enabled. For permanent offboarding, disable and rotate the version in one database transaction:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := identity.Directory().Disable(ctx, subject); err != nil {
		return err
	}

	return identity.Directory().Rotate(ctx, subject)
})
```

## Deleting Accounts

With the built-in directory, `DELETE /account` lets a signed-in user with a [fresh login](freshness.md) delete their account (`204`). For administrative deletion, call `identity.Directory().Delete(ctx, subject)` (`auth.ErrUserNotFound` if missing).

Before deleting the user row, IAM runs every registered **forget hook** inside the same database transaction, removing passwords, two-factor secrets, tokens, connections, permission grants, and cached login records. Register your own forget hook to delete application-owned data in that same transaction:

```go
identity.UserProvider().OnForget(func(ctx context.Context, subject string) error {
	return posts.DeleteBySubject(ctx, subject)
})
```

If any hook returns an error, the SQL transaction rolls back and the user stays intact. Keep in mind that cache updates or external API calls inside a hook do not participate in SQL rollback. Deletion emits `account.UserDeleted` and core's `auth.UserForgotten`.

## Security Events

To send security alert emails or record audit observations, attach your application's [event bus](../events/overview.md) to IAM:

```go
identity.Events(bus)
```

IAM events carry only local `subject` identifiers, never emails, passwords, tokens, codes, or external IDs:

| Package | Events |
| --- | --- |
| `account` | `UserCreated`, `EmailVerified`, `UserDeleted` |
| `password` | `Changed`, `WasReset` |
| `twofactor` | `Enabled`, `Disabled`, `Regenerated`, `RecoveryUsed` |
| `apitoken` | `Issued`, `Revoked` |
| `federation` | `LinkCreated`, `LinkDeleted`, `AccountClaimed` |
| `permission` | `GrantCreated`, `GrantRevoked`, `HolderOrphaned` |

Core also emits `auth.CredentialEnrolled`, `auth.UserForgotten`, and `auth.SecondFactorLocked`. Keep [event listeners](../events/listeners.md) short: they run in-process, cannot roll back the triggering transaction, and are not a substitute for transactional hooks or a durable audit log.

For example, because resetting an **already verified** account preserves its external provider connections, you can listen for `password.WasReset` to notify the user to review `identity.Links().List(ctx, subject)` (check that `identity.Links() != nil` when providers are optional) or call `UnlinkAll` if your policy requires revoking connections on recovery.

Next, see [Custom User Stores](own-users.md) if your application already maintains its own users table or needs custom credentials.

For directory and facade APIs, see [`account`](https://pkg.go.dev/arkoren.dev/ark/iam/account) and [`iam`](https://pkg.go.dev/arkoren.dev/ark/iam).
