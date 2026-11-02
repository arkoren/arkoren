---
title: Storing Data
description: Persist encrypted records with explicit schema, identity binding, encoding, and guarded re-encryption.
order: 40
summary:
  - Store binary ciphertext through typed writes while keeping plaintext out of database records and logs.
  - Bind credentials to their expected record and handle text encoding and equality-lookup trade-offs.
  - Replace old ciphertext with concurrency guards and preserve key, context, and schema compatibility.
---

# Storing Data

Encrypting a credential protects its contents, but your application still owns
the row that contains it, the metadata used to interpret it, and the rules for
replacing it. Decide those boundaries before writing ciphertext so future
deployments and backups remain readable.

## Choosing Storage

Prefer a binary column for ciphertext: PostgreSQL `BYTEA`, MySQL `VARBINARY`
or `BLOB`, or SQLite `BLOB`. Scan it into `[]byte`. Both shipped drivers return
a 12-byte nonce followed by encrypted bytes and a 16-byte tag, adding 28 bytes
to the raw plaintext length. `Encrypt` adds JSON encoding first, so field names
and JSON escaping also affect size. Size a bounded column for the largest
encoded payload or choose an appropriate unbounded column.

Encrypt only fields that need recovery. Leaving identifiers and routing fields
outside ciphertext permits normal queries, but exposes those fields to anyone
who can read storage. Encrypting a whole payload hides more content while
removing ordinary filtering and indexing over its contents. Ciphertext still
reveals its length and does not replace database access controls or encrypted
transport.

## Writing Credentials

Assume `enc` is your configured `*crypto.Encrypter`, `db` is your configured
`*database.Database`, and `ctx` is the operation context. Imports are
`arkoren.dev/core/service/crypto`, `arkoren.dev/core/service/database`, and
`arkoren.dev/foundation/query`. The application owns a `calendar_connections`
table with `id`, `account_id`, and binary `credentials` columns. The connection
and account identifiers come from an authorized application operation, not
arbitrary writable request fields.

