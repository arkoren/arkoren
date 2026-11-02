---
title: Encryption Drivers
nav: Drivers
description: Choose and configure a local authenticated cipher, including purpose binding and cleanup ownership.
order: 20
summary:
  - Compare the two local ciphers and their shared key, nonce, and authentication limits.
  - Construct a driver and facade, bind a stable purpose, and release owned resources safely.
  - Observe complete facade operations without exporting encrypted values in metric labels.
---

# Encryption Drivers

Choose AES-GCM for a server with hardware AES support. Consider
ChaCha20-Poly1305 when AES acceleration is unavailable; benchmark your workload
if performance determines the choice. Both run in your process without a cloud
encryption service and require a persistent 32-byte `key.Shared` value.
[Encryption Keys](keys.md) explains loading and rotating that material.

## Compatibility matrix

This compares Arkoren's shipped drivers, not every cipher mode available from
their underlying libraries. Facade metrics apply only when operations go through
`crypto.Encrypter`.

| Driver | Authenticated encryption | Purpose AAD | 32-byte keys | 16- or 24-byte keys | 192-bit nonces | Facade metrics |
| --- | --- | --- | --- | --- | --- | --- |
| [AES-GCM](#aes-gcm) | Yes | Yes | Yes | No | No | Yes |
| [ChaCha20-Poly1305](#chacha20-poly1305) | Yes | Yes | Yes | No | No | Yes |

Both use a random 96-bit nonce and add 28 bytes to the raw plaintext length.
Rotate before 2^32 encryptions under one key across all replicas. Neither
driver exposes XChaCha20 or a caller-supplied nonce. Changing cipher requires
keeping the original driver available to decrypt old values; see
[Rotating Keys](keys.md#rotating-keys).

## AES-GCM

The AES driver uses AES-256 in Galois/Counter Mode. It needs no account,
credentials beyond the encryption key, network connection, or `Start` call.

### Installing AES

From your application's module directory, install the driver:

```sh
go get arkoren.dev/driver/aes
```

### Creating Encrypters

Import `crypto` from `arkoren.dev/core/service/crypto` and `aescrypto` from
`arkoren.dev/driver/aes/aescrypto`. With validated material named `material`,
construct the driver and then its facade:

```go
driver, err := aescrypto.New(material)
enc, err := crypto.New(driver)
```

These are focused fragments; handle each construction error before continuing.
The driver builds its cipher locally and rejects a zero `key.Shared` with
`key.ErrInvalid`. Key import rejects lengths other than 32 bytes, so this is
not an AES-128 or AES-192 interface. `crypto.New` rejects nil current or previous
drivers with `crypto.ErrInvalidDriver`.

Register `driver.Stop` with [application cleanup](../application/lifecycle-and-ownership.md)
immediately after successful acquisition. Keep the constructor's pointer rather
than copying the driver, which contains synchronization state. The facade
shares that driver and does not stop it for you. Use `enc` for the ordinary
[typed encryption task](overview.md#encrypting-values).

### Binding Context

**Additional authenticated data** (AAD) binds ciphertext to known context
without encrypting that context. For a calendar-only driver, use a stable
purpose string when constructing it instead of the unconfigured constructor:

```go
driver, err := aescrypto.New(material, func(encrypter *aescrypto.Builder) {
	encrypter.AdditionalData([]byte("calendar-credentials:v1"))
})
```

No AAD is bound by default. `AdditionalData` replaces and copies the bytes;
nil or empty clears them. Construction copies the completed setting again.
The exact bytes must match during decryption. They are not included in the
ciphertext and do not increase its size. The setting applies to every operation
on this driver, not to an individual call; [Storing Data](storing.md#binding-records)
explains the record-binding boundary. There are no network, retry, or timeout
settings.

### Handling Shutdown

Operations support concurrent use. `Stop` waits for in-flight operations, clears
the driver's owned raw key copy, and releases its cipher reference. Cancellation
of the cleanup context does not interrupt that wait or skip release. Repeated
stops are safe; later operations return `crypto.ErrEncrypterClosed`.

This does not clear the caller's immutable key, exported bytes, plaintext, or
all internal cipher state. Treat process memory as sensitive. The constructor
pointer formats as `[REDACTED AES driver]` in ordinary `fmt` diagnostics, but
arbitrary serialization, reflection, copied values, and explicit exports are
not a sanitization boundary.

Invalid ciphertext wraps `crypto.ErrInvalidCiphertext`; input shorter than the
12-byte nonce additionally wraps `aescrypto.ErrMismatchedAESNonceSize`.
No native client or companion encryption adapter is exposed.

For driver options, see the [`aescrypto` API reference](https://pkg.go.dev/arkoren.dev/driver/aes/aescrypto).

## ChaCha20-Poly1305

The ChaCha20 driver uses the 96-bit nonce variant of ChaCha20-Poly1305, not
XChaCha20. Choose it when software performance without AES acceleration suits
your deployment. It needs no external service or `Start` call.

### Installing ChaCha20

From your application's module directory, install the driver:

```sh
go get arkoren.dev/driver/chacha20
```

### Creating Encrypters

Import `crypto` from `arkoren.dev/core/service/crypto` and `chacha20crypto` from
`arkoren.dev/driver/chacha20/chacha20crypto`. With persistent `key.Shared`
material named `material`, construct the driver and facade:

```go
driver, err := chacha20crypto.New(material)
enc, err := crypto.New(driver)
```

Construction is local. Handle each error before proceeding: a zero key returns
`key.ErrInvalid`, and key import rejects any length other than 32 bytes.
Register `driver.Stop` with [application cleanup](../application/lifecycle-and-ownership.md)
after acquiring it. Retain the pointer, not a copy, and stop it only after its
consumers finish. The facade does not own cleanup. Use the shared
[typed encryption task](overview.md#encrypting-values) with `enc`.

### Binding Context

To restrict this driver to calendar credentials, use the alternative constructor:

```go
driver, err := chacha20crypto.New(material, func(encrypter *chacha20crypto.Builder) {
	encrypter.AdditionalData([]byte("calendar-credentials:v1"))
})
```

The exact AAD bytes must match on decryption. No AAD is the default; repeated
settings replace and copy the bytes, nil or empty clears them, and construction
detaches the completed setting. Context is fixed for the driver's lifetime and
is not stored in its output. There are no timeout, retry, or network settings.

### Handling Shutdown

Operations are safe for concurrent use. `Stop` waits for in-flight operations
even with a canceled context, clears its owned raw key copy, and releases the
cipher reference. It is repeatable; subsequent calls to encrypt or decrypt
return `crypto.ErrEncrypterClosed`. The caller's key and all internal cipher
copies are not erased.

The constructor pointer formats as `[REDACTED ChaCha20 driver]` in ordinary
`fmt` diagnostics. This does not sanitize arbitrary serialization, reflection,
copied values, explicit key exports, or application data.

Authentication failures wrap `crypto.ErrInvalidCiphertext`. Input shorter than
the 12-byte nonce also wraps `chacha20crypto.ErrMismatchedChaCha20NonceSize`.
No native client or companion encryption adapter is exposed.

For driver options, see the [`chacha20crypto` API reference](https://pkg.go.dev/arkoren.dev/driver/chacha20/chacha20crypto).

## Facade Metrics

To observe logical encryption and decryption calls, pass your configured
[telemetry service](../observability/telemetry.md), named `metrics`, to the facade:

```go
enc, err := crypto.New(driver, func(encrypter *crypto.Builder) {
	encrypter.Telemetry(metrics)
})
```

Without an explicit service, nil follows `telemetry.Default()` per call.
`telemetry.New(nil)` explicitly disables reporting. Counts and durations include
JSON work and all fallback attempts as one final operation, with no traces or
events. A successful previous-key outcome does not mean replacement ciphertext
was persisted. Labels contain no keys, plaintext, ciphertext, or native error
text; application logging still needs its own exposure policy. Direct driver
calls bypass these metrics.

For the facade and metric definitions, see the [`crypto` API reference](https://pkg.go.dev/arkoren.dev/core/service/crypto).
