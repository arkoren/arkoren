---
title: Sessions and Accounts
nav: Sessions & Accounts
description: Manage active logins, suspend or remove users, and choose how credential changes and recovery affect existing access.
order: 100
summary:
  - List and revoke session logins without mistaking them for device tokens.
  - Set shared session policy and distinguish reversible suspension from credential-version rotation.
  - Delete user-owned data through forget hooks and observe security changes without relying on notifications for enforcement.
---

# Sessions and Accounts

After login works, users need to recognize their active sessions and end
access they no longer trust. Your application also needs administrative ways
to suspend or delete a user. IAM separates **login records** in the cache
from the user and credentials in the database; deleting one session is not
the same as revoking every credential.

This page assumes the [facade is mounted](overview.md#mount-routes).
Session-management routes require a guard-authenticated session, not a bearer
token. Administrative Go methods belong behind your application's own
authorization, never on unprotected routes.

## Manage Sessions

`GET /sessions` returns `200` with the user's active login records.
An abbreviated entry looks like:

```json
{
  "id": "Hq3...",
  "created_at": "2026-11-15T09:30:00Z",
  "seen_at": "2026-11-15T10:02:00Z",
  "current": true
}
```

The actual response is an array. By default entries also include `user_agent`
and `ip`; those are personal data. `seen_at` can lag by five minutes or more,
so do not present it as an exact activity timestamp.

To end one listed login, send `DELETE /sessions/{id}`.
Success is `204`, including for an unknown ID. The current login may also be
revoked this way. To end every login except the caller's, send
`DELETE /sessions`. Neither operation needs freshness, so a user can react
to suspicious activity without first finding another credential.

These requests do not revoke API tokens. A mobile login exchanged for a device
token is no longer a session; manage it through the
[token routes](tokens.md#manage-and-prune).

## Session Policy

The facade gives its guard and session middleware the same absolute lifetime:
24 hours by default. Configure that once, not on each component independently:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher)
	identity.SessionLifetime(7 * 24 * time.Hour)
})
```

This changes the maximum login age; it does not promise seven days without an
idle expiry, cache eviction, revocation, or credential change.
For idle lifetime and renewal, configure the session policy through
`identity.Session` and follow the [session guide](../http-capabilities/sessions.md).

For example, to use an application-specific cookie name, import `session`
from `arkoren.dev/core/http/session`:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Session(func(cookies *session.Builder) {
		cookies.Name("__Host-acme.session")
	})
})
```

Keep the host-prefixed cookie's required secure, root-path, no-domain settings.
This is a session-setting fragment; retain the login features you need.
The facade applies its own absolute lifetime and default session handle after
your callback. A `Guard` callback likewise cannot override the shared lifetime,
handle, or client-address function.

## Credential Changes

A change intended to protect an account must not leave an attacker's earlier
login working. IAM applies these defaults:

| Change | Session effect | Token effect |
| --- | --- | --- |
| Password change | End other logins; keep the caller | Revoke all |
| Enable two-factor | End other logins; keep the caller | Revoke all |
| Password reset | Rotate version; end all sessions and pending logins | Invalidate all |
| Provider takeover of an unverified account | Rotate version; invalidate previous sessions and pending logins | Delete all |

If users depend on long-lived personal tokens for scripts, you can preserve
them for the first two changes. This also preserves personal tokens an
attacker may have created using the old password. Accept that risk explicitly
before configuring:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher)
	identity.Tokens()
	identity.KeepPersonalTokens()
})
```

Device tokens are still revoked. `KeepPersonalTokens` never exempts a token
from a password reset, version rotation, or account deletion.
The individual feature pages describe failure and recovery behavior;
[Fresh Logins](freshness.md#credential-routes) owns the route requirements.

## Suspend a User

The built-in directory exposes `Disable(ctx, subject)` for reversible
suspension. The next session, pending-login, token, or provider-login check
rejects the disabled user. It does not delete their data.
`Enable(ctx, subject)` clears the disabled flag.

Disabling **does not** change the credential version. An unexpired token or
otherwise valid session can become usable again after re-enabling.
For offboarding, rotate the version as well, using the same database
transaction:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := identity.Directory().Disable(ctx, subject); err != nil {
		return err
	}

	return identity.Directory().Rotate(ctx, subject)
})
```

