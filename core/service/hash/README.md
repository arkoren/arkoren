# hash

Password hashing contracts and typed helpers.

## Implementations

- `arkoren.dev/driver/argon2/argon2hash` (default recommendation)
- `arkoren.dev/driver/bcrypt/bcrypthash` (compatibility-oriented)

## Security notes

- Use Argon2 for new systems unless interoperability constraints require bcrypt.
- Avoid logging raw passwords or hashes.
- Drivers do not modify the byte slice passed to `Hash` or `Check`, so it can be reused (for example to rehash after a successful check). `Hasher.Hash` and `Hasher.Check` zero only their own encoded copy.
- Wrap a driver with `hash.New`. `Hash` and `Check` hash strings and byte
  slices as raw bytes, so `Hash("pw")` matches `HashRaw([]byte("pw"))`; other
  values are JSON-encoded first. bcrypt rejects inputs over 72 bytes.

Every hash uses a random salt, so hashing the same password twice does not
produce equal bytes. Verification must check both results from `Check`: only
`true, nil` permits login. Rate-limit password endpoints and bound concurrent
Argon2 work to fit your memory budget.

Follow the [hashing guide](../../../cmd/arkoren/skills/arkoren/references/guides/hashing/overview.md)
for installation and password checks, or the
[`hash` API reference](https://pkg.go.dev/arkoren.dev/core/service/hash).
