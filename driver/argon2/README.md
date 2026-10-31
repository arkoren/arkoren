# Argon2 password hashing driver

`arkoren.dev/driver/argon2/argon2hash` implements the core hash driver with
Argon2id by default and selectable Argon2i for compatibility. Both variants support
Argon2 version 19 (`0x13`). `New` uses the native library's `argon2.DefaultConfig`
defaults: three iterations, 64 MiB, four lanes, a 16-byte random salt and a
32-byte digest. Construction performs no lifecycle I/O.

For password storage in a new application, install `go get arkoren.dev/driver/argon2`
and import `argon2hash` from `arkoren.dev/driver/argon2/argon2hash`.
Given the validated password string from your registration form, hash it:

```go
driver := argon2hash.New(func(hasher *argon2hash.Builder) {
	hasher.MemoryCost(64 * 1024) // KiB
})
encoded, err := driver.Hash([]byte(password))
// Store encoded unchanged: it contains the salt and parameters.
```

Callbacks run directly once in order on one fresh draft; supplied nil entries
panic, expanded nil/empty slices omit callbacks. HashLength, SaltLength,
TimeCost, MemoryCost, Parallelism, Mode and Version replace their previous
values and return the same draft. Zero settings restore library defaults after
all callbacks; Mode zero is not a request for Argon2d. `New` retains nonzero
settings without eager validation. `Hash` rejects unsupported versions before
computation and returns no record; other native parameters remain your
responsibility. Use `argon2.ModeArgon2id` for new passwords. If compatibility
requires `argon2.ModeArgon2i`, retain at least three iterations to resist
time-memory tradeoffs. Both variants require `argon2.Version13` (decimal 19).

Publication copies all scalar settings. Retained drafts cannot change a driver,
and callback policies can be reused without sharing drafts. Hash and Check do
not change input passwords. Rate-limit password endpoints: hashing deliberately
consumes CPU and memory. No Start or Stop hook is needed.

`Check` accepts supported Argon2i and Argon2id records using their stored
parameters, independently of the driver's policy for new hashes. Only `true, nil`
permits login; a wrong password returns `false, nil`. Malformed or unsupported
records return an error wrapping `hash.ErrInvalidHash`. Stored costs are used
as-is, so accept only application-owned records and bound concurrent checks.

`NeedsRehash` flags different hash length, salt length, resource parameters, mode
or version, as well as malformed and unsupported records. It does not verify a
password. Rehash only after successful verification and replace the original
record only if it has not changed, so a concurrent password reset is not
overwritten. Bound concurrent hashes to fit your memory budget; the default is
64 MiB per operation.

## Unsupported version-16 records

The pinned native computation implements only version 19, despite exposing an
`argon2.Version10` constant. Configuring Version10 (decimal 16) now makes `Hash`
fail rather than label a version-19 digest as version 16. `Check` rejects every
unsupported stored version before password computation, and `NeedsRehash`
reports true for those records.

**Do not change an encoded `v=16` label to `v=19`.** A genuine version-16 record
and a historically mislabeled version-19 record cannot be distinguished safely
by their labels. If you have such records, use a correct legacy verifier followed
by rehashing, or an application-owned password reset. This driver does not repair,
relabel or reinterpret them automatically.

See the [API reference](https://pkg.go.dev/arkoren.dev/driver/argon2/argon2hash).
