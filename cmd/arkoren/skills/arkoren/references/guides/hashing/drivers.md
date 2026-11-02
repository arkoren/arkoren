---
title: Hashing Drivers
nav: Drivers
description: Configure Argon2id or bcrypt and understand their stored formats, input limits, and shared metrics.
order: 20
summary:
  - Compare Argon2id and bcrypt, then construct a shared hasher without lifecycle hooks.
  - Configure new-hash policies and recognize each driver's format and input limits.
  - Observe aggregate facade outcomes without exporting credentials or stored hashes.
---

# Hashing Drivers

Choose Argon2id for new password storage. Choose bcrypt when an existing user
database or another system requires bcrypt compatibility. Both run in your
process, generate a fresh random salt for every hash, and store work settings
inside the encoded record. Neither verifies the other's format; changing
algorithms requires a [verification-time migration](upgrading.md#change-algorithms).

## Compatibility Matrix

This comparison covers Arkoren's two hashing drivers, not every feature of their
underlying libraries. Verification reads stored work settings; your new-hash
configuration does not cap verification cost.

| Driver | Random Salt | Work Settings | Memory Settings | Parallelism Settings | Rehash Detection | Other Driver's Format |
| --- | --- | --- | --- | --- | --- | --- |
| [Argon2id](#argon2id) | Yes | Yes — Iteration count. | Yes — Kibibytes per operation. | Yes | Yes — Full policy, including salt length. | No |
| [bcrypt](#bcrypt) | Yes | Yes — Logarithmic cost. | No | No | Yes — Complete-record validation and stored cost. | No |

Both support concurrent callers after construction and require no lifecycle
hooks. Neither has credentials, a network connection, a native-client accessor,
or a companion adapter. Facade metrics are available with either driver.

## Argon2id

The Argon2 driver creates Argon2id records by default. It also supports Argon2i
when another system requires that variant. From your application's module
directory, install the driver:

```sh
go get arkoren.dev/driver/argon2
```

### Create Hasher

Import the driver and facade, then construct one shared hasher:

```go
import (
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/argon2/argon2hash"
)

hasher := hash.New(argon2hash.New())
```

Construction performs no hashing or lifecycle I/O and returns no error. The
published settings are immutable. Use this `hasher` to
[store and check passwords](overview.md#store-passwords).

### Set Policy

Defaults come from the native library's `argon2.DefaultConfig`: Argon2id version
19, 64 MiB of memory, three iterations, four parallel lanes, a 16-byte salt, and
a 32-byte digest. Measure these defaults under your deployment's resource limits
before changing them, following [Choosing Cost](cost.md).

If measurements justify 128 MiB per operation while retaining the other defaults,
construct an alternative policy:

```go
hasher := hash.New(argon2hash.New(func(policy *argon2hash.Builder) {
	policy.MemoryCost(128 * 1024)
}))
```

`MemoryCost` takes kibibytes, not bytes. `TimeCost`, `Parallelism`, `SaltLength`,
and `HashLength` configure the other dimensions. Zero settings restore native
defaults after all callbacks. Nonzero settings are not validated at construction;
use application-controlled settings, not arbitrary request values.

For an Argon2i interoperability requirement, import native constants from
`github.com/matthewhartstonge/argon2` and construct this alternative:

```go
hasher := hash.New(argon2hash.New(func(policy *argon2hash.Builder) {
	policy.Mode(argon2.ModeArgon2i)
	policy.TimeCost(3)
}))
```

Keep at least three iterations for Argon2i. Retain Argon2id for new passwords
otherwise. Only version 19 (`argon2.Version13`) is supported. Setting version 16
or another nonzero unsupported version does not fail construction, but `Hash`
returns an error before computing a digest. Other native hashing failures are
returned to the caller.

### Stored Records

`Check` accepts supported version-19 Argon2id and Argon2i records regardless of
the configured new-hash variant. It uses the record's memory, iteration, and
parallelism values as stored. Accept only application-owned records and
[bound concurrent work](cost.md#budget-concurrent-work).

A mismatch returns `false, nil`. Malformed records and unsupported versions
return an error wrapping `hash.ErrInvalidHash`. Version 16 is rejected, not
silently treated as version 19.

`NeedsRehash` compares memory, iterations, parallelism, mode, version, salt
length, and digest length. Malformed or unsupported records also return `true`;
that does not authorize login or replacement without a successful check.
Neither hashing nor checking modifies the caller's input bytes.

For all settings, see the [`argon2hash` API reference](https://pkg.go.dev/arkoren.dev/driver/argon2/argon2hash).

## bcrypt

The bcrypt driver uses Go's extended crypto library. Choose it for bcrypt
compatibility, and test imported records with known fixture passwords before
depending on historical implementation behavior. Install it from your
application's module directory:

```sh
go get arkoren.dev/driver/bcrypt
```

### Create Hasher

Construct the driver and facade once:

```go
import (
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/bcrypt/bcrypthash"
)

hasher := hash.New(bcrypthash.New())
```

Construction returns no error and performs no hashing or lifecycle I/O. Use the
shared [password operations](overview.md#store-passwords), enforcing bcrypt's
input limit before both hashing and checking.

### Set Policy

Arkoren's default cost is `bcrypthash.DefaultCost`, or 12. A cost increase of one
approximately doubles the computation. If measurements justify cost 13,
construct an alternative hasher:

```go
hasher := hash.New(bcrypthash.New(func(policy *bcrypthash.Builder) {
	policy.Cost(13)
}))
```

Zero selects 12; other values are clamped to the supported range of 4 through 31.
Clamping is not a resource-safety policy: the upper endpoint is extraordinarily
expensive. Keep the cost application-controlled. Low settings such as 4 are
useful for isolated behavior tests, not a production recommendation.

### Stored Records

`Hash` rejects passwords longer than 72 bytes with the native
`bcrypt.ErrPasswordTooLong` error. Characters can occupy multiple bytes.
Your application must also reject overlength attempts before `Check`, because
native verification can accept a longer value sharing a valid 72-byte prefix.
Never truncate an attempt or migrate its unverified suffix into Argon2id.

Before computing a password check, the driver validates the complete stored
record: exact length, separators, two decimal cost digits, and canonical unpadded
bcrypt base64 for the 22-character salt and 31-character digest. Truncation,
trailing data, invalid characters, line breaks, and nonzero unused encoding bits
produce an error wrapping `hash.ErrInvalidHash`. A canonical record with a wrong
password returns `false, nil` instead.

Complete legacy `$2$` records are supported. Minor labels, including `$2a$`,
`$2b$`, `$2x$`, `$2y$`, and unrecognized minor labels, follow the native verifier;
the driver adds no minor-label allowlist. This does not establish compatibility
with every historical bcrypt implementation.

`NeedsRehash` validates the same complete record without password computation,
then compares stored cost with configured cost. A supported minor label alone
does not request renewal. Checking uses the stored cost even after you change
the new-hash policy. Input bytes remain unchanged.

For all settings, see the [`bcrypthash` API reference](https://pkg.go.dev/arkoren.dev/driver/bcrypt/bcrypthash).

## Observe Operations

To measure outcomes through either facade, pass your existing telemetry service
as `metrics` when constructing the facade around an existing `driver`:

```go
hasher := hash.New(driver, func(policy *hash.Builder) {
	policy.Telemetry(metrics)
})
```

Omitting telemetry or setting nil follows `telemetry.Default()` per call.
Passing `telemetry.New(nil)` explicitly disables reporting; import `telemetry`
from `arkoren.dev/core/service/telemetry`. See
[Telemetry](../observability/telemetry.md) for service configuration.

Facade counts and durations cover encoding and driver outcomes, not traces or
events. A password mismatch is not an error; a rehash-needed observation is not
a persisted password update. Metric labels contain no password, stored hash, or
native error text. Direct driver calls bypass these metrics.

For facade operations and metric names, see the [`hash` API reference](https://pkg.go.dev/arkoren.dev/core/service/hash).