The old version's sessions, pending logins, and tokens remain invalid even
after re-enabling. Rotation does not delete passwords, connections, or
authenticators. Keep the user disabled while they must not sign in.

The [provisioning example](overview.md#add-a-user) covers adding directory users.
For provider-only invitations, use the different
[unverified invitation path](sso.md#admit-users), rather
than assuming `CreateVerified` connects an unknown provider identity.

## Delete an Account

`DELETE /account` requires the current user's session and a fresh login.
On success it returns `204`, deletes the built-in user and the enabled
features' SQL rows, and ends the caller's session.
This is destructive; your application decides how to confirm the deletion
and whether additional business records must be removed or retained.

The directory calls the shared user provider's **forget hooks** before
deleting the user. Each hook receives the subject and the transaction context.
To include application-owned posts, register a hook on that provider:

```go
identity.UserProvider().OnForget(func(ctx context.Context, subject string) error {
	return posts.DeleteBySubject(ctx, subject)
})
```

Here `posts.DeleteBySubject` is your application's subject-filtered deletion
using the same database and context. A hook error prevents the SQL transaction
from committing. Cache changes or external side effects do not gain SQL
rollback merely because their hook runs in that context.
The guard's hook also removes cached login records.

For administrative deletion, `identity.Directory().Delete(ctx, subject)` follows
the same forget path. Deletion emits `account.UserDeleted` and core's
`auth.UserForgotten`; an unknown subject returns `auth.ErrUserNotFound`.
With [custom users](own-users.md#delete-your-users), the built-in account
route is absent and you own the deletion transaction.

## Security Notifications

Events let you notify a user or record an audit observation. Pass your existing
event bus when constructing the facade:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher)
	identity.Events(bus)
})
```

The feature events carry local subjects, not emails, passwords, tokens, codes,
or external identifiers:

| Package | Events |
| --- | --- |
| `account` | `UserCreated`, `EmailVerified`, `UserDeleted` |
| `password` | `Changed`, `WasReset` |
| `twofactor` | `Enabled`, `Disabled`, `Regenerated`, `RecoveryUsed` |
| `apitoken` | `Issued`, `Revoked` |
| `federation` | `LinkCreated`, `LinkDeleted`, `AccountClaimed` |

Core additionally emits credential-enrollment, user-forgotten, and
second-factor-lockout events. Use the normal
[listener lifecycle](../events/listeners.md) and keep callbacks short:
they run in-process, not on an email worker. Notifications are not a durable
audit log or a place to enforce a security decision.

In particular, resetting an **already verified** account keeps its provider
connections: they may be legitimate ways the owner signs in.
After `password.WasReset`, review them under your recovery policy:

```go
connections, err := identity.Links().List(ctx, subject)
```

Only call this when providers are enabled; `identity.Links()` is nil otherwise.
Ask the owner to review the list, or use `UnlinkAll(ctx, subject)` if your
application's explicit policy revokes every connection on recovery.
A provider takeover of an unverified account already purges connections and
emits `federation.AccountClaimed`; review application-owned credentials too.
A listener cannot extend or roll back the reset transaction.
If your policy requires atomic removal from an already verified account,
provide an application-owned transactional recovery flow.
`OnEmailProven` handles unverified-mailbox cleanup only; it is not a hook for
every reset.

For an organization-specific application, continue with
[Organization Sign-In](sso.md).

For directory administration and facade hooks, see the
[`account` API](https://pkg.go.dev/arkoren.dev/ark/iam/account) and
[`iam` API](https://pkg.go.dev/arkoren.dev/ark/iam).
