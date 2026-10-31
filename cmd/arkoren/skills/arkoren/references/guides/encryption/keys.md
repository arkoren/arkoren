---
title: Encryption Keys
nav: Keys
description: Generate encryption keys, load them from a secret store, rotate them without breaking stored data, and use one key per purpose.
order: 20
---

# Encryption Keys

## Introduction

Encrypted data must remain readable after a restart or deployment. That requires
a persistent key: losing it loses access to the data, while anyone who obtains
it can decrypt values made with it, given any required additional data.

Generate the key once, keep it in a secret store, and retain old keys while stored
values or backups still need them. Rotation lets new writes use a new key without
immediately making old values unreadable.

## Generating a key

A newly generated key is 32 random bytes, suitable for either driver. Install
the CLI with `go install arkoren.dev/cmd/arkoren@latest`, then generate once in
a protected provisioning environment, never on server startup:

```sh
arkoren key generate shared --out app-key.key
arkoren key check shared app-key.key
```

Generation creates a base64 text file with mode `0600`, refuses existing files
and symlinks, and never modifies `.env`. Validation confirms format and length,
not entropy or provider connectivity. To intentionally send private material
to a secret-store pipeline, use `--out -`; otherwise secrets stay off stdout.

If the CLI is unavailable, keep this manual fallback. Protect its stdout and
store the result securely; it uses the same padded standard base64 format:

```sh
openssl rand -base64 32
```

For an application-owned provisioning command, import
`arkoren.dev/foundation/key` and use the same generator as the CLI:

```go
material, err := key.GenerateShared()
encoded := material.Base64()
```

> [!WARNING]
> Keep the key in a [secret store](../secrets/overview.md), never in your
> source code, your container image, or a configuration file that is
> committed. Back it up like your database: a database backup without its key
> is unreadable.

## Loading the key at startup

Examples use standard-library `fmt`, `key` from `arkoren.dev/foundation/key`,
`crypto` from `arkoren.dev/core/service/crypto`, and `aescrypto` from
`arkoren.dev/driver/aes/aescrypto`.

Read the key once, while the service starts, and build the encrypter from it.
With a [secret store](../secrets/overview.md) that holds the key as base64
text, use your existing `secrets` store:

```go
encoded, err := secrets.String(ctx, "prod/app-key")
if err != nil {
	return fmt.Errorf("load prod/app-key: %w", err)
}
material, err := key.ParseShared(encoded)
if err != nil {
	return fmt.Errorf("decode prod/app-key: %w", err)
}
driver, err := aescrypto.New(material)
if err != nil {
	return fmt.Errorf("configure prod/app-key: %w", err)
}
```

A mounted file needs only `material, err := key.LoadShared("app-key.key")`;
an application-owned environment variable uses
`key.ParseShared(os.Getenv("APP_KEY"))`. No environment name is implicit.
Drivers require `key.Shared`, not raw bytes. Import existing bytes through
`key.NewShared(raw)`; both AES-256-GCM and ChaCha20 require exactly 32 bytes.
Zero typed values are rejected at construction.

A missing or malformed key stops startup without echoing key contents.
Key values are immutable; explicit byte exports are detached copies. The driver's
`Stop` waits for active operations and zeroes only its owned raw key copy, even
with an ended cleanup context. It does not clear your `key.Shared` value, exported
bytes, or every internal cipher copy. Go's cipher keeps key material that user
code cannot clear, so treat process memory as sensitive regardless.
The drivers' [diagnostic formatting](drivers.md#aes-gcm) protects their constructor
pointers in ordinary `fmt` diagnostics, not arbitrary serialization, reflection,
invalid verbs, native objects, or explicit key exports. Do not use it as permission
to log key material.

## Rotating keys

Keys should change from time to time, and at once if one may have leaked.
Rotation must not break the data already encrypted with the old key, so the
encrypter accepts **previous keys** that it still decrypts with, but never
encrypts with. Here `newKey` and `oldKey` are validated `key.Shared` values
loaded from separate secret-store entries, not base64 strings or raw bytes:

```go
current, err := aescrypto.New(newKey)
previous, err := aescrypto.New(oldKey)

enc, err := crypto.New(current, func(encrypter *crypto.Builder) {
	encrypter.Previous(previous)
})
```

Decryption tries the current key first, then each previous key in order. New
values are always encrypted with the current key. `crypto.New` fails if any
previous driver is nil, so a missing old key stops the service at startup
rather than leaving data silently unreadable.

The encrypter retains the driver objects; it does not stop them for you. Register
`current.Stop` and `previous.Stop` with your application's shutdown hooks so both
drivers stop after their consumers. Keep previous drivers available until no
running consumer needs them.

A rolling rotation needs two deployments so old instances can read values
written by new ones:

1. Generate the new key and store it under a separate secret name. Keep the
   old key as the current encryption key.
2. Deploy every reader with the old key current and the new key in its
   decryption-only previous list. Wait until all readers accept both keys.
3. Deploy with the new key current and the old key previous. Instances from
   step 2 can already decrypt values written with the new key.
4. Re-encrypt stored values with the new key, as described in
    [Storing Encrypted Data](storing.md#re-encrypting-stored-values).
5. Once no writer uses the old key and no stored value needs it, remove it
   from the encrypter. Retain it securely for backups that still need it.

Skipping re-encryption leaves the old key in use for as long as old data
exists. After a leak, re-encryption protects the replacement ciphertext from
the old key. It cannot protect copies an attacker already obtained, or old
backups that still use that key.

## Separating purposes

An encrypted value can be decrypted anywhere the same key is used. If your
service encrypts both session cookies and stored bank details with one key, a
value from one place could be presented in the other. Give each purpose its
own key, or bind each driver to its purpose with **additional data**, which is
checked during decryption but not stored in the output:

```go
cookies, err := aescrypto.New(material, func(encrypter *aescrypto.Builder) {
	encrypter.AdditionalData([]byte("cookies"))
})
bank, err := aescrypto.New(material, func(encrypter *aescrypto.Builder) {
	encrypter.AdditionalData([]byte("bank-accounts"))
})
```

A value encrypted with `cookies` fails to decrypt with any other additional
data, even under the same key. The additional data must stay the same for as
long as values encrypted with it exist, and a previous driver for rotation
needs the same additional data as the current one.

`AdditionalData` replaces the context and copies its input immediately; nil or
empty clears it. `New` copies the completed context again, so retaining the draft
cannot change a published driver's authentication context.

Separate keys are the stronger choice for data of very different sensitivity,
because a key that leaks then exposes only one kind of data. Additional data
is the lighter choice for purposes of similar sensitivity within one feature.

## Limits

Both drivers use random 96-bit nonces. Do not exceed 2^32 encryptions (about
four billion) under one key, and rotate before reaching that limit. Count
encryptions across every replica that shares the key; a calendar-only policy
is not enough for a high-volume service.

Values encrypted with one driver cannot be decrypted with the other, even with
the same key, so changing cipher is a rotation like any other: the new driver
is current, the old one previous.

For the options of each driver, see [Encryption Drivers](drivers.md).
For generation, loading, and explicit exports, see the
[`key` API reference](https://pkg.go.dev/arkoren.dev/foundation/key).
