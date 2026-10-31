---
title: Storing Encrypted Data
nav: Storing Data
description: Store encrypted values in binary or text columns, find rows by an encrypted value with a keyed hash, and re-encrypt after a key rotation.
order: 30
---

# Storing Encrypted Data

## Introduction

An encrypted value is only useful if it can be stored and found again. It is
binary, a little longer than the original, and different every time the same
value is encrypted, which affects the column it goes in, how it travels as
text, and whether you can look rows up by it.

This page covers those practical questions: the column types and encodings,
how to find a row by a value you only store encrypted, and how to move stored
values to a new key after a [rotation](keys.md#rotating-keys).

## Columns and sizes

Examples use `database` from `arkoren.dev/core/service/database` through an
existing facade named `db`, `query` from `arkoren.dev/foundation/query`, and
`crypto` from `arkoren.dev/core/service/crypto`. The encoding and lookup helpers
use standard-library `encoding/base64`, `crypto/hmac`, `crypto/sha256`, and
`strings`; the row types belong to your application. Import standard-library
`errors` for the rotation conflict below.

An encrypted value is 28 bytes longer than its input with both drivers: a
12-byte nonce in front and a 16-byte authentication tag behind. Store it in a
binary column, such as `BYTEA` in PostgreSQL, `VARBINARY` or `BLOB` in MySQL,
or `BLOB` in SQLite, and scan it into a `[]byte` field:

```go
type Connection struct {
	ID          int    `db:"id"`
	UserID      int    `db:"user_id"`
	Credentials []byte `db:"credentials"` // sealed with enc.Encrypt
}
```

Values encrypted with `Encrypt` are JSON before encryption, so their size
depends on the field names too. Size a fixed-length column for the largest
JSON value plus 28 bytes, or use a column without a length limit.

### Encrypted values as text

Where only text fits, for example in a cookie, a URL, or a JSON response,
encode the value with `base64.RawURLEncoding`, which uses only characters that
are safe in URLs and cookies:

```go
token := base64.RawURLEncoding.EncodeToString(sealed)

sealed, err := base64.RawURLEncoding.DecodeString(token)
```

Encoding makes the value about a third longer. Cookies are limited to about
4 KB, so keep what you encrypt into them small.

## Finding rows by an encrypted value

Because each encryption uses a fresh nonce, encrypting an email address again
does not reproduce the stored ciphertext. You cannot find the row by encrypting
the search value and comparing ciphertexts. To look rows up by a value you store
encrypted, store a second column
next to it: a **keyed hash** of the value, computed with HMAC-SHA256 and a key
of its own. The same input always gives the same hash, so the column can be
indexed and compared, while nobody without the key can compute it:

```go
func lookupHash(key []byte, email string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(email))))

	return mac.Sum(nil)
}
```

Here `lookupHash` is application code, not a shipped helper. Normalize the
value before hashing, as above, so that `Ada@Example.com` and
`ada@example.com` find the same row. This application treats email addresses
case-insensitively; choose normalization that agrees with your own account
policy and use it on both writes and lookups. Store the hash in an indexed binary
column, and query by it using your existing `db` facade and `User` row type:

```go
user, err := db.Find[User](ctx, query.Table("users", func(statement *query.Builder) {
	statement.Where("email_hash", lookupHash(hashKey, email))
}))
```

Use a separate key for the hash, not the encryption key, and keep it in the
secret store with the other keys. A keyed hash reveals which rows hold the
same value, which is the point of it; only add one for values you actually
need to search by.

> [!NOTE]
> A plain SHA-256 hash, without a key, is not enough here. Email addresses,
> phone numbers, and similar values are easy to guess, so anyone with the
> database could hash candidates until one matches.

## Re-encrypting stored values

Rotation never rewrites stored data by itself. To move a value to the new key,
decrypt it with `DecryptWithRotation`, which also reports whether an old key
was needed, and store it again. Here `enc` accepts the current and previous
keys, `conn` is a stored `Connection`, and `Credentials` is the plaintext type
from [the overview](overview.md#encrypting-values):

Match the original ciphertext when writing the replacement. This prevents a
rotation from overwriting credentials changed by another request.

```go
creds, rotated, err := enc.DecryptWithRotation[Credentials](conn.Credentials)
if err != nil {
	return err
}
if rotated {
	fresh, err := enc.Encrypt(creds)
	if err != nil {
		return err
	}
	n, err := db.Exec(ctx, query.Table("connections", func(statement *query.Builder) {
		statement.Where("id", conn.ID)
		statement.Where("credentials", conn.Credentials)
		statement.Update(map[string]any{"credentials": fresh})
	}))
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("credentials changed; retry rotation with the current row")
	}
}
```

Keep the error checks here: a failed decryption or encryption must never
replace stored credentials. Check the database write error too before treating
that row as rotated. The original-ciphertext predicate handles concurrent
credential changes; alternatively, lock the row in a transaction before reading
and rotating it.

Doing this whenever a value is read moves active data over gradually. To
finish a rotation, run a [queue job](../queues/overview.md) or a one-off
command that walks the table in batches and applies the steps above, wrapped
in a `rotate` function, to every row:

```go
for conn, err := range db.Each[Connection](ctx, query.Table("connections", func(statement *query.Builder) {
	statement.OrderBy("id")
}), 500).Iter() {
	if err != nil {
		return err
	}
	if err := rotate(ctx, conn); err != nil {
		return err
	}
}
```

`rotate` above is your own function implementing the per-row operation, not an
Arkoren helper. `Each` reads one batch at a time by `id`, so updating the rows it has already
passed is safe, and memory stays bounded however large the table is. The job
can be stopped and run again: rows already on the new key report
`rotated == false` and are left alone. When a full run finds nothing to
rotate, and no writer still uses the old key, remove the previous driver from
the encrypter. Retain the key securely for any backups that still need it, as
described in [Rotating keys](keys.md#rotating-keys).

For the encrypter, see the [`crypto` API reference](https://pkg.go.dev/arkoren.dev/core/service/crypto).
