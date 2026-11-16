---
title: Sessions and Accounts
nav: Sessions & Accounts
description: List and end logins, delete an account, and see how the facade protects credential changes with fresh logins and verified emails.
order: 70
summary:
  - List a user's logins and end one or all others from the sessions routes.
  - Delete an account together with every feature's rows in one transaction.
  - Know which routes need a browser session, a fresh login or a verified email, and what a changed credential ends.
---

# Sessions and Accounts

Users expect to see where they are logged in, end a login they do not recognize
and delete their account. These routes are always mounted, except account deletion,
which belongs to the built-in users. This page also gathers the rules that keep a
stolen session or an unverified address from taking over an account.

## Sessions

`GET /sessions` answers `200` with the user's logins as a JSON array. The login of
the request itself is marked `current`:

```json
[
  {
    "id": "Hq3...",
    "created_at": "2026-11-15T09:30:00Z",
    "seen_at": "2026-11-15T10:02:00Z",
    "user_agent": "Mozilla/5.0 ...",
    "ip": "203.0.113.7",
    "current": true
  }
]
```

`user_agent` and `ip` are present when the guard records devices, which is the
default, and are personal data. `seen_at` can lag by five minutes or more.

`DELETE /sessions/{id}` ends the login with that id, on another device or this one,
and answers `204`. An unknown id changes nothing. `DELETE /sessions` ends every
login of the user except the one that made the request, and answers `204`. All
three routes need a browser session; a bearer token is `401`. They do not need a
fresh login.

## Deleting Accounts

`DELETE /account` deletes the logged-in user and answers `204`. It needs a browser
session and a fresh login. The user's row and every feature's rows (passwords,
two-factor, tokens, connections, pending verifications and login records) are
removed in one transaction by the provider's forget hooks, and a failure of any
leaves the user in place. The request's own session ends, and the facade publishes
`account.UserDeleted` and core's `auth.UserForgotten`.

With [your own users](own-users.md) the route is absent: you delete your users, and
you call `provider.Forget` in the same transaction to remove every feature's rows.
To remove rows of your own tables in the same way, register a hook on that
provider:

```go
iam.UserProvider().OnForget(func(ctx context.Context, subject string) error {
	return posts.DeleteBySubject(ctx, subject)
})
```

## Credential Changes

A stolen cookie or token must not be enough to take an account over. So every
handler that adds, changes or removes a credential requires a browser session and a
fresh login. A bearer token, even a valid one, is rejected with `401`, so a stolen
token can never enroll a second factor or change a password.

A login is **fresh** when the user proved a factor of one of their step-up methods
(`pwd` or `otp` by default) within the window, 15 minutes by default. Set another
window with `iam.Guard(func(policy *auth.GuardBuilder) { policy.FreshFor(...) })`.
A user who has no step-up method, such as someone who only signs in with a
provider, is fresh right after any login, so a first enrollment needs nothing
special. Otherwise the handler answers `403` with the methods the user can confirm
with, and the client posts to `POST /confirm`, for example
`{"method": "pwd", "password": "..."}`, then retries. A provider login is not a
step-up method: it never makes a user who has a password or an authenticator fresh
again.

| Route | Browser session | Fresh login | Verified email |
| --- | --- | --- | --- |
| `PUT /password` | Yes | Yes | No |
| `POST /two-factor/setup`, `POST /two-factor` | Yes | Yes | Yes |
| `DELETE /two-factor`, `POST /two-factor/recovery-codes` | Yes | Yes, and a recent second factor | No |
| `POST /tokens` | Yes | Yes | Yes |
| `POST /device-tokens` | Yes | A login at most five minutes old | Yes |
| `POST /connections/{provider}` | Yes | Yes | Yes |
| `DELETE /connections/{id}`, `DELETE /account` | Yes | Yes | No |
| `GET /tokens`, `DELETE /tokens/{id}`, `GET /connections` | Yes | No | No |
| `POST /email/verify`, `POST /email/verify/resend` | Yes | No | No |

The checks live in the handlers, so they hold on custom paths too. With
[your own users](own-users.md), the verified-email column applies only when you
provide `Users.Verified`.

What a change ends:

- A password change ends the user's other logins and revokes all their tokens,
  personal and device ones. Someone who stole the password can log in and create a
  personal token, and that token must not outlive the change. If your users rely
  on long-lived personal tokens for scripts and you accept the risk, call
  `iam.KeepPersonalTokens()` to revoke only device tokens. It has no effect
  without tokens.
- Enabling two-factor ends the user's other logins and revokes their tokens the
  same way, for the same reason, and `iam.KeepPersonalTokens()` limits it to
  device tokens too.
- A password reset rotates the user, which ends every session, pending login and
  token.

## Unverified Accounts

Someone can register an address they do not own before its owner does, then attach
a provider or an authenticator and wait. The facade closes this in two ways. Until
the email is verified, the account cannot connect a provider, enroll two-factor or
create tokens. And a completed password reset, which proves the mailbox, marks the
email verified and deletes every connection, the authenticator, the recovery codes
and all tokens, in the reset's transaction.

If other modules registered credentials through `iam.SecondFactor` or `iam.StepUp`
(a passkey package, say), the facade cannot know what to delete. Give it a hook:

```go
iam.OnEmailProven(passkeys.RemoveAll)
```

The hook receives the subject and runs inside the reset, after the built-in purge.
An error rolls the reset back. It does not run when the email was already verified.
With your own users, the purge needs `Users.Verified` and `Users.ProveEmail`; see
[Your Own Users](own-users.md#verified-emails).

For every function and option, see
[the `access` API reference](https://pkg.go.dev/arkoren.dev/ark/access).
