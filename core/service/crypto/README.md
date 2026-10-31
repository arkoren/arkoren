# crypto

`arkoren.dev/core/service/crypto` defines authenticated-encryption contracts and the typed
`Encrypter` facade. Use it for stored tokens or personal data that your service
must recover; use password hashing for passwords instead.

Use a driver from its own module, then wrap it with `crypto.New` for
JSON-encoded values. Use `EncryptRaw` and `DecryptRaw` for bytes.

Install `go get arkoren.dev/core` and a cipher driver such as
`go get arkoren.dev/driver/aes` from your application's module directory.
Load a persistent secret key before constructing the driver and handle
constructor errors before using the encrypter. Never generate a replacement key
on every startup: existing ciphertext would become unreadable.

Follow the [encryption guide](../../../cmd/arkoren/skills/arkoren/references/guides/encryption/overview.md)
for a typed round trip and safe key rotation, or the
[`crypto` API reference](https://pkg.go.dev/arkoren.dev/core/service/crypto)
for all operations and failure behavior.
