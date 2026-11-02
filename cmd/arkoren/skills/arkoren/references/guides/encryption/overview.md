---
title: Encryption Overview
nav: Overview
description: Encrypt recoverable application values and reject ciphertext that cannot be authenticated or decoded.
order: 10
summary:
  - Choose encryption for recoverable values without confusing integrity with access authorization.
  - Encrypt typed values as JSON or preserve existing bytes with raw operations.
  - Reject authentication failures and distinguish them from decoding and lifecycle errors.
---

# Encryption Overview

A calendar integration needs to keep an access token and recover it when sending
requests. Encryption lets you store that token as **ciphertext**, bytes that
require the secret key to recover the original value. Arkoren's
`crypto.Encrypter` also checks integrity: changed ciphertext fails authentication
instead of becoming trusted application data.

## Choosing Encryption

Use encryption when your application needs the original value back. Use
[password hashing](../hashing/overview.md) for login passwords, which you only
need to verify. A [secret store](../secrets/overview.md) protects the encryption
key outside your application data; it does not replace encryption of that data.
If a value may remain readable but must resist changes, consider a signature,
as used by [secure links](../http-capabilities/secure-links.md).

Ciphertext authentication does not authorize the caller, establish freshness,
or prevent replay. Apply [authorization](../authorization/overview.md) before
exposing a decrypt path. A compromised application with access to its keys can
still read the values it protects.

## Encrypting Values

Assume `enc` is a configured `*crypto.Encrypter`, from
`arkoren.dev/core/service/crypto`. [Drivers](drivers.md) owns construction;
[Encryption Keys](keys.md) owns persistent keys and rotation. Focused snippets
keep error results visible and omit routine propagation; handle each error
before using its result.

After your calendar callback has validated the provider response, keep the
recoverable credentials in a named application type:

```go
type CalendarCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}
```

With `credentials` holding that value, encrypt it before handing it to storage:

```go
sealed, err := enc.Encrypt(credentials)
```

`Encrypt` JSON-encodes the value, then encrypts those bytes with the current
driver. Encoding can fail before encryption happens. On success, `sealed` is
binary ciphertext, not JSON or text. Each encryption uses a fresh random nonce,
so encrypting the same credentials again produces a different result.

For an already-encoded payload named `payload`, use `EncryptRaw(payload)`
instead. It skips JSON encoding. Neither raw operation changes its input, and
its returned bytes do not alias the input.

## Reading Values

After selecting a stored connection the caller may access, decode its
ciphertext into the same application shape:

```go
credentials, err := enc.Decrypt[CalendarCredentials](sealed)
if err != nil {
	return err
}
```

Only use `credentials` after the error check. Decryption authenticates the
ciphertext before JSON decoding. A successful result restores the credential
fields; it does not validate provider token expiry or permission to use them.

Match raw encryption with raw decryption:

```go
payload, err := enc.DecryptRaw(sealedPayload)
```

Here `sealedPayload` was produced by `EncryptRaw`. `DecryptRaw` returns the
authenticated bytes without interpreting their format. Keep the encoding and
application schema compatible for as long as stored values exist; see
[Storing Data](storing.md).

## Handling Failures

With either shipped driver, truncated, altered, wrong-key, or wrong-context
ciphertext produces an error wrapping `crypto.ErrInvalidCiphertext`. The error
does not distinguish tampering from a missing historical key. If previous
drivers are configured, the facade tries them before returning a failure.

Import standard-library `errors` to classify an invalid stored credential
without including its contents in a user-facing error:

```go
if errors.Is(err, crypto.ErrInvalidCiphertext) {
	return errors.New("calendar credentials are unavailable; reconnect the calendar")
}
```

Reject invalid client-supplied ciphertext. For stored application data, investigate
key availability, cipher choice, authenticated context, and corruption rather
than silently replacing it with empty credentials. Do not log keys, plaintext,
or whole ciphertext values while investigating; inspect native error causes
before exporting them to logs.

Authenticated bytes can still fail JSON decoding into the requested type.
That is a decoding error, not `ErrInvalidCiphertext`; keep the original stored
value for recovery. A stopped driver returns `crypto.ErrEncrypterClosed`, which
indicates a lifecycle error rather than invalid user data. A usable previous
driver can still decrypt even if the current driver has stopped; stopping one
driver does not disable the entire facade.

For all operations, see the [`crypto` API reference](https://pkg.go.dev/arkoren.dev/core/service/crypto).
