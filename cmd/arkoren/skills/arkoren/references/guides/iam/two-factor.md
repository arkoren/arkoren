---
title: Two-Factor Authentication
nav: Two-Factor
description: Add TOTP authenticator apps and single-use recovery codes to your login flow, handle challenges, and rotate encryption keys safely.
order: 50
summary:
  - Enable TOTP with a dedicated encryption key and enroll authenticators in a two-step flow.
  - Complete pending login challenges with an authenticator code or a single-use recovery code.
  - Regenerate recovery codes or disable two-factor safely, and rotate encryption keys without breaking enrolled users.
---

# Two-Factor Authentication

Passwords can be leaked or reused. Adding a time-based one-time password (**TOTP**) authenticator requires possession of a second device to finish signing in, while single-use recovery codes give users a way back in if they lose that device.

Enrollment requires an authenticated browser session, a verified email address, and a [fresh proof](freshness.md). Bearer tokens cannot enroll or modify two-factor settings.

## Enabling Two-Factor

Create a TOTP scheme with `totp` from `arkoren.dev/foundation/totp` using the service name you want authenticator apps to display (nonempty, with no colons):

```go
scheme := totp.New("Acme")
```

By default, `totp.New` uses SHA-1, six digits, a 30-second period, and one step of clock-drift tolerance, matching standard authenticator apps.

Enable two-factor in your `iam.New` callback using a `*crypto.Encrypter` (`cipher`) with a key reserved for authenticator secrets:

```go
identity.TwoFactor(scheme, cipher)
```

An invalid scheme or nil cipher makes `iam.New` fail with `iam.ErrInvalidConfig`. Each user can enroll one authenticator, and IAM encrypts every TOTP secret before storing it.

## Enrolling an Authenticator

Enrollment is a two-step handshake so a user never locks themselves out before their app is configured.

First, start enrollment by sending `POST /two-factor/setup` with `Content-Type: application/json` and an empty body. IAM responds with `200`:

```json
{
  "secret": "JBSWY3DPEHPK3PXP",
  "uri": "otpauth://totp/Acme:ada%40example.com?..."
}
```

Render `uri` as a QR code and show `secret` as a manual-entry fallback. Calling setup again replaces the pending secret; once two-factor is active, setup returns `409`.

> [!WARNING]
> Both `secret` and `uri` expose the raw authenticator key. Show them only to the signed-in user and never log them.

Next, ask the user for the first 6-digit code from their authenticator app and confirm enrollment with `POST /two-factor`:

```json
{
  "code": "123456"
}
```

IAM responds with `200` and a set of single-use recovery codes:

```json
{
  "recovery_codes": ["ABCD-EFGH-IJKL-MNOP", "..."]
}
```

Prompt the user to store these codes immediately, because IAM hashes them with SHA-256 and never displays them again. A wrong activation code returns `422` (throttled to `429` after 10 failures in 15 minutes), and activating without calling setup first returns `409`.

Activating two-factor ends the user's other browser sessions and revokes all API and device tokens by default (unless you opt into [`KeepPersonalTokens`](accounts.md#credential-change-revocation)). If activation or revocation fails, the database transaction rolls back and no codes are returned. On success, IAM records the new authenticator proof on the current session; if refreshing that proof fails, the recovery codes are still returned so they are not lost, and the user may be asked to confirm again before their next sensitive action.

## Completing a Login

When an enrolled user signs in with a password (or through an external provider), login pauses in a **pending login** state instead of creating a full session. For passwords, `POST /login` returns `202` with the offered methods, normally `["otp", "recovery"]` in that order.

Keep the same cookie jar and submit an authenticator code to `POST /login/challenge`:

```json
{
  "method": "otp",
  "code": "123456"
}
```

On `204`, the session is now fully authenticated. If the user lost their phone, submit a saved recovery code to the same endpoint instead:

```json
{
  "method": "recovery",
  "code": "ABCD-EFGH-IJKL-MNOP"
}
```

Recovery codes contain 80 random bits, ignore case, spaces, and dashes, and work once. They are accepted at `/login/challenge`, never at `/confirm`.

Key challenge rules to keep in mind:

- **Pending-login expiry**: Pending logins expire after 5 minutes by default (`401` afterward).
- **Shared attempt budget**: The guard limits failed attempts per user across all challenge methods, so restarting password login does not reset the budget. Exhausting the budget discards the pending login (`401`).
- **Replay protection**: Every accepted TOTP code must belong to a newer time step than the last accepted code, enforced atomically so concurrent copies cannot both succeed. If session completion fails after a code is accepted, that step is already spent; wait for the next code or use a recovery code.
- **Error codes**: Wrong, replayed, spent, or unenrolled codes return core's `403`.

## Disabling or Regenerating Codes

A stolen password alone must not be enough to remove two-factor protection. Both regenerating recovery codes and disabling two-factor require a fresh login **and** a recent second-factor proof (within 15 minutes by default). If the session lacks that proof, IAM returns `403` with `methods: ["otp"]` so the user can confirm at `POST /confirm` and retry.

If the user lost their authenticator app, they can sign in with a recovery code instead. For these two endpoints only, a recent recovery-code login satisfies both the freshness check and the second-factor requirement:

| Request | Result |
| --- | --- |
| `POST /two-factor/recovery-codes` | `200` with a new set of recovery codes; all old codes stop working |
| `DELETE /two-factor` | `204`; removes the authenticator, pending setup, and recovery codes |

Both endpoints return `409` if two-factor is not enabled. Remember to include `Content-Type: application/json` on `POST /two-factor/recovery-codes` even without a body.

If you shorten the guard's freshness window, consider tightening the separate second-factor management window too using `twofactor` from `arkoren.dev/ark/iam/twofactor`:

```go
identity.TwoFactor(scheme, cipher, func(factors *twofactor.Builder) {
	factors.StepUpWindow(5 * time.Minute)
})
```

To check a user's enrollment status from Go code (for example, to render an account settings page), call `identity.Factors().Status(ctx, subject)`.

## Rotating Encryption Keys

When you rotate the encryption key used by `cipher`, keep the previous decryption keys in your [encrypter configuration](../encryption/keys.md). Whenever an authenticator secret is decrypted with an older key, IAM automatically re-encrypts it with the current key.

> [!WARNING]
> Removing a previous decryption key before existing secrets have been re-encrypted renders those authenticators unreadable.

Two-factor changes emit `twofactor.Enabled`, `twofactor.Disabled`, `twofactor.Regenerated`, `twofactor.RecoveryUsed`, and core's `auth.CredentialEnrolled` when you wire [security events](accounts.md#security-events).

Next, explore [External Sign-In](federated-sign-in.md) to let users sign in with Google, GitHub, Apple, or any OIDC provider.

For all two-factor options and types, see the [`twofactor` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/twofactor).
