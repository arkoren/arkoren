---
title: Upgrading Hashes
description: Move stored password hashes to stronger settings after login, migrate safely from bcrypt to Argon2id, and handle imported hashes.
order: 30
---

# Upgrading Hashes

## Introduction

Computers get faster, so the settings that make guessing expensive should grow
over time, and an algorithm you chose years ago may no longer be the best one.
You cannot convert a stored hash into a hash of the same password under new
settings without knowing the password. A successful login provides that moment:
check the attempt against the old hash, then hash the verified password again
with the new settings. A password reset instead replaces it with a new password.

Every hash records the algorithm and the settings it was made with, so old and
new hashes can live side by side. This page shows how to replace them as
people sign in, how to change algorithm the same way, and what to do about the
accounts that never sign in again.

## Rehashing after login

Examples import `auth` from `arkoren.dev/core/http/auth`, `bytes` and `log/slog`
from the standard library. `hasher`, `argonHasher`, and `bcryptHasher` are
`*hash.Hasher` values from `arkoren.dev/core/service/hash`.

Apply the configured algorithm's input limits before checking a password. For
bcrypt, reject attempts longer than 72 bytes; the migration example below shows
that guard. A successful native bcrypt comparison alone does not enforce it.

When you raise the work settings, supported stored hashes keep working. To bring
them up to date, check the password as usual, then ask the hasher whether the
stored hash uses the current settings. An unsupported or malformed record cannot
be upgraded through this login path: `Check` must succeed first.

Here `hasher` is your configured hasher, `user` is the account being signed in,
and `users.SetPasswordHash` is your application's persistence method, not an
Arkoren helper. It accepts the original hash and applies a conditional write,
so a concurrent password reset is never overwritten. A failed conditional match
leaves the newer password unchanged. Keep a rehash failure separate from the
login decision:

```go
ok, err := hasher.Check(attempt, user.PasswordHash)
if err != nil || !ok {
	return auth.ErrUnauthenticated
}

if hasher.NeedsRehash(user.PasswordHash) {
	fresh, err := hasher.Hash(attempt)
	if err == nil {
		err = users.SetPasswordHash(ctx, user.ID, user.PasswordHash, fresh)
	}
	if err != nil {
		slog.WarnContext(ctx, "rehash password", "user", user.ID, "error", err)
	}
}
```

`NeedsRehash` compares the recorded parameters with the current policy, so
lowering a cost setting also flags a hash. Argon2 compares salt length, hash
length, memory, iterations, parallelism, mode, and version; changing salt length
alone requires renewal. bcrypt compares cost after validating the complete
record. Both drivers return `true` for malformed records, and Argon2 also does
so for unsupported stored versions. That result is not proof of a valid password.

A failed rehash is logged rather than returned: the person already passed the
password check, and the old hash still works, so the login succeeds and the next
one tries again.

Each account moves to the new settings after a successful login and rehash
write. Accounts that never sign in keep their old hash until they do; failed
rehash writes also leave the old hash in place for another attempt later.

## Migrating from bcrypt to Argon2id

A service that started with bcrypt can migrate supported passwords to Argon2id
after a successful login. Keep a hasher for each algorithm, check each stored
hash with the one that made it, and store an Argon2id hash for later logins.

Reject bcrypt attempts longer than 72 bytes before comparison. Native bcrypt
comparison can accept an extra suffix after a valid 72-byte password; hashing
the entire attempt with Argon2id would silently change the account's password.
The limit counts bytes, not characters. If an imported account depends on
historical truncation of a longer password, use a verified password-reset or
recovery flow rather than silently truncating and migrating its login attempt.

Create `argonHasher` and `bcryptHasher` with their respective
[drivers](drivers.md) at startup. In this fragment, `user.PasswordHash` is the
stored byte slice:

```go
const bcryptLimit = 72
checker := argonHasher
if bytes.HasPrefix(user.PasswordHash, []byte("$2")) { // bcrypt hashes start with $2
	if len(attempt) > bcryptLimit {
		return auth.ErrUnauthenticated
	}
	checker = bcryptHasher
}

ok, err := checker.Check(attempt, user.PasswordHash)
if err != nil || !ok {
	return auth.ErrUnauthenticated
}
if checker == bcryptHasher {
	fresh, err := argonHasher.Hash(attempt)
	if err == nil {
		err = users.SetPasswordHash(ctx, user.ID, user.PasswordHash, fresh)
	}
	if err != nil {
		slog.WarnContext(ctx, "migrate password hash", "user", user.ID, "error", err)
	}
}
```

Pick the hasher by the hash's prefix, not by trying one and then the other:
a hash the driver cannot parse is an error, not a wrong password or a reason to
retry it with another algorithm. New passwords, from sign-up or a password change,
are always hashed with Argon2id.

## Hashes that never upgrade

Accounts that stay inactive keep their old hashes, and a database leak exposes
them at the old cost. Once most active accounts have moved, decide what to do
with the rest. A count of the remaining old hashes, by prefix or with
`NeedsRehash`, tells you how many accounts are affected.

The usual choices are to wait, which is fine when the old settings were
reasonable, or, when they were weak, to clear the remaining hashes and require
those people to set a new password through the reset flow the next time they
come back. Clearing is safe only when the reset flow works for every such
account, for example because each has a verified email address.

## Importing hashes from another system

Check the complete format and version, not only the prefix, before planning an
import. The Argon2 driver verifies Argon2id and Argon2i records with version 19:
`$argon2id$v=19$...` or `$argon2i$v=19$...`. It does not verify version 16 or other
stored versions: `Check` returns an error wrapping `hash.ErrInvalidHash`, and
`NeedsRehash` returns `true`. Do not edit an encoded version label to bypass this
check; use a verified password-reset or recovery flow for unsupported records.

The bcrypt driver requires the complete, canonically encoded record, including
its salt and digest. Truncated records, trailing data, and noncanonical encodings
are storage errors wrapping `hash.ErrInvalidHash`, not password mismatches.
Complete legacy `$2$` records remain supported, and minor-version labels follow
the native library's policy rather than an adapter allowlist. A supported label
alone does not trigger rehashing. See the [bcrypt limits](drivers.md#bcrypt)
for the full validation boundary; acceptance of a label does not establish
compatibility with every historical implementation.

Enforce the bcrypt input limit above; imported accounts that relied on truncation
need a verified recovery flow. Import a few records with known passwords in a test
first, and check them with the matching driver before migrating everyone. Hashes
in another format, such as a salted SHA-1 from an old system, cannot be checked by
either driver; send those accounts through a password reset instead.

For the settings of each algorithm, see [Hashing Drivers](drivers.md), and
for every function, the [`hash` API reference](https://pkg.go.dev/arkoren.dev/core/service/hash).
