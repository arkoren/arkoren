---
title: Hashing Drivers
nav: Drivers
description: Choose Argon2id for new services or bcrypt for compatibility, install either driver, and set its memory, iterations, or cost.
order: 40
---

# Hashing Drivers

## Introduction

Choose **Argon2id** for new services. It won the Password Hashing Competition,
is the first recommendation of OWASP and RFC 9106, and its memory cost makes
attacks with graphics cards and custom hardware far more expensive. Choose
**bcrypt** when you share a user table with a system that already uses it, or
need hashes that other tools can verify.

Both drivers embed a fresh random salt in every hash, record their settings in
it, and pass the same `hashtest` conformance suite. You use the same facade calls
with either, but their resource costs, input limits, and stored formats differ.
A hash made by one cannot be checked by the other;
[Upgrading Hashes](upgrading.md#migrating-from-bcrypt-to-argon2id) shows how
to move between them.

## Compatibility matrix

Compare the settings and password formats exposed by Arkoren's local hash
drivers. Both embed the salt and work settings in the stored hash; neither
verifies the other driver's format. `NeedsRehash` reports a policy difference,
not permission to log in or an automatic rewrite of the stored hash.

| Driver | Embedded random salt | Configurable work factor | Configurable memory | Configurable parallelism | Rehash detection | Aggregate facade metrics |
| --- | --- | --- | --- | --- | --- | --- |
| [Argon2id](#argon2id) | Yes | Yes | Yes | Yes | Yes — Detects salt-length differences and unsupported stored versions. | Yes — Through hash.Hasher. |
| [bcrypt](#bcrypt) | Yes | Yes | No | No | Yes — Validates the entire record, then compares stored cost. | Yes — Through hash.Hasher. |

Metrics are a shared facade feature, not a difference between these drivers.
Configure `hash.Builder.Telemetry` in a trailing `hash.New` callback using your
[telemetry service](../observability/telemetry.md); do not set it on a driver
builder. Nil follows `telemetry.Default()` per call; `telemetry.New(nil)` disables
reporting explicitly. Direct driver calls emit no facade metrics.

Counts and durations cover complete facade calls, including encoding failures,
without traces or events. A password mismatch is not an error, and a rehash-needed
observation does not mean a hash was updated. Passwords, stored hashes and native
error text never become labels. See the [hash metric names and label values](https://pkg.go.dev/arkoren.dev/core/service/hash).

Argon2's memory and iteration settings differ from bcrypt's logarithmic cost;
their numeric values are not interchangeable. bcrypt rejects passwords longer
than 72 bytes when hashing; enforce that limit before checking passwords too.

## Argon2id

The Argon2 driver defaults to Argon2id, which combines memory-hard hashing with
side-channel resistance. It runs locally without a server or credentials.

### Installation

Run this command from your application's module directory:

```sh
go get arkoren.dev/driver/argon2
```

### Connecting

Create the driver and facade at startup:

```go
import (
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/argon2/argon2hash"
)

hasher := hash.New(argon2hash.New())
```

Construction does not hash anything or perform I/O. No lifecycle hooks are needed.

### Configuration

Defaults are Argon2id version 19, 64 MiB, three iterations, four parallel lanes,
a 16-byte salt, and a 32-byte hash. Measure the cost on production hardware
before changing it. To use 128 MiB and four iterations, pass this callback to
`argon2hash.New`:

```go
configure := func(hasher *argon2hash.Builder) {
	hasher.MemoryCost(128 * 1024) // 128 MiB
	hasher.TimeCost(4)
}
```

Raising memory makes attacks more expensive than raising iterations does, so
increase memory first, as far as your servers' concurrency allows;
[Choosing the Cost](cost.md#memory-and-concurrent-logins) shows how to bound
it. The driver also lets you change parallelism and salt and hash lengths.
If another system requires newly created hashes in Argon2i format, set
`Mode(argon2.ModeArgon2i)` on the construction draft and keep at least three
iterations. Import the native constants from `github.com/matthewhartstonge/argon2`;
keep the default Argon2id variant for new passwords otherwise.

Both Argon2i and Argon2id support version 19 only (`argon2.Version13`). Version 16
(`argon2.Version10`) is not supported. Zero settings restore library defaults
after all callbacks. Construction does not validate nonzero parameters, but
`Hash` rejects an unsupported configured version before computing or returning
a digest. Other nonzero settings pass through to the native library.

### Checking passwords and upgrading settings

Follow [Checking a password](overview.md#checking-a-password) with `hasher`;
only a successful check authorizes login. [Upgrading Hashes](upgrading.md)
shows how to replace a hash after that check.

### Behavior and limits

Each hash has a fresh random salt. `Check` accepts version-19 Argon2i and Argon2id
records, reading parameters from the stored hash rather than the current policy.
Budget concurrent checks and accept only trusted stored hashes. A mismatch returns
`false, nil`; malformed hashes and unsupported stored versions return an error
wrapping `hash.ErrInvalidHash`. Version 16 is rejected before password verification,
not interpreted as version 19.

`NeedsRehash` compares salt length, hash length, memory, iterations, parallelism,
mode, and version. A salt-length-only change flags existing hashes without changing
how `Check` verifies them. Malformed or unsupported records also return `true`,
but cannot authorize a login or a rehash without a successful check; follow the
[import guidance](upgrading.md#importing-hashes-from-another-system).
Input bytes are never modified. There is no native client, companion adapter,
or external service to run locally.

### API references

For every option, see the
[`argon2hash` API reference](https://pkg.go.dev/arkoren.dev/driver/argon2/argon2hash).

## bcrypt

The bcrypt driver uses Go's extended crypto library. Choose it for an existing
bcrypt password database or interoperability; it needs no service or credentials.

### Installation

Run this command from your application's module directory:

```sh
go get arkoren.dev/driver/bcrypt
```

### Connecting

Create the facade once and share it between requests:

```go
import (
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/bcrypt/bcrypthash"
)

hasher := hash.New(bcrypthash.New())
```

Construction performs no I/O and needs no lifecycle hooks.

### Configuration

Its only setting is the **cost**, the base-2 logarithm of the number of rounds:
each step up doubles the time. The default is 12, and values outside bcrypt's
range of 4 to 31 are clamped to the nearest endpoint. Zero selects 12. Pass
this callback to `bcrypthash.New` after measuring its cost:

```go
configure := func(hasher *bcrypthash.Builder) {
	hasher.Cost(13)
}
```

### Checking passwords and upgrading settings

Use the shared [password check](overview.md#checking-a-password), then
[rehash after login](upgrading.md#rehashing-after-login) if the cost differs.
Checking an old hash uses its stored cost, so raising the construction setting
does not invalidate it.

### Behavior and limits

`Hash` rejects inputs longer than 72 bytes. Limit
passwords to 72 bytes in your validation when you use it, keeping in mind that
characters outside ASCII take several bytes each. Your application must reject
overlength attempts before `Check`: native comparison can accept a longer attempt
sharing a valid 72-byte prefix. The driver preserves that native comparison
behavior; it does not enforce the input limit for you during checking. Never
truncate the attempt or migrate its unverified suffix into another algorithm.

Before comparing passwords, `Check` validates the complete stored record: exact
length, separators, two decimal cost digits, and canonical unpadded bcrypt base64
for its 22-character salt and 31-character digest. Missing bytes, trailing data,
invalid characters, line breaks, or nonzero unused encoding bits return an error
wrapping `hash.ErrInvalidHash`, whether or not the supplied password would match.
A complete, canonical record with a wrong password returns `false, nil` instead.

`NeedsRehash` uses the same complete-record validation without hashing a password;
it reports malformed records or a different stored cost. Version-label handling
remains native: complete legacy `$2$` records are supported. Labels such as `$2a$`,
`$2b$`, `$2x$`, and `$2y$`, as well as unrecognized minor labels, follow the native
verifier; the driver does not impose a minor-version allowlist. A native-accepted
minor label alone does not require rehashing. This is not a guarantee of every
historical bcrypt implementation's behavior;
[test imported hashes](upgrading.md#importing-hashes-from-another-system) with known
passwords before migration. Input is not modified. There is no native client,
additional adapter, or external service.

### API references

For every option, see the
[`bcrypthash` API reference](https://pkg.go.dev/arkoren.dev/driver/bcrypt/bcrypthash).

For the hasher itself, see the [`hash` API reference](https://pkg.go.dev/arkoren.dev/core/service/hash).
