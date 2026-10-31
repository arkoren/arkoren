---
title: Encryption Overview
nav: Overview
description: Encrypt values that must be read back later, decrypt them into typed values, detect tampering, and decide when encryption is the right tool.
order: 10
---

# Encryption Overview

## Introduction

Your service may need to store a connected calendar's access token and read it
back later. Storing it as plaintext exposes it to anyone who can read that
storage. **Encryption** turns the value into ciphertext that requires a secret
key to read, then recovers the original value when your service needs it. The
same approach applies to bank account numbers or cookie contents.

Arkoren's crypto service provides **authenticated encryption**. Besides hiding
the value, it checks the ciphertext's authenticity during decryption. Changed
bytes fail that check instead of producing altered plaintext. This does not
prove that the caller is authorized or that the value has not been replayed.
The service follows the usual Arkoren shape: an `Encrypter`
facade that encrypts typed values, over a driver that implements the cipher.
Two drivers are available, AES-GCM and ChaCha20-Poly1305, and both are
standard, well-reviewed constructions.

This page covers encrypting and decrypting. Where keys come from and how they
rotate is in [Encryption Keys](keys.md), storing and finding encrypted values
in [Storing Encrypted Data](storing.md), and the two ciphers in
[Encryption Drivers](drivers.md).

## When to encrypt

Encryption is the right tool only when you need the original value back.
Three neighboring tools cover the other cases:

| You need to | Use |
| --- | --- |
| Read the value back later | Encryption, on this page |
| Check a password without being able to read it | [Password hashing](../hashing/overview.md) |
| Prove a readable value, such as a link, was not changed | A signature, as [secure links](../http-capabilities/secure-links.md) use |
| Keep a credential out of code and configuration | A [secret store](../secrets/overview.md) |

Passwords are never encrypted: anyone with the key could read them all. And
encryption is not a replacement for access control: whoever can call your
service's decrypt path can read the data, so the
[authorization](../authorization/overview.md) around that path matters as much
as the cipher.

## Creating an encrypter

Examples import `crypto` from `arkoren.dev/core/service/crypto` (not the
standard-library `crypto` package), and `aescrypto` from
`arkoren.dev/driver/aes/aescrypto`. Key loading and generation use
`key` from `arkoren.dev/foundation/key`; error classification uses `errors`.

An encrypter needs a driver, and the driver needs a key. With `material`, a
validated `key.Shared` value holding exactly 32 bytes,
loaded at startup, as [Encryption Keys](keys.md) describes, create the driver
and facade. Construction errors must stop startup:

```go
driver, err := aescrypto.New(material)
enc, err := crypto.New(driver)
```

Unlike most facades, `crypto.New` returns an error, because it checks the
driver. Register the driver's `Stop` with your
[application cleanup](../application/lifecycle-and-ownership.md). It zeroes the driver's
raw key copy when the service shuts down, not the caller's key or every internal
cipher copy. `Stop` waits for in-flight operations even if its cleanup context has
ended; it is not a memory-erasure guarantee. Stop current and previous drivers
only after their consumers finish. The facade retains those drivers but does not
stop them for you. An encrypter is safe to share between requests and never changes
its configured driver chain after construction.

To observe encryption cost and successful previous-key use, set
`Builder.Telemetry` in the trailing `crypto.New` callback using your
[telemetry service](../observability/telemetry.md). If you omit it or set nil,
each call follows `telemetry.Default()`; pass `telemetry.New(nil)` to disable
reporting explicitly. Counts and durations cover the final logical operation,
including JSON failures and every fallback attempt, without creating traces or
events. A successful fallback is not a persisted key rotation. Metrics never
contain keys, plaintext, ciphertext or native error text. The
[crypto API reference](https://pkg.go.dev/arkoren.dev/core/service/crypto) lists metric names and bounded
label values.

Focused examples keep error results visible but omit routine error handling.
Handle those errors before using results; startup must fail if key loading or
encrypter construction fails.

## Encrypting values

`Encrypt` takes any value, encodes it as JSON, and encrypts the result.
`Decrypt` reverses both steps into the type you ask for. Here `creds` is your
application's `Credentials` value:

```go
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

sealed, err := enc.Encrypt(creds)

restored, err := enc.Decrypt[Credentials](sealed)
```

For data that is already bytes, such as a file, `EncryptRaw` and `DecryptRaw`
skip the JSON step. Neither modifies its argument, and the result never shares
memory with it.

Encrypting the same value twice gives different results, because each
encryption uses a fresh random **nonce**. Both results decrypt to the same
value. This hides whether two rows hold the same value, and is why encrypted
values cannot be compared or indexed directly;
[Storing Encrypted Data](storing.md#finding-rows-by-an-encrypted-value) shows
the alternative.

## When decryption fails

Decryption fails with an error wrapping `crypto.ErrInvalidCiphertext` when the
value was modified, truncated, encrypted with another key, or is not an
encrypted value at all. Authentication failure does not distinguish a wrong key
from modified ciphertext. A truncated nonce additionally wraps the selected
driver's nonce-size sentinel. Reject decryption errors before using plaintext:

```go
creds, err := enc.Decrypt[Credentials](sealed)
if err != nil {
	if errors.Is(err, crypto.ErrInvalidCiphertext) {
		return errors.New("calendar credentials are unavailable; reconnect the calendar")
	}
	return err
}
```

Use `creds` only after this check. Authenticated bytes can still fail JSON
decoding into `Credentials`; that returns a decoding error, not
`crypto.ErrInvalidCiphertext`.

For values that come from outside, such as cookies, an invalid ciphertext is
expected now and then, and the usual response is to ignore the value. For
values from your own database, it usually means a key was lost or rotated
wrongly, and it is worth an alert.

A stopped driver returns `crypto.ErrEncrypterClosed` from its raw operations.
Facade encryption uses the current driver, but decryption can still succeed
through a configured previous driver that remains usable. Stopping only the
current driver is therefore not a barrier against all facade decryption. Seeing
`ErrEncrypterClosed` during normal operation means a caller is still using a
stopped driver; finish consumers before releasing their drivers.

## Testing

Tests can use a real driver with a random key. It is fast, and the code under
test runs exactly as in production:

```go
material, err := key.GenerateShared()
driver, err := aescrypto.New(material)
enc, err := crypto.New(driver)
```

A test that checks tampering flips one byte of a sealed value and expects
`crypto.ErrInvalidCiphertext`. To check a driver of your own against the
contract, run the `cryptotest` suite, as described in
[Conformance Suites](../testing/conformance-suites.md).

For every function, see the [`crypto` API reference](https://pkg.go.dev/arkoren.dev/core/service/crypto).