Use a named payload that keeps record identity with the credential. Here
`credentials` is a validated `CalendarCredentials` value from
[Overview](overview.md#encrypting-values), and `connectionID` and `accountID`
identify the authorized target:

```go
type StoredCalendar struct {
	Version      int                 `json:"version"`
	ConnectionID int64               `json:"connection_id"`
	AccountID    int64               `json:"account_id"`
	Credentials  CalendarCredentials `json:"credentials"`
}
```

Encrypt that payload before building the database update:

```go
sealed, err := enc.Encrypt(StoredCalendar{
	Version:      1,
	ConnectionID: connectionID,
	AccountID:    accountID,
	Credentials:  credentials,
})
if err != nil {
	return err
}
```

Do not write a result from a failed encryption. The identity fields and version
are encrypted and authenticated together with the credentials. For the database
write, expose only the intended ciphertext column through a separate typed input:

```go
type CredentialUpdate struct {
	Credentials []byte `db:"credentials"`
}

input := CredentialUpdate{Credentials: sealed}
statement := query.Table("calendar_connections", func(statement *query.Builder) {
	statement.Where("id", connectionID)
	statement.Where("account_id", accountID)
	statement.Update(query.Values(input))
})
affected, err := db.Exec(ctx, statement)
```

Check the write error and affected-row count before reporting success. No
matching row is not a successful credential replacement. The database `db` tags
control column mapping; the payload's `json` tags control the encrypted JSON.
Do not persist the plaintext alongside the ciphertext or include it in query logs.

## Binding Records

A purpose string such as `calendar-credentials:v1` in
[driver AAD](drivers.md#binding-context) prevents using that ciphertext under a
different purpose. It does not stop copying ciphertext between two calendar
connections that share the same driver context. AAD is fixed at driver
construction; the facade has no per-operation AAD argument.

To reject record substitution with the payload above, compare its authenticated
identity against the independently established account and requested connection.
With standard-library `errors` imported, check both decryption and the embedded
metadata before using a credential:

```go
payload, err := enc.Decrypt[StoredCalendar](stored)
if err != nil {
	return err
}
if payload.Version != 1 {
	return errors.New("unsupported calendar payload version")
}
if payload.ConnectionID != connectionID || payload.AccountID != accountID {
	return errors.New("calendar payload belongs to another connection")
}
```

Here `stored` is the selected row's ciphertext. Do not take the expected identity
from the decrypted payload itself or trust an attacker-supplied account ID.
Cleartext columns and external version headers are not automatically
authenticated by encryption. Put security-relevant metadata inside the encrypted
payload or bind it through the exact configured AAD, then verify its meaning.
Neither approach replaces access authorization or prevents replay of an older
valid payload for the same record.

## Encoding Text

Where a transport accepts only text, encode the complete ciphertext. With
standard-library `encoding/base64`, URL-safe unpadded encoding is suitable for
an application token:

```go
token := base64.RawURLEncoding.EncodeToString(sealed)
```

Encoding adds roughly one third to the length; it is not another encryption
layer. Keep cookie payloads small enough for browser limits and account for
headers and other cookie fields. Avoid putting credentials in URLs, where
history, referrers, and access logs can expose replayable values.

Decode using the matching encoding before decrypting:

```go
stored, err := base64.RawURLEncoding.DecodeString(token)
if err != nil {
	return err
}
```

A base64 error is an encoding failure, not `crypto.ErrInvalidCiphertext`.
Successful decoding says nothing about authenticity; decryption must still
succeed. Apply transport size limits before allocating or decoding untrusted
input. Never use base64 to justify logging a key, plaintext, or ciphertext token.

## Searching Values

Random nonces mean encrypting a search term again does not reproduce stored
ciphertext. Query by an ordinary identifier whenever possible. If equality
lookup over encrypted data is necessary, an application-owned indexed keyed
hash can accompany the ciphertext, but reveals which rows share a value.

For an application that deliberately treats contact addresses case-insensitively,
this helper uses standard-library `crypto/hmac`, `crypto/sha256`, and `strings`:

```go
func contactLookup(secret []byte, email string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(email))
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(normalized))

	return mac.Sum(nil)
}
```

Choose normalization according to your account policy and apply it identically
on writes and searches. Use an independent protected lookup key, not the
encryption key. Store the result in an indexed binary column such as
`contacts.email_lookup`, updating it consistently with the encrypted address.
A plain unkeyed hash permits guessing common addresses from a database copy.

With validated `email`, protected bytes `lookupKey`, and your application row
type `Contact`, restrict the lookup to the authorized account:

```go
contact, err := db.Find[Contact](ctx, query.Table("contacts", func(statement *query.Builder) {
	statement.Where("account_id", accountID)
	statement.Where("email_lookup", contactLookup(lookupKey, email))
}))
```

The hash supports equality, not arbitrary encrypted search. Add it only when
the lookup is needed, and plan a separate index migration if its key or
normalization changes. Arkoren does not generate or rotate this index for you.

## Re-encrypting Values

[Key rotation](keys.md#rotating-keys) only changes future encryption. To replace
old stored ciphertext, configure current and previous drivers and ask whether
a previous driver authenticated the value. For an authorized loaded row
`connection`, read its `Credentials []byte` field:

```go
payload, previous, err := enc.DecryptWithRotation[StoredCalendar](connection.Credentials)
if err != nil {
	return err
}
```

Check `err` first: `previous` can be true even if later JSON decoding fails.
Apply the version and identity checks from [Binding Records](#binding-records)
before using or rewriting `payload`. If `previous` is false, no key replacement
is needed. Otherwise encrypt the verified payload with the current driver:

```go
fresh, err := enc.Encrypt(payload)
if err != nil {
	return err
}
```

Do not overwrite a credential changed by another request. Use the original
ciphertext as a compare-and-swap condition alongside the authorized target.
The update reuses `CredentialUpdate` from the write example:

```go
input := CredentialUpdate{Credentials: fresh}
affected, err := db.Exec(ctx, query.Table("calendar_connections", func(statement *query.Builder) {
	statement.Where("id", connection.ID)
	statement.Where("account_id", accountID)
	statement.Where("credentials", connection.Credentials)
	statement.Update(query.Values(input))
}))
if err != nil {
	return err
}
if affected == 0 {
	return errors.New("calendar credentials changed; reload before rotating")
}
```

These replacement steps run only for a successfully decoded, identity-checked
payload with `previous == true`. A failed write leaves rotation incomplete.
Alternatively, lock the row within your [transaction](../database/transactions.md)
before reading and replacing it.

Read-time replacement gradually migrates active rows. To finish, run a bounded
application-owned batch job over every relevant row, handle conflicts by
reloading, and record completion without logging payloads. Already-migrated
rows report `previous == false`, so the key-replacement check is repeatable.
Do not retire old keys until no live writer or stored value needs them, and
retain keys required by backups.

For opaque byte payloads, `DecryptRawWithRotation` and `EncryptRaw` preserve
the byte format without JSON decoding. Application format validation still
belongs before any replacement.

## Keeping Compatibility

Persist complete ciphertext bytes; do not split or reinterpret the nonce and
tag in application code. The raw format carries no cipher, key identifier, AAD,
or schema version. Keep that configuration recoverable for old values and
backups. Changing AAD requires the original context on the old driver and the
new context on the new driver; changing a purpose string without retaining
the old decoder makes existing values fail authentication.

The version inside `StoredCalendar` is an application field, not an Arkoren
version protocol. Reject unsupported versions and preserve the original bytes.
If incompatible payload shapes need separate decoders, define an explicit
application format and validate its discriminator rather than guessing from
decryption failures. Unknown external discriminators must not select arbitrary
keys or cause plaintext fallback.

A successful JSON decode does not prove that a schema change preserved meaning:
renamed or absent fields can still produce zero values. Validate required fields
and version semantics before use or re-encryption. Key replacement and schema
migration are separate decisions; verify both before rewriting durable data.

For typed and raw rotation operations, see the [`crypto` API reference](https://pkg.go.dev/arkoren.dev/core/service/crypto).
