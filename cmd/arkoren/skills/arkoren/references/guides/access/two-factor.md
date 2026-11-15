---
title: Two-Factor
description: Enroll an authenticator app, finish logins with a code or a recovery code, and manage both, with the access facade.
order: 40
summary:
  - Enable two-factor with a cipher, set up an authenticator and receive recovery codes once.
  - Finish a parked login through the challenge route with a code or a recovery code.
  - Replace recovery codes or turn two-factor off, and know how codes resist replay, guessing and key rotation.
---

# Two-Factor

A password alone can be guessed, phished or leaked. Two-factor adds a time-based
one-time password (TOTP) from an authenticator app, plus single-use recovery codes
for a lost phone. Enable it with a [cipher](../encryption/overview.md) that
encrypts the secrets and the name authenticator apps show:

```go
iam.TwoFactor(cipher, "Example")
```

Use a key reserved for these secrets. Each user has one authenticator. The guard
lists the enrolled methods in registration order: `otp`, then `recovery`. The core
[two-factor page](../authentication/two-factor.md) explains the guard's side of a
second factor.

## Enroll an Authenticator

Enrollment has two steps, and both need a browser session, a fresh login and a
verified email (`403` `email-unverified` otherwise). A user without a step-up
method is fresh right after logging in, so the first enrollment needs no extra
step.

`POST /two-factor/setup` stores a pending secret and answers `200`:

```json
{ "secret": "JBSWY3DPEHPK3PXP", "uri": "otpauth://totp/Example:ada%40example.com?..." }
```

Render `uri` as a QR code for the user, and show `secret` for manual entry. Both
contain the secret, so show them only to the enrolling user. Calling setup again
before the next step replaces the pending secret. Once the authenticator is on,
setup is `409`.

`POST /two-factor` takes the first code, `{"code": "123456"}`, turns two-factor on
and answers `200` with the recovery codes:

```json
{ "recovery_codes": ["ABCD-EFGH-IJKL-MNOP", "..."] }
```

This is the only time they are shown. A wrong code is `422`, ten wrong codes in 15
minutes are `429`, and enabling without setup is `409`. The session records the
code, so the user stays fresh for the next change.

Enabling also ends every other login of the user and revokes all their tokens
(only device tokens with `iam.KeepPersonalTokens()`), so a session an attacker
opened before the second factor existed does not outlive it. All of that happens in the same transaction as the activation: if it
fails, the answer is `500`, two-factor stays off and no recovery code was shown.
Enabling publishes `twofactor.Enabled` and core's `auth.CredentialEnrolled`.

## Log In With a Code

After the password, a user with an authenticator gets `202` with
`{"second_factor": {"methods": ["otp", "recovery"]}}`. The client posts the code to
the challenge route:

```json
{ "method": "otp", "code": "123456" }
```

`{"method": "recovery", "code": "ABCD-EFGH-IJKL-MNOP"}` uses a recovery code
instead, and case, dashes and spaces do not matter. A wrong code is core's `403`,
counted per user across methods. The same bodies re-prove a logged-in user at
`POST /confirm`.

A code works once. Each accepted time step must be newer than the last, enforced
by one atomic update, so of two concurrent identical codes exactly one wins.
Recovery codes carry 80 random bits, are stored only as hashes and are single
use. One answer covers a wrong code, a replay, a spent code and a user who is not
enrolled, so a response never says which. If completing a login fails after a code
was accepted, the code is spent and the user waits for the next one.

## Manage Two-Factor

`POST /two-factor/recovery-codes` replaces the codes and answers with the new
list, and the old ones stop working. `DELETE /two-factor` removes the
authenticator and the codes and answers `204`. Both answer `409` when two-factor
is not enabled.

Weakening two-factor needs more than a fresh login. Both routes also need an
authenticator or recovery code this session verified recently, within 15 minutes
by default. A recent password alone is `403` with the enrolled second-factor
methods in the problem (`"methods": ["otp"]`). The client posts
`{"method": "otp", ...}` to `/confirm` and retries, so someone holding a password
and an old session cannot turn two-factor off. A recovery code spent at login
counts as a recent proof.

To change the window, configure the package, and set the same value as the guard's
`FreshFor` if you changed that too:

```go
iam.TwoFactor(cipher, "Example", func(factors *twofactor.Builder) {
	factors.StepUpWindow(5 * time.Minute)
})
```

For an account page, read the state with `iam.Factors().Status(ctx, subject)`.
The `twofactor` events `Disabled`, `Regenerated` and `RecoveryUsed` suit
notifications such as "two-factor was turned off".

## Key Rotation

Secrets are decrypted with the current cipher and, when you rotate keys, with the
previous keys you configured on it. A secret sealed with an old key is sealed
again with the current one on its next use.

For every function and option, see
[the `twofactor` API reference](https://pkg.go.dev/arkoren.dev/ark/access/twofactor).
