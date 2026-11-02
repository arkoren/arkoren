---
title: Hashing Overview
nav: Overview
description: Store password hashes and distinguish a successful password check from a mismatch or a storage failure.
order: 10
summary:
  - Separate accepted-password rules from the work policy used to store passwords.
  - Store complete salted records and distinguish successful checks, mismatches, and failures.
  - Keep input encoding consistent and account for login timing and resource limits.
---

# Hashing Overview

Password hashing lets you check a password without storing a readable copy.
Arkoren's `hash.Hasher` produces an encoded record containing the algorithm,
work settings, random salt, and digest. Store that entire record; there is no
decryption operation. A leaked record can still be attacked by guessing passwords.

Unlike fast general-purpose hashes such as SHA-256, password-hashing algorithms
deliberately spend time, and Argon2 also spends memory, on each guess.

The examples assume a configured `*hash.Hasher` named `hasher`; choose and wire
one on [Drivers](drivers.md). Argon2id is the recommended choice for new passwords;
bcrypt serves existing bcrypt databases and compatibility requirements.
Focused fragments leave routine errors visible rather than showing application
error propagation. Handle those errors before using their results.

## Password Policy

Your password policy decides which new passwords the application accepts. The
hash policy decides how much work storing and checking them costs. Hashing does
not enforce minimum length, reject common passwords, or authorize a password
change: even an empty value is valid input to the hashing contract.

Validate new passwords before hashing, and apply request-size and password-byte
limits before any expensive check. bcrypt requires a limit of **72 bytes** on
both creation and checking. Its native verifier can accept a longer attempt
sharing a valid 72-byte prefix; never truncate the attempt. Argon2 has no such
bcrypt-specific limit, but still needs [bounded input and work](cost.md#bound-request-work).

Do not apply a newly tightened minimum-length rule to existing login attempts;
an older valid password might no longer satisfy your registration policy.
Keep applicable resource limits and scheme-specific input limits on that path.

## Store Passwords

At registration, hash only the validated password field, not the whole request.
For example, your decoded registration input can have this shape:

```go
type RegistrationInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
```

With `input` already validated, create the stored record:

```go
stored, err := hasher.Hash(input.Password)
```

On success, persist `stored` unchanged in the account's password-hash column.
Each call generates a fresh salt, so hashing the same password twice produces
different records that both verify. You do not generate or store a separate salt.

Use a column large enough for the chosen format and future policy changes.
A text column avoids a fixed format-length assumption; if you choose a bounded
column, test that complete records fit after changing salt or digest lengths.
Never store or log the plaintext password alongside the record.

## Check Passwords

At login, load the hash from application-owned account storage. Do not let a
request supply the stored hash or its work settings: verification uses the
record's settings, not the current new-hash policy.

Assume a decoded `LoginInput` with a `Password string` field, an account named
`account`, and `auth` imported from `arkoren.dev/core/http/auth`. After enforcing
the input limits above, check the attempt:

```go
matched, err := hasher.Check(input.Password, account.PasswordHash)
if err != nil {
	return err
}
if !matched {
	return auth.ErrUnauthenticated
}
```

Only `true, nil` passes this credential check. A well-formed hash with a wrong
password returns `false, nil`. An error matching
`errors.Is(err, hash.ErrInvalidHash)` means malformed or unsupported stored data,
not an ordinary mismatch. Import `errors` from the standard library and `hash`
from `arkoren.dev/core/service/hash` when classifying it. Other encoding or
driver errors are failures too; none authorizes login.

Keep failures distinguishable internally without exposing storage details,
passwords, or hashes to the client. The final digest comparison does not make
the whole login request constant-time. Account lookup, unknown-account paths,
different stored costs, and errors can change duration. Follow the
[account-hiding login flow](../authentication/session-login.md#check-passwords)
for dummy checks and uniform public rejection; it is not a guarantee of identical
request timing.

`Check` does not update the account or consume the password. After success,
[check for a hash upgrade](upgrading.md) before discarding the attempt from your
request handling.

## Input Encoding

Exact `string` and `[]byte` values use their raw bytes. Other values, including
defined string or slice types, are JSON-encoded before hashing or checking.
Use the same encoding on both paths; explicitly convert a defined password type
to `string` if raw password bytes are intended. `HashRaw` and `CheckRaw` accept
bytes directly.

The typed methods clear their own encoded copy after use, not your input.
The raw methods also leave the caller's input unchanged. This is not a promise
that all copies of a credential are erased from application memory.

Slow password hashing is for guessable, human-chosen secrets. For securely
generated, high-entropy [API keys](../authentication/api-tokens.md#issue-keys),
a fast SHA-256 digest is appropriate instead. If you must recover a secret's
original value, such as an OAuth refresh token, use
[encryption](../encryption/overview.md), not password hashing.

For all operations, see the [`hash` API reference](https://pkg.go.dev/arkoren.dev/core/service/hash).
