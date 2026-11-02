---
title: Upgrading Hashes
description: Renew verified password hashes with conditional writes and migrate supported records without changing unverified credentials.
order: 40
summary:
  - Renew hashes only after successful password verification and a policy comparison.
  - Use conditional storage updates that leave concurrent credential changes untouched.
  - Migrate supported bcrypt records to Argon2id and plan recovery for unsupported or inactive accounts.
---

# Upgrading Hashes

A stored hash cannot be converted into a hash of the same password under new
settings without the password. A successful login supplies a verified attempt:
check it against the old record, then hash those same password bytes under the
new policy. A password reset is different: it establishes a new password through
your application's recovery flow.

This page assumes configured [hashing drivers](drivers.md), trusted account
storage, and the [ordinary password check](overview.md#check-passwords). Budget
the old check and new hash together under your [password-work limits](cost.md).
Do not retain plaintext passwords in a queue or log them for a later migration.

## Detect Renewal

Changing construction settings affects new hashes, not the parameters used to
check an existing record. `NeedsRehash` compares a stored record with the current
policy; it does not rewrite storage or prove a password match.

Argon2 compares memory, iterations, parallelism, variant, version, salt length,
and digest length. bcrypt validates the complete record and compares cost.
Differences in either direction request renewal, so lowering the configured
cost can also flag a stronger stored hash. Review the target policy before
deploying a bulk policy change.

Both drivers return `true` for malformed records; Argon2 also does so for
unsupported stored versions. These records cannot be upgraded through a login
check that fails. Never treat `NeedsRehash` alone as permission to replace them.

## Verify First

Assume a decoded `LoginInput` with a `Password string` field, an account named
`account`, and the current `*hash.Hasher` named `hasher`. Apply the appropriate
input limits first. For bcrypt, reject attempts over 72 bytes before comparison:
native checking can accept an extra suffix after a valid 72-byte prefix.

With `auth` imported from `arkoren.dev/core/http/auth`, establish the credential
decision separately from renewal:

```go
matched, err := hasher.Check(input.Password, account.PasswordHash)
if err != nil {
	return err
}
if !matched {
	return auth.ErrUnauthenticated
}
```

Only this success path may attempt renewal. Preserve malformed-storage and other
failures internally instead of counting them as ordinary wrong passwords. Map
public failures without leaking account existence or storage details, as in
[Session Login](../authentication/session-login.md#check-passwords).

## Replace Safely

Use a conditional write so renewal cannot overwrite a password reset that
completed after your account read. In this example, `accounts.ReplacePasswordHash`
is an application-owned repository method, not an Arkoren API. It takes the
account ID, expected old hash, and replacement, and returns `(applied bool, err error)`.
It must atomically update only that account whose stored hash still equals the
expected value. No match is a concurrent change, not permission to retry an
unconditional write.

After the successful check above, create a replacement only if policy differs.
Import standard-library `log/slog` for the bounded failure reports:

```go
if hasher.NeedsRehash(account.PasswordHash) {
	fresh, err := hasher.Hash(input.Password)
	if err != nil {
		slog.WarnContext(ctx, "password hash renewal failed", "stage", "hash")
	} else {
		applied, err := accounts.ReplacePasswordHash(ctx, account.ID, account.PasswordHash, fresh)
		if err != nil {
			slog.WarnContext(ctx, "password hash renewal failed", "stage", "store")
		} else if !applied {
			slog.InfoContext(ctx, "password hash renewal skipped", "reason", "concurrent change")
		}
	}
}
```

A successful write installs a fresh salted record. A rejected conditional write
leaves the newer credential untouched. This example treats renewal failures as
best-effort maintenance after a valid check, rather than rejecting login because
renewal failed; enforce any separate account or concurrent-reset login policy in
your authentication flow. An unchanged old record can be retried on a later
successful login.

Keep the attempt only for this request's synchronous work. The failure reports
contain no credential, stored hash, or native error text; use similarly bounded
diagnostics if you need to distinguish failures in monitoring.

## Change Algorithms

To move bcrypt accounts to Argon2id, keep a legacy bcrypt hasher alongside your
target Argon2id hasher. New registrations and password changes use the target.
Existing logins route by a known stored format, then let the selected driver
validate the complete record. Do not try another driver after a mismatch or
parse error.

Here `hasher` is the target Argon2id facade, `bcryptHasher` is the legacy facade,
and `bytes` is the standard-library package. Select the checker from the
application-owned record:

```go
checker := hasher
switch {
case bytes.HasPrefix(account.PasswordHash, []byte("$2")):
	if len(input.Password) > 72 {
		return auth.ErrUnauthenticated
	}
	checker = bcryptHasher
case bytes.HasPrefix(account.PasswordHash, []byte("$argon2id$")),
	bytes.HasPrefix(account.PasswordHash, []byte("$argon2i$")):
	// The Argon2 driver validates the stored variant, version, and parameters.
default:
	return auth.ErrUnauthenticated
}
```

A recognized prefix is only routing information, not format validation. Record
unknown formats internally as unsupported storage without exposing the record
to clients. Now verify with the selected checker:

```go
matched, err := checker.Check(input.Password, account.PasswordHash)
if err != nil {
	return err
}
if !matched {
	return auth.ErrUnauthenticated
}
```

After success, use the conditional renewal block above with the target `hasher`.
Its `NeedsRehash` returns true for bcrypt records it cannot parse, causing the
verified attempt to be hashed with Argon2id. It also renews supported Argon2
records whose policy differs. The prior successful legacy check—not that policy
result—is what makes migration eligible.

The 72-byte guard prevents an unverified bcrypt suffix from becoming part of a
new Argon2 password. If an imported account relied on historical truncation,
send it through verified recovery instead of truncating and migrating an attempt.

## Import Records

Treat imports as a controlled migration, not an endpoint for client-submitted
hashes. Audit formats and encoded work factors before enabling checks: a
parseable record can still demand excessive CPU or memory. Use isolated,
resource-limited tests with known fixture passwords, never production passwords
in source or logs. Confirm both valid and invalid attempts with the matching
driver before importing the full set.

Argon2 accepts Argon2i and Argon2id version 19 records. Version 16 and other
versions fail with `hash.ErrInvalidHash`. Do not edit a version label to force
acceptance; use a correct, separately reviewed legacy verifier or a verified
recovery flow for unsupported data.

bcrypt requires complete canonical salt and digest encodings. Truncated records,
trailing data, and noncanonical encodings are storage failures, not mismatches.
Legacy `$2$` and native-accepted minor labels do not prove compatibility with
historical algorithm quirks; test representative fixtures and retain the
[bcrypt input limit](drivers.md#bcrypt).

Neither driver accepts arbitrary legacy schemes such as salted SHA-1. Without
a suitable reviewed verifier, require password recovery rather than guessing a
conversion or accepting a hash as a credential.

## Inactive Accounts

Accounts that never log in retain their old records, as do accounts whose renewal
has not succeeded. Count those records by known scheme or policy to assess what
remains; a policy count is not evidence that their passwords still verify.

Choose whether to retain acceptable legacy records or require recovery for weak
ones. Before disabling any old credential, ensure the account has a working,
verified recovery route and plan the user impact. Do not clear hashes merely to
make a migration count reach zero: that can lock people out permanently.

For verification and policy operations, see the [`hash` API reference](https://pkg.go.dev/arkoren.dev/core/service/hash).
