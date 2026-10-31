# bcrypt password hashing driver

`arkoren.dev/driver/bcrypt/bcrypthash` implements the core hash driver for bcrypt
compatibility. New defaults to cost 12 and performs no lifecycle I/O.

Install `go get arkoren.dev/driver/bcrypt` and import `bcrypthash` from
`arkoren.dev/driver/bcrypt/bcrypthash`. Given a validated password string no
longer than 72 bytes, hash it before storage:

```go
driver := bcrypthash.New(func(hasher *bcrypthash.Builder) {
	hasher.Cost(12)
})
encoded, err := driver.Hash([]byte(password))
// Store encoded unchanged: it contains the salt and cost.
```

Callbacks run directly once in order on one fresh draft; supplied nil entries
panic, expanded nil/empty slices omit callbacks. Cost replaces the previous
value and returns the same draft. After all callbacks, zero selects DefaultCost
(12); other values are clamped to bcrypt.MinCost through bcrypt.MaxCost.

Publication copies the scalar cost. Retained drafts cannot change a driver,
and callback policies can be reused without sharing drafts. Hash and Check do
not change input passwords. Hash rejects passwords longer than 72 bytes.
Rate-limit password endpoints: hashing deliberately consumes CPU. No Start or
Stop hook is needed.

Enforce the same 72-byte limit before checking passwords. `Check` uses the
stored cost; a mismatch returns `false, nil`, and malformed hashes wrap
`hash.ErrInvalidHash`. Only `true, nil` permits login.
Native comparison can accept an overlength attempt sharing a valid 72-byte
prefix. The driver preserves that compatibility behavior: reject attempts over
72 **bytes** before `Check`, and never truncate or migrate an unverified suffix.

Store the complete encoded record without normalization. `Check` and
`NeedsRehash` require exactly 22 salt characters and 31 digest characters in
canonical unpadded bcrypt base64. Invalid characters, nonzero unused encoding
bits, missing bytes, and trailing data are malformed, not password mismatches.
`NeedsRehash` returns true for malformed records or a different cost. Rehash
only after successful authentication; a true result does not validate storage.

Version handling stays with the native library, including legacy `$2$` records
and `$2a$`, `$2b$`, `$2x$`, and `$2y$` labels. Labels do not select historical
implementation quirks, and a label alone does not trigger rehashing. Argon2id
is the recommended alternative for new applications without bcrypt
compatibility needs.

See the [API reference](https://pkg.go.dev/arkoren.dev/driver/bcrypt/bcrypthash).
