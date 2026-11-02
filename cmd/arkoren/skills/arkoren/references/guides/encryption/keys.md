---
title: Encryption Keys
description: Keep current and previous encryption keys available and rotate writers without losing access to existing ciphertext.
order: 30
summary:
  - Load durable keys without replacing missing material or exposing it in diagnostics.
  - Configure current and previous drivers and roll out new writers before retiring old keys.
  - Handle missing historical keys and application versions without assuming ciphertext identifies them.
---

# Encryption Keys

Encrypted data must remain readable after a restart. Provision a persistent key
once and load it at startup; generating a replacement whenever loading fails
makes old data unreadable. Anyone who obtains the key can decrypt values made
with it when the cipher and required authenticated context are known.

Use independent keys for different purposes. Separate keys limit the data
exposed by one leaked key; purpose AAD prevents cross-purpose substitution but
does not contain a key compromise. Generic key types and encodings belong in
[Keys Overview](../keys/overview.md), and protected provisioning commands belong
in [CLI Keys](../cli/keys.md).

## Loading Keys

Load the persistent key from a protected mounted file or your configured
[secret store](../secrets/overview.md). Both encryption drivers take a
`key.Shared`, which validates exactly 32 bytes. With `key` imported from
`arkoren.dev/foundation/key`, a mounted file can supply the material:

```go
material, err := key.LoadShared("/run/secrets/calendar-key.key")
if err != nil {
	return err
}
```

Fail startup on a missing, unreadable, or malformed key; do not print the file
contents or generate substitute material. For secret-store text, use
`key.ParseShared(encoded)` after successfully retrieving the value through your
configured secret facade. No environment variable or secret name is implicit.
See [Drivers](drivers.md) for constructing the selected cipher and owning its
cleanup.

Keep key backups separate from ciphertext backups, with controlled access and
a tested recovery procedure. Retain any key and context needed by backups for
their entire retention period. A ciphertext backup without its key is not a
recoverable backup.

## Selecting Keys

An encrypter has one **current driver**, used for every encryption, and an
ordered list of **previous drivers**, used only as decryption fallbacks. This
is a configured driver ring, not a remotely managed keyring: Arkoren does not
fetch missing keys, watch secret changes, or switch the current key at runtime.

Import `aescrypto` from `arkoren.dev/driver/aes/aescrypto` and `crypto` from
`arkoren.dev/core/service/crypto`. Here `newKey` and `oldKey` are validated
`key.Shared` values loaded from separate protected entries. Build both drivers
with the context used for their ciphertext:

```go
purpose := func(encrypter *aescrypto.Builder) {
	encrypter.AdditionalData([]byte("calendar-credentials:v1"))
}
current, err := aescrypto.New(newKey, purpose)
previous, err := aescrypto.New(oldKey, purpose)
```

Handle each construction error and register cleanup immediately after each
successful acquisition. Then publish the facade with the old driver available
for reads:

```go
enc, err := crypto.New(current, func(encrypter *crypto.Builder) {
	encrypter.Previous(previous)
})
```

Decryption tries the current driver first, then previous drivers in configured
order; repeated `Previous` calls append to that order. New values always use
`current`. The facade copies the driver list, not the driver objects, and is
immutable after construction. Rebuild it to change the ring.

Stop every current and previous driver only after all consumers finish. The
facade does not own their lifecycle. Stopping the current driver alone does
not prevent fallback decryption through an active previous driver.

## Rotating Keys

Rotation changes the key used for future writes. It does not rewrite stored
values. During a rolling deployment, old instances must be able to read new
ciphertext before any writer switches keys:

1. Provision the new key under a separate protected name; retain the old key.
2. Deploy all readers with the old driver current and the new driver in their
   previous list. Wait until every reader can accept either key.
3. Deploy the new driver as current and the old driver as previous. Readers
   from the preceding deployment can already read new writes.
4. [Re-encrypt stored values](storing.md#re-encrypting-values) and verify that
   no live writer or stored value still depends on the old driver.
5. Remove the previous driver from active configuration. Retain its key
   securely for backups that still need it.

The previous list is a decryption role, so step 2 can contain a newly provisioned
key. Deployments must preserve the exact cipher and AAD for each driver. Changing
from AES-GCM to ChaCha20-Poly1305 also requires retaining the old cipher for old
reads, even if key material stays available.

Rotate before 2^32 encryptions per key across all replicas sharing that key.
A time-based schedule alone does not enforce this volume limit. If a key leaks,
coordinate rotation and incident response; re-encryption cannot protect
ciphertext copies already obtained with the leaked key.

## Missing Keys

`crypto.New` rejects a nil current or configured previous driver with
`crypto.ErrInvalidDriver`. It does not know which historical keys your data
requires: omitting an old driver can construct successfully and break old reads.
Check the required key inventory during deployment rather than treating facade
construction as proof that all data is readable.

Ciphertext contains no key identifier. Decryption tries each configured driver;
if every attempt fails, the facade returns the current driver's original error,
not the last fallback error. With active shipped drivers, wrong-key, wrong-AAD,
altered, and truncated input wrap `crypto.ErrInvalidCiphertext`. There is no
separate missing-key sentinel that identifies which key to fetch.

Preserve the stored bytes and restore the required historical key or context
through your recovery process. Never retry an invalid value as plaintext or
overwrite it with an empty value. Avoid logging keys, exported key bytes, or
credential payloads when reporting the failure.

## Ciphertext Versions

The shipped ciphertext format contains a nonce, ciphertext, and authentication
tag, but no cipher name, key ID, AAD, or application schema version. The facade
does not negotiate versions or return an unknown-version error. Retain the
configuration needed to interpret existing values.

If your application adds a format discriminator, reject unknown versions before
selecting a decoder. Do not interpret an unknown version as the current one.
Keep old decoders until their values and backups have migrated; a successful
key fallback is not a schema migration. [Storing Data](storing.md#keeping-compatibility)
explains which metadata needs authentication and how to preserve compatibility.

For fallback configuration, see the [`crypto` API reference](https://pkg.go.dev/arkoren.dev/core/service/crypto).
