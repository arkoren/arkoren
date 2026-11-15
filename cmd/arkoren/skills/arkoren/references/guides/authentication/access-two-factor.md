---
title: Access Two-Factor
description: Enroll an authenticator app, finish logins with a code or a recovery code, and manage both, with the access facade.
order: 26
summary:
  - Enable two-factor with a cipher, set up an authenticator and receive recovery codes once.
  - Finish a parked login through the challenge route with a code or a recovery code.
  - Know how codes are protected against replay, guessing and key rotation.
---

# Access Two-Factor

Enable time-based one-time passwords (TOTP) and recovery codes with a
[cipher](../encryption/overview.md) that encrypts the secrets and the name
authenticator apps show:

```go
iam.TwoFactor(cipher, "Example")
```

Use a key reserved for these secrets. Each user has one authenticator. The
guard lists the enrolled methods in registration order: `otp`, then `recovery`.

## Enroll an Authenticator

Enrollment has two steps, both needing a browser session and a fresh login. A
user without a step-up method is fresh right after logging in, so the first
enrollment needs no extra step.

`POST /two-factor/setup` stores a pending secret and answers `200`:

```json
{ "secret": "JBSWY3DPEHPK3PXP", "uri": "otpauth://totp/Example:ada%40example.com?..." }
```

Render `uri` as a QR code for the user, and show `secret` for manual entry; both
contain the secret, so show them only to the enrolling user. Calling setup again
before the next step replaces the pending secret; once the authenticator is on,
setup is `409`.

`POST /two-factor` takes the first code, `{"code": "123456"}`, turns
two-factor on and answers `200` with the recovery codes:

```json
{ "recovery_codes": ["ABCD-EFGH-IJKL-MNOP", "..."] }
```

This is the only time they are shown. A wrong code is `422`, ten wrong codes in
15 minutes are `429`, and enabling without setup is `409`. The session records
the code, so the user stays fresh for the next change. Enabling also ends every
other login of the user, so a session an attacker opened before the second
factor existed does not outlive it, and an unverified email is `403`
(email-unverified). Enabling publishes the
`twofactor` enabled event and core's `auth.CredentialEnrolled`.

## Log In With a Code

After the password, a user with an authenticator gets `202` with
`{"second_factor": {"methods": ["otp", "recovery"]}}`. The client posts the code
to the core challenge route:

```json
{ "method": "otp", "code": "123456" }
```

`{"method": "recovery", "code": "ABCD-EFGH-IJKL-MNOP"}` uses a recovery code
instead; case, dashes and spaces do not matter. A wrong code is core's `403`,
counted per user across methods. The same bodies re-prove a logged-in user at
`POST /confirm`.

A code works once: each accepted time step must be newer than the last, enforced
by one atomic update, so of two concurrent identical codes exactly one wins.
Recovery codes carry 80 random bits, are stored only as hashes and are single
use. One `422` or `403` covers a wrong code, a replay, a spent code and a user
who is not enrolled, so a response never says which.

## Manage It

`POST /two-factor/recovery-codes` replaces the codes and answers with the new
list; the old ones stop working. `DELETE /two-factor` removes the authenticator
and the codes and answers `204`. Both answer `409` when two-factor is not
enabled. Both need a fresh login and, on top of that, an authenticator or
recovery code this session verified recently (15 minutes by default,
`twofactor.Builder.StepUpWindow`; set the same value as the guard's `FreshFor`
if you changed it). A recent password alone, which makes core's check pass, is
`403` with `"methods": ["otp"]`: the client posts `{"method": "otp", ...}` to
`/confirm` and retries. Someone holding a password and an old session therefore
cannot turn two-factor off. Read the state for your account page with `iam.Factors().Status(ctx,
subject)`.

Secrets are decrypted with the current cipher and, when you rotate keys, with
the previous keys you configured on it; a secret sealed with an old key is
sealed again with the current one on its next use. If completing a login fails
after a code was accepted, the code is spent and the user waits for the next one.

For every function and option, see
[the `twofactor` API reference](https://pkg.go.dev/arkoren.dev/ark/access/twofactor).
