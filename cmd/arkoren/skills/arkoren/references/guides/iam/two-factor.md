---
title: Two-Factor
description: Enroll an authenticator, finish a pending login, and recover from a lost device without weakening the account through an old session.
order: 80
summary:
  - Enable TOTP with an encryption key and activate enrollment with the first code.
  - Complete login challenges with an authenticator or a single-use recovery code.
  - Replace recovery codes or disable two-factor using recent second-factor proof.
---

# Two-Factor

An authenticator adds a possession proof when a password is stolen.
IAM supports time-based one-time passwords (**TOTP**) and recovery codes
for a lost device. A user enrolls once, then completes a second-factor
challenge during later logins.

This page assumes [mounted IAM routes](overview.md#mount-routes).
Enrollment requires a verified email, a session, and a
[fresh login](freshness.md); a bearer token cannot enroll credentials.

## Enable the Feature

Use an existing `*crypto.Encrypter` named `cipher` with a key reserved for
authenticator secrets. Import `totp` from `arkoren.dev/foundation/totp`:

```go
scheme := totp.New("Acme")
```

`Acme` is the service name shown in authenticator apps. It must be nonempty
and contain no colon. The defaults are SHA-1, six digits, a 30-second period,
and one time step of accepted drift; keep them unless your clients require
different parameters.

Enable the feature alongside your existing login method:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher)
	identity.TwoFactor(scheme, cipher)
})
```

An invalid scheme or nil cipher returns `iam.ErrInvalidConfig`.
Apply the newly enabled feature's migrations before serving its routes.
Each user can enroll one authenticator.

## Enroll an Authenticator

From Ada's session, send `POST /two-factor/setup` with the JSON content-type
header and no body. It returns `200` with a pending enrollment:

```json
{
  "secret": "JBSWY3DPEHPK3PXP",
  "uri": "otpauth://totp/Acme:ada%40example.com?..."
}
```

Render `uri` as a QR code and offer `secret` for manual entry. Both expose
the authenticator secret; show them only to Ada and keep them out of logs.
Setup alone does not enable two-factor. Calling setup again replaces the
pending secret; after activation it returns `409`.

Ask Ada for the first code from her app, then send `POST /two-factor`:

```json
{
  "code": "123456"
}
```

Success is `200` with recovery codes:

```json
{
  "recovery_codes": ["ABCD-EFGH-IJKL-MNOP", "..."]
}
```

Offer a way to save them now: the full codes are returned only at creation.
A wrong enrollment code is `422`, the next attempt after ten failures in
15 minutes is `429`, and activation without setup is `409`.

Activation ends the user's other logins and, by default, revokes every
personal and device token. If activation or its required revocation steps fail,
the database activation rolls back and no codes are returned. On success,
the current session records the authenticator proof; if refreshing that proof
fails, the codes are still returned so they are not lost.
The user may need to confirm again before the next sensitive action.

## Complete a Login

On a later password login, an enrolled user receives `202` with
`second_factor.methods`, normally `["otp", "recovery"]` in that order.
Keep the same cookie jar and submit one offered method to
`POST /login/challenge`:

```json
{
  "method": "otp",
  "code": "123456"
}
```

Success is `204`; the session now authenticates the user. To recover a lost
authenticator, use one saved code at the same route:

```json
{
  "method": "recovery",
  "code": "ABCD-EFGH-IJKL-MNOP"
}
```

Recovery codes ignore case, dashes, and spaces. They contain 80 random bits,
are stored only as hashes, and can be spent once. A recovery code is not
accepted at `/confirm`; an authenticator code is.

Pending logins expire after five minutes by default. The guard limits
attempts per user across challenge methods, so restarting password login
does not reset that budget. Wrong, replayed, spent, or unenrolled credentials
receive core's `403`. An absent or expired pending login receives `401`;
exhausting the challenge budget also discards the pending login, and later
challenge attempts receive `401`.

An accepted TOTP time step must be newer than the last accepted step.
Concurrent copies of the same code cannot both succeed. If session completion
fails after the code was accepted, it is already spent: wait for the next
authenticator code or use another recovery code.

## Recover or Disable

To weaken two-factor, an old session and the password alone are insufficient.
Both removal and recovery-code replacement need a fresh login **and** a recent
second-factor proof, normally within 15 minutes. If required, confirm an
authenticator code at `POST /confirm`, then retry.

A person who lost the authenticator can instead log in with a recovery code.
For these two operations only, that recent recovery proof also satisfies the
freshness check. Complete the change within the window:

| Request | Result |
| --- | --- |
| `POST /two-factor/recovery-codes` | `200` with new codes; every old code stops working |
| `DELETE /two-factor` | `204`; authenticator, pending enrollment, and recovery codes are removed |

Send the JSON content-type header for the `POST`, even without a body.
Both operations return `409` when two-factor is not enabled.
An insufficient second-factor proof returns `403` with methods the user can
confirm, normally `["otp"]`.

If you shorten the guard's freshness window, consider the separate
second-factor management window too. Import `twofactor` from
`arkoren.dev/ark/iam/twofactor` and configure it during construction:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.TwoFactor(scheme, cipher, func(factors *twofactor.Builder) {
		factors.StepUpWindow(5 * time.Minute)
	})
})
```

This fragment shows that setting only; retain your login features in the
application's actual constructor. Read account-page state through
`identity.Factors().Status(ctx, subject)`.

## Keep Secrets Usable

When rotating encryption keys, retain previous decryption keys in the
[encrypter configuration](../encryption/keys.md). IAM decrypts old secrets
with those keys and re-encrypts them with the current key on their next use.
Removing an old key before its secrets have moved makes those authenticators
unusable.

Activation emits `twofactor.Enabled` and core's `auth.CredentialEnrolled`.
`Disabled`, `Regenerated`, and `RecoveryUsed` support
[security notifications](accounts.md#security-notifications).

Next, use [API Tokens](tokens.md) for scripts or first-party mobile clients
without giving them account-management powers.

For enrollment, recovery, and configuration, see the
[`twofactor` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/twofactor).
