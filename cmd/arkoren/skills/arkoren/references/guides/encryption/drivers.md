---
title: Encryption Drivers
nav: Drivers
description: Choose AES-GCM or ChaCha20-Poly1305, install the driver, pass it a key, and bind it to a purpose with additional data.
order: 40
---

# Encryption Drivers

## Introduction

Choose **AES-GCM** for the usual server deployment with hardware AES
instructions. Choose **ChaCha20-Poly1305** when your processor lacks those
instructions, such as some ARM boards; it performs well in pure software.

Both drivers provide authenticated encryption with the same output size: a
random 96-bit nonce, a 128-bit authentication tag, and 28 bytes added to every
value. Both require exactly 32-byte secrets. This choice changes the cipher, not your
responsibility to protect keys and authorize decryption.

Values encrypted with one cannot be decrypted with the other. Moving from one
to the other is a key rotation, with the new driver as the current one and the
old driver as a previous one, as described in
[Encryption Keys](keys.md#rotating-keys). Both drivers accept
[additional data](keys.md#separating-purposes) with `Builder.AdditionalData`, and
both have a `Stop` method that zeroes their copy of the key.

## Compatibility matrix

Compare the cipher variants and key lengths accepted by Arkoren's drivers,
not every mode offered by the underlying cryptography libraries. Additional
data binds all operations on a driver to the same configured purpose.

| Driver | Authenticated encryption | Additional data | Requires `key.Shared` | 16- or 24-byte keys | 32-byte keys | 192-bit nonces | Aggregate facade metrics |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [AES-GCM](#aes-gcm) | Yes | Yes | Yes | No | Yes | No | Yes — Through crypto.Encrypter. |
| [ChaCha20-Poly1305](#chacha20-poly1305) | Yes | Yes | Yes | No | Yes | No | Yes — Through crypto.Encrypter. |

Metrics are shared by both drivers through the facade. Configure
`crypto.Builder.Telemetry` in the trailing `crypto.New` callback using your
[telemetry service](../observability/telemetry.md), not on a driver builder.
Nil follows `telemetry.Default()` per call; `telemetry.New(nil)` disables
reporting explicitly. Direct driver calls emit no facade metrics.

Counts and durations cover each complete facade call, including JSON failures
and all key attempts, without traces or events. Decryption records one final
logical outcome, not one failure per attempted key. Successful fallback does not
mean the caller persisted a rotated value. Keys, plaintext, ciphertext and native
error text never become labels. See the [crypto metric names and label values](https://pkg.go.dev/arkoren.dev/core/service/crypto).

Both drivers generate random 96-bit nonces; the ChaCha20 driver does not expose
XChaCha20. Rotate keys before 2^32 encryptions per key across all instances.
Ciphertexts are not interchangeable between drivers, and decryption requires
the original key and exact additional-data bytes.

## AES-GCM

The AES driver uses AES in Galois/Counter Mode, the most widely used
authenticated cipher. It runs in your process and needs no external service.

### Installation

Run this command from your application's module directory:

```sh
go get arkoren.dev/driver/aes
```

### Connecting

Load a persistent `key.Shared` value named `material` as described in [Encryption Keys](keys.md#loading-the-key-at-startup).
The key package requires exactly 32 bytes, so this driver uses AES-256-GCM.
Other lengths fail during key import; a zero typed value is rejected by the driver:

```go
import (
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/driver/aes/aescrypto"
)

driver, err := aescrypto.New(material)
enc, err := crypto.New(driver)
```

Construction builds the cipher without network I/O. Register `driver.Stop` with
[application cleanup](../application/lifecycle-and-ownership.md) immediately after
acquiring the driver; the hook clears its raw key copy after consumers finish.
Register cleanup for each previous driver too. The internal cipher state can
retain key material until garbage collection; `Stop` is not a memory-erasure guarantee.
It waits for in-flight encryption/decryption before clearing the owned raw key
and releasing the cipher reference. An expired or canceled cleanup context does
not interrupt that wait or skip release. It does not clear the caller's key value.

### Configuration

By default no additional authenticated data is bound. To prevent a ciphertext
from being used for another purpose, add a stable purpose when constructing the
driver:

```go
configure := func(encrypter *aescrypto.Builder) {
	encrypter.AdditionalData([]byte("calendar-credentials:v1"))
}
```

Pass `configure` to `aescrypto.New(material, configure)`. The additional-data bytes are copied;
repeated settings replace them, and nil or empty clears them. Decryption needs
the exact same bytes, including on previous drivers. This setting does not add
bytes to the output. There are no timeout or retry settings for this local cipher.

### Encrypting stored credentials

Use `enc` for the [typed encryption example](overview.md#encrypting-values),
or raw bytes with `EncryptRaw`. Each successful call returns new ciphertext;
store it only after checking the error.

### Behavior and limits

The driver copies the key and builds the cipher once. It is safe for concurrent
use. Its output is nonce, ciphertext, then tag: 28 bytes more than the input.
Rotate before 2^32 encryptions under one key across all replicas. Modified or
wrong-key input wraps `crypto.ErrInvalidCiphertext`; input shorter than the nonce
also wraps `aescrypto.ErrMismatchedAESNonceSize`. After `Stop`, operations fail
with `crypto.ErrEncrypterClosed`. No native client or additional adapter is shipped.

Keep the constructor's `*Driver`; copying its synchronization state is invalid.
Its `fmt.Formatter` method writes `[REDACTED AES driver]` for `%v`, `%+v`, and `%#v`
on that pointer without inspecting key, cipher state, or additional data. This
does not promise sanitization through arbitrary serialization, reflection,
invalid `fmt` verbs, copied values, or native/caller-owned objects. Explicit key
exports, plaintext, and returned error causes still require your logging policy.

### API references

For every option, see the
[`aescrypto` API reference](https://pkg.go.dev/arkoren.dev/driver/aes/aescrypto).

## ChaCha20-Poly1305

The ChaCha20 driver uses ChaCha20-Poly1305 in your process. Choose it when your
processor lacks efficient AES instructions; it needs no external service.

### Installation

Run this command from your application's module directory:

```sh
go get arkoren.dev/driver/chacha20
```

### Connecting

Load a persistent `key.Shared` value named `material` as in [Encryption Keys](keys.md#loading-the-key-at-startup).
The key must be exactly 32 bytes:

```go
import (
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/driver/chacha20/chacha20crypto"
)

driver, err := chacha20crypto.New(material)
enc, err := crypto.New(driver)
```

Construction is local; key import rejects other lengths. Register `driver.Stop`
with [application cleanup](../application/lifecycle-and-ownership.md) immediately
after acquiring it. Stop every current and previous driver after its consumers.
`Stop` waits for in-flight encryption/decryption, then clears its owned raw key
and releases the cipher reference. Cancellation or an expired cleanup context
does not interrupt that wait or skip release. The caller's key is not cleared.

### Configuration

No additional data is bound by default. For purpose separation, pass this
callback to `chacha20crypto.New(material, configure)`:

```go
configure := func(encrypter *chacha20crypto.Builder) {
	encrypter.AdditionalData([]byte("calendar-credentials:v1"))
}
```

The setting replaces and copies the bytes; nil or empty clears it. Use the same
bytes when decrypting and on previous drivers. There are no network, retry, or
timeout settings.

### Encrypting stored credentials

Use `enc` for [typed values](overview.md#encrypting-values) or raw byte encryption.
Handle the returned error before storing or using the result.

### Behavior and limits

The driver uses the standard 96-bit nonce variant, not XChaCha20. Rotate before
2^32 encryptions per key across all instances. Output overhead is 28 bytes and
operations are safe for concurrent use. Authentication failure wraps
`crypto.ErrInvalidCiphertext`; a too-short nonce also wraps
`chacha20crypto.ErrMismatchedChaCha20NonceSize`. `Stop` clears the driver's raw
key copy, not all internal cipher state; later calls fail with
`crypto.ErrEncrypterClosed`. No native client or additional adapter is shipped.

Keep the constructor's `*Driver`, not a copy of its synchronization state. Its
`fmt.Formatter` method writes `[REDACTED ChaCha20 driver]` for `%v`, `%+v`, and `%#v`
on that pointer without inspecting owned key, cipher state, or additional data.
No sanitization guarantee is made for arbitrary serialization, reflection,
invalid `fmt` verbs, copied values, or native/caller-owned objects. Explicit
exports and application data remain sensitive; formatting is not a
memory-erasure guarantee.

### API references

For every option, see
the [`chacha20crypto` API reference](https://pkg.go.dev/arkoren.dev/driver/chacha20/chacha20crypto).

For the encrypter itself, see the [`crypto` API reference](https://pkg.go.dev/arkoren.dev/core/service/crypto).
