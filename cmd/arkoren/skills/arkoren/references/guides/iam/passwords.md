---
title: Passwords & Registration
nav: Passwords
description: Sign users in with passwords, handle pending second-factor challenges, change passwords safely, configure password rules, and open self-service registration.
order: 20
summary:
  - Sign users in and out, and distinguish a finished login from a pending second-factor challenge.
  - Change passwords safely while understanding what sessions and tokens are revoked.
  - Customize the password policy and account for built-in brute-force throttling.
  - Open self-service registration deliberately and handle taken addresses without leaking accounts.
---

# Passwords & Registration

Once you enable `identity.Password(hasher)` and [mount IAM's routes](overview.md#mounting-routes-and-protecting-endpoints), users with a password can sign in, sign out, and update their password. Self-service registration stays closed until you turn it on explicitly.

Every route on this page expects `Content-Type: application/json`. Browsers carry the session cookie automatically on same-origin requests; non-browser clients must keep a cookie jar across login steps.

## Signing In

Send the user's email and password to `POST /login` (prefixed by your mount path, such as `/auth/login`):

```json
{
  "email": "ada@example.com",
  "password": "correct horse battery"
}
```

There are two success responses:

- **`204 No Content`**: Login is complete and the session cookie now authenticates the user.
- **`202 Accepted`**: The password was right, but the user has a second factor enrolled. The body lists the methods that can finish the login:

```json
{
  "second_factor": {
    "methods": ["otp", "recovery"]
  }
}
```

A `202` response is a **pending login**, not an authenticated session. Protected routes still return `401` until the client completes [`POST /login/challenge`](two-factor.md#completing-a-login). If the page reloads, `GET /login/pending` returns the available methods again.

An unknown email, wrong password, disabled account, or user without a password all return the same `422` `password:invalid-credentials` problem. Unknown emails still run a dummy password hash so timing does not reveal whether an address exists. Applications using [custom user stores](own-users.md) may send `identifier` instead of `email`.

## Signing Out

Send `POST /logout` with the session cookie and `Content-Type: application/json` (no body is required). IAM responds with `204` and ends only the current login; the user's other browser sessions and API tokens stay active. Use the [session routes](accounts.md#managing-active-sessions) to sign out other devices.

## Changing a Password

To update a password, an authenticated browser session sends `PUT /password`:

```json
{
  "current_password": "correct horse battery",
  "password": "another long passphrase"
}
```

This route requires a **fresh login**: a recent proof of identity, not merely an old session cookie. If the session is stale, IAM returns `403` `step-up-required` so your UI can re-verify the user through [`POST /confirm`](freshness.md#renewing-a-proof) and retry without logging them out. Bearer tokens cannot change passwords (`401`).

On `204`, the password is updated:

- The caller's session stays signed in with a fresh password proof.
- The user's other sessions are ended.
- Every personal and device token is revoked by default (unless you opt into [`KeepPersonalTokens`](accounts.md#credential-change-revocation)).
- Any previously sent password-reset links stop working immediately, because links bind to the old hash.

`current_password` is required whenever the user already has a password, and wrong attempts have their own per-user throttle. A user who signed up through an external provider and has no password yet can set a first password by omitting `current_password`, while still passing the session and freshness checks.

> [!NOTE]
> If a post-change revocation or hook fails, `PUT /password` may return `500` *after* the new password has already been saved. Do not assume the password rolled back: retry with the new password as `current_password` so the remaining cleanup runs again.

## Password Policy

By default, IAM accepts passwords between 8 and 72 **bytes** (not characters). The 72-byte ceiling matches bcrypt's input limit. This policy runs everywhere a password is set: registration, change, reset, and `identity.Verifier().Set`.

To enforce a stricter rule, supply a custom policy function using `password` from `arkoren.dev/ark/iam/password`:

```go
checkPassword := func(ctx context.Context, value string) error {
	if len(value) < 12 || len(value) > password.MaxLength {
		return password.ErrInvalidPassword
	}

	return nil
}
```

Attach it inside your `Password` callback:

```go
identity.Password(hasher, func(passwords *password.Builder) {
	passwords.Policy(checkPassword)
})
```

A custom policy replaces the default completely, so keep any upper bound your hasher requires. Returning an error that wraps `password.ErrInvalidPassword` rejects the input with `422`; any other error is treated as an infrastructure failure (`500`).

## Login Throttling

`POST /login` reserves throttle budget before hashing. Within a 15-minute window, IAM allows:

| Scope | Failures allowed |
| --- | --- |
| Identifier and client address together | 10 |
| Identifier across all client addresses | 50 |
| Client address across all identifiers | 100 |

Exceeding any limit returns `429` with a `Retry-After` header in seconds. IPv6 addresses share a `/64` bucket, and your [`ClientIP`](overview.md#behind-a-reverse-proxy) resolver controls which address is counted.

A successful password check refunds the attempt and clears the combined identifier/address counter, except during a second-factor lockout. While a user is locked out of their second factor, even the right password stays counted so restarting login cannot bypass the lockout. If the lockout state cannot be read from the cache, login fails closed with `500`.

## Self-Service Registration

Leave registration closed if invitations, administrators, or external providers control who can join. When your application allows public sign-up, enable registration explicitly after configuring [password recovery and email verification](email-links.md).

With the built-in directory, `Registration(true)` requires `Reset(...)` so an address owner can always recover an account someone else registered first. If `Reset` is missing, `iam.New` logs a warning, ignores `Registration(true)`, and does not mount `POST /register`.

```go
identity.Queue(dispatcher)
identity.Password(hasher, func(passwords *password.Builder) {
	passwords.Reset(signer, mailer, "https://app.example.com/reset-password")
	passwords.Registration(true)
})
identity.Directory(func(accounts *account.Builder) {
	accounts.Verification(signer, mailer, "https://app.example.com/verify-email")
})
```

Turning registration off later removes `POST /register` without affecting existing accounts or their ability to sign in. Provider sign-up is configured separately on [External Sign-In](federated-sign-in.md#first-time-sign-in).

### Submitting a Registration

Send `POST /register` with an email and password:

```json
{
  "email": "ada@example.com",
  "password": "correct horse battery"
}
```

For a new address, IAM creates an **unverified** user and stores the password in one database transaction. It responds with an empty **`202 Accepted`**, not a session; the client still signs in via `POST /login`. Until the email is verified, the user can log in, but cannot enroll two-factor authentication, connect external providers, or create API tokens.

For an address that already exists, `POST /register` also returns `202`. It never overwrites the existing password or merges accounts. Malformed emails (trimmed and lowercased, max 254 bytes) or passwords failing your policy return `422`.

### Registration Emails

What IAM queues on `POST /register` depends on which email flows you enabled:

| Configuration | New address | Taken address |
| --- | --- | --- |
| Reset and verification | Queues a verification email | Queues a reset notice for the owner |
| Reset only | Queues nothing | Queues nothing |

When verification is enabled, both branches enqueue a background job so the request does comparable work either way. On the taken-address branch, `password.ResetMessage.Notice` is `true` so your email template can explain that someone tried to register with that address and offer a recovery link. If queueing fails, IAM logs the error and still answers `202` so the response cannot reveal whether the address exists.

With [your own users](own-users.md), `UsersBuilder.Create` is required and IAM does not queue registration emails automatically; supply your own `Registered` and `Taken` hooks with comparable work on both paths.

### Honest Sign-Up UX and Enumeration

Do not tell the user that `202` guarantees an account was created, and do not assume it completely hides account existence from an active attacker: anyone can register an address with a chosen password and immediately attempt `POST /login`.

IAM bounds that probing with built-in throttles: `POST /register` allows 5 attempts per typed identifier and 30 per client address in 15 minutes before returning `429`. In your UI, confirm that the request was accepted, invite the user to sign in, and note that an email may arrive. If sign-in fails, offer password recovery. For public sign-up forms, layer application [rate limits](../abuse-prevention/rate-limiting.md) and [CAPTCHA](../abuse-prevention/captcha.md) on top of the built-in counters.

Next, set up [Verification & Recovery](email-links.md) so users can reset forgotten passwords and prove email ownership.

For all password options and hooks, see the [`password` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/password).
