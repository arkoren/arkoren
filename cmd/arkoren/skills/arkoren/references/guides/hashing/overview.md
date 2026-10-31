---
title: Hashing Overview
nav: Overview
description: Store passwords so they cannot be recovered, check them at login, tell a wrong password from a broken hash, and know what needs one.
order: 10
---

# Hashing Overview

## Introduction

A service that signs people in with a password must be able to check the
password without storing a readable copy. If the database leaks, an attacker
should have to guess each password rather than read it directly. **Password
hashing** turns a password into an encoded value that lets you check a later
attempt without decrypting the original. Weak passwords can still be guessed.

Ordinary hash functions such as SHA-256 are the wrong tool for this. They are
designed to be fast, and an attacker with a leaked database can try billions of
guesses per second against them. Password hashing functions are deliberately
slow, and Argon2id also uses substantial memory, so each guess costs time and
resources. Every hash also gets a random **salt**, so identical passwords produce different
hashes and each one must be attacked on its own.

Arkoren's hash service wraps two such functions behind a `Hasher` facade:
**Argon2id**, the current recommendation, and **bcrypt**, the long-standing
standard. The driver manages salts and algorithm parameters; the facade also
encodes typed inputs. Your application stores the resulting hash and checks it
at login. How slow a hash should be is covered in
[Choosing the Cost](cost.md), keeping stored hashes current in
[Upgrading Hashes](upgrading.md), and the two algorithms in
[Hashing Drivers](drivers.md).

## Creating a hasher

After [installing the Argon2id driver](drivers.md#argon2id), create a hasher at
startup and share it:

```go
import (
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/argon2/argon2hash"
)

hasher := hash.New(argon2hash.New())
```

Neither driver has a lifecycle or can fail to start, so there is nothing to
register on the application. A hasher never changes after it is created and is
safe to use from many requests at once.

To observe hashing cost and check outcomes, set `Builder.Telemetry` in a trailing
`hash.New` callback using your [telemetry service](../observability/telemetry.md).
If you omit it or set nil, each call follows `telemetry.Default()`; pass
`telemetry.New(nil)` to disable reporting explicitly. This emits aggregate counts
and durations, not traces or events. Encoding failures count as errors; a wrong
password with no error counts as a mismatch. Rehash metrics report policy
observations, not stored password updates. Metrics never contain passwords,
stored hashes or native error text. The [hash API reference](https://pkg.go.dev/arkoren.dev/core/service/hash)
lists metric names and bounded label values.

Focused examples keep error results visible but omit routine error handling.
Handle those errors in your application before using the results.

## Hashing a password

At sign-up, or when a person changes their password, hash it and store the
result. If you use bcrypt, validate the password's 72-byte limit first; `Hash`
returns an error for longer inputs. Argon2 does not have that bcrypt-specific
limit; bound accepted input and concurrent work as described in
[Choosing the Cost](cost.md).

```go
encoded, err := hasher.Hash(password)
```

The result is a byte slice containing a self-describing encoded hash. It records the algorithm, the
parameters, and the salt next to the hash itself, so it is all you need to
check a password later. Store it as is, in a text or binary column:

```text
$argon2id$v=19$m=65536,t=3,p=4$Pq0Rq7yXH0aZ0V3hQyZ2Jg$hY1KxJf3...
```

Leave room in the column: an Argon2id hash with the default settings is about
100 characters, and longer salts or hashes make it longer. A `TEXT` column
avoids a fixed length limit. If you use `VARCHAR(255)`, check that your chosen
salt and hash lengths still fit when you change the settings.

## Checking a password

At login, check the password the person typed against the stored hash.

If you use bcrypt, your application must reject attempts longer than 72 bytes
before calling `Check`. Native comparison can accept a longer attempt sharing
a valid 72-byte prefix; the driver does not change that policy. Never truncate
the attempt. Keep the same bound when [upgrading hashes](upgrading.md), so an
unverified suffix cannot become a new password under another algorithm.

Import `auth` from `arkoren.dev/core/http/auth` for the rejection below.

```go
ok, err := hasher.Check(attempt, user.PasswordHash)
if err != nil {
	return err
}
if !ok {
	return auth.ErrUnauthenticated
}
```

The two results mean different things. `false` with no error is a wrong
password. An error wrapping `hash.ErrInvalidHash` means the stored value is not
a hash this driver understands, for example a truncated column or a hash from
another algorithm, which is a bug or data problem worth logging, not a failed
login.

The final hash comparison is constant-time; verification as a whole is not a
guarantee of identical request duration. A login form still needs care so that
it does not reveal which accounts exist;
[Session Login](../authentication/session-login.md#hiding-which-accounts-exist)
shows how.

`Check` never modifies the password it is given, so you can use the same value
again right after it, for example to
[rehash it with stronger settings](upgrading.md).

## Hashing other values

`Hash` and `Check` accept any value. Exact `string` and `[]byte` values are
hashed as their bytes; other types, including named string or slice types, are
encoded as JSON first. Check with the same type you hashed, or explicitly
convert a named type to `string` or `[]byte` when you want raw-byte behavior.
`HashRaw` and `CheckRaw` work on bytes directly.
`Hash` zeroes its own encoded copy of the value once it is done, but never the
value you passed.

Password hashing is meant for secrets that people choose. Values your service
generates with enough randomness, such as [API keys](../authentication/api-tokens.md#issuing-a-key)
or reset tokens, make guessing infeasible when generated securely with sufficient
entropy, so a fast SHA-256 hash is enough for them.
Using a slow hash there only makes every request slower.

| Value | Store it as |
| --- | --- |
| A password or passphrase a person chose | A password hash from this service |
| A random token your service generated (API key, reset token) | A SHA-256 hash |
| A value you must read back (an OAuth refresh token) | An [encrypted](../encryption/overview.md) value |

## Testing

Real password hashing makes test suites slow. In tests, build the hasher with
the cheapest settings, which keep the behavior and drop the cost:

Import `bcrypthash` from `arkoren.dev/driver/bcrypt/bcrypthash` for this
alternative to the Argon2id driver above.

```go
hasher := hash.New(bcrypthash.New(func(hasher *bcrypthash.Builder) {
	hasher.Cost(4) // The lowest cost bcrypt allows.
}))
```

Never use such settings outside tests. Let the function that builds your
services take the hasher as an argument, so production passes the real one and
tests pass the cheap one. To check a driver of your own, run the `hashtest`
suite, as described in [Conformance Suites](../testing/conformance-suites.md).

For every function, see the [`hash` API reference](https://pkg.go.dev/arkoren.dev/core/service/hash).
