---
title: Passwords
description: Sign in with a password, handle a pending second factor, and confirm or change credentials through the Access routes.
order: 20
summary:
  - Log in an existing user and distinguish a completed login from a pending second-factor challenge.
  - Confirm an older session before changing a password, and understand which credentials the change revokes.
  - Choose a password policy and account for login throttling without opening registration.
---

# Passwords

Once [Access is mounted](overview.md#mount-routes) with `iam.Password(hasher)`,
an existing user can sign in with their email and password. This page assumes
that the user already has a password, either provisioned by your application or
created through [registration](registration.md).

Send JSON with `Content-Type: application/json`. A browser keeps the session
cookie automatically on same-origin requests; a non-browser client needs a
cookie jar across the login steps.

## Log In

To sign Ada in, send `POST /login`:

```json
{
  "email": "ada@example.com",
  "password": "correct horse battery"
}
```

`204` with no body means the login is complete. If Ada has an enrolled second
factor, the response is `202` instead:

```json
{
  "second_factor": {
    "methods": ["otp", "recovery"]
  }
}
```

This is a **pending login**, not an authenticated session. Complete it through
`POST /login/challenge` before requesting private resources.
[Two-Factor](two-factor.md#complete-a-login) explains the code bodies and
recovery path. `GET /login/pending` returns the available methods again.

An unknown email, wrong password, disabled user, or user without a password
receives the same `422` `password:invalid-credentials` problem. Unknown users
still incur a dummy password hash; the login does not return a distinct
“account not found” answer. Applications with
[their own identifiers](own-users.md) can send `identifier` instead of `email`.

## Confirm Identity

A valid session can be older than the proof required to change a credential.
A **fresh login** normally means that the user proved their password or
authenticator within the last 15 minutes.

If a protected action returns `403` `step-up-required`, keep the session and
ask for one of the methods in the problem's `methods` field. For a password,
send `POST /confirm`:

```json
{
  "method": "pwd",
  "password": "correct horse battery"
}
```

Success is `204`; retry the original action. A wrong confirmation is core's
`403`, not the password-login route's `422`. Confirmation attempts are limited
by the guard.

The [Fresh Logins](freshness.md) page explains users without a password,
provider sign-in, and operations that require a genuinely new login instead.

## Change Passwords

To replace a password, send `PUT /password` from an authenticated session
with a fresh login:

```json
{
  "current_password": "correct horse battery",
  "password": "another long passphrase"
}
```

Success is `204`. The caller stays logged in with a fresh password proof;
the user's other logins end. By default Access also revokes every personal
and device token. A password change invalidates previously sent reset links,
but does not rotate the user's credential version.

The current password is required if the user has one, and wrong attempts have
a separate per-user limit. A provider-only user can set a first password without
`current_password`, but must still pass the session and freshness checks.
A bearer token cannot change a password.

If a later revocation or hook fails, the request can return `500` after the
password was stored. Do not assume rollback: the new password is already in
effect. A retry must use it as `current_password`; repeating the change runs
the remaining cleanup again.

If scripts must retain personal tokens after a password change, the
[credential-change policy](accounts.md#credential-changes) explains
`KeepPersonalTokens` and its security trade-off.

## Password Policy

The default policy accepts passwords of 8–72 **bytes**, not characters. The
upper bound accommodates bcrypt's input limit. The policy applies when a
password is registered, changed, reset, or set through the verifier.

To require longer passphrases, supply a policy in the constructor. Import
`password` from `arkoren.dev/ark/access/password`; this example also uses
`context` from the standard library:

```go
checkPassword := func(ctx context.Context, value string) error {
	if len(value) < 12 || len(value) > password.MaxLength {
		return password.ErrInvalidPassword
	}

	return nil
}
```

This replaces the default policy, so preserve any upper bound your hasher needs.
An error wrapping `password.ErrInvalidPassword` rejects the value with `422`;
another error represents an infrastructure failure.

Attach the policy while enabling passwords:

```go
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Password(hasher, func(passwords *password.Builder) {
		passwords.Policy(checkPassword)
	})
})
```

This does not enable registration or forgotten-password recovery.
Configure those deliberately on the following pages.

## Login Limits

The login route reserves throttle budget before hashing. Within 15 minutes,
the failure limits are:

| Scope | Failures allowed |
| --- | --- |
| Identifier and client address together | 10 |
| Identifier across all client addresses | 50 |
| Client address across all identifiers | 100 |

The next attempt returns `429` with `Retry-After` in seconds. A successful
password check normally refunds the attempt and clears the combined
identifier/address counter. During a second-factor lockout, even the correct
password stays counted: starting another pending login must not bypass the
lockout. If that lockout cannot be read, login fails closed with `500`.

The combined counter limits local guessing; the wider identifier limit also
bounds distributed guessing and can temporarily block the real user. They wait
for the window or use recovery. The
[client-address configuration](overview.md#start-small) determines how these
limits identify a client; IPv6 addresses share their `/64` counter.

## Log Out

Send `POST /logout` with the session cookie and the JSON content-type header.
No body is needed. Success is `204` and ends the current login, not the user's
other sessions or API tokens. Use the
[session routes](accounts.md#manage-sessions) to end other logins.

Next, configure [Email Links](email-links.md) so users can recover forgotten
passwords and prove their email before enrolling additional credentials.

For the password operations and settings, see the
[`password` API reference](https://pkg.go.dev/arkoren.dev/ark/access/password).
