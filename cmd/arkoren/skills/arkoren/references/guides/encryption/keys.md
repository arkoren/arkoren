---
title: Encryption Keys
description: Keep current and previous encryption keys in a ring and rotate writers without losing access to existing ciphertext.
order: 30
summary:
  - Load durable keys without replacing missing material or exposing it in diagnostics.
  - Configure a key ring and roll out new writers before retiring old keys.
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

Load the persistent keys from a protected mounted file or your configured
[secret store](../secrets/overview.md). Both encryption drivers take a
`key.Ring[key.Shared]`: an ordered set of keys where the first is current and
encrypts, and later keys are previous and only decrypt. Each key is exactly 32
bytes. With `key` imported from `arkoren.dev/foundation/key`, parse the ring
from its text form, newest key first:

```go
ring, err := key.ParseRing[key.Shared](encoded)
if err != nil {
	return err
}
```

A ring with one key is the ordinary case before any rotation. Build one in code
with `key.NewRing(current, previous...)`, which rejects zero keys and duplicates.
Fail startup on a missing or malformed ring; do not print its text or generate
substitute material. Retrieve `encoded` through your configured secret facade or a
protected file; no environment variable or secret name is implicit. A ring
formats as a redacted marker, and `ring.Encode()` is the only way to export it.
See [Drivers](drivers.md) for constructing the selected cipher and owning its
cleanup.

Keep key backups separate from ciphertext backups, with controlled access and
a tested recovery procedure. Retain any key and context needed by backups for
their entire retention period. A ciphertext backup without its key is not a
recoverable backup.

## Selecting Keys

A driver encrypts with its ring's current key and decrypts with the current key
first, then each previous key in order. Rotating one cipher's key is therefore a
ring change, not a second driver. Arkoren does not fetch missing keys, watch
secret changes, or switch the current key at runtime: rebuild the driver to
change the ring.

Import `aescrypto` from `arkoren.dev/driver/aes/aescrypto` and `crypto` from
`arkoren.dev/core/service/crypto`. Here `newKey` and `oldKey` are validated
`key.Shared` values loaded from separate protected entries. List the new key
first:

```go
ring, err := key.NewRing(newKey, oldKey)
driver, err := aescrypto.New(ring, func(encrypter *aescrypto.Builder) {
	encrypter.AdditionalData([]byte("calendar-credentials:v1"))
})
enc, err := crypto.New(driver)
```

Handle each construction error and register cleanup immediately after the
successful acquisition. A driver's `Decrypt` reports `previous=true` when a
previous key authenticated the ciphertext, and the facade's `DecryptWithRotation`
passes that on, meaning the value should be re-encrypted with the current key.

The facade's own `Previous` drivers remain for moving between ciphers or
parameters, such as from AES-GCM to ChaCha20-Poly1305. They are tried after the
current driver, in configured order, and `DecryptWithRotation` also reports
`true` when one of them succeeds. The facade copies that driver list, not the
driver objects, and is immutable after construction.

Stop every driver only after all consumers finish. The facade does not own their
lifecycle. Stopping the current driver alone does not prevent fallback
decryption through an active previous facade driver.

## Rotating Keys

Rotation changes the key used for future writes. It does not rewrite stored
values. During a rolling deployment, old instances must be able to read new
ciphertext before any writer switches keys:

1. Provision the new key under a separate protected name; retain the old key.
2. Deploy all readers with a ring that lists the old key first and the new key
   as previous. Wait until every reader can accept either key.
3. Deploy the ring with the new key first and the old key as previous. Readers
   from the preceding deployment can already read new writes.
4. [Re-encrypt stored values](storing.md#re-encrypting-values), using the
   `previous` result to find values that still depend on the old key, and verify
   that no live writer or stored value still needs it.
5. Remove the old key from the ring. Retain it securely for backups that still
   need it.

The previous position is a decryption role, so step 2 can contain a newly
provisioned key. Deployments must preserve the exact cipher and AAD for each
driver. Changing from AES-GCM to ChaCha20-Poly1305 is not a ring change: keep the
old cipher as a facade `Previous` driver for old reads, even if key material
stays available.

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
