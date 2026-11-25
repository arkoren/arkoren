# ChaCha20-Poly1305 driver

`arkoren.dev/driver/chacha20/chacha20crypto` implements the core crypto driver.
Construction takes a `key.Ring[key.Shared]`: the first key encrypts and later keys only
decrypt, which makes rotation a configuration change. Each key is exactly 32 bytes; do
not use a fixed example key in production. This driver uses the 96-bit nonce variant, not XChaCha20.

From your application's module directory, install `go get arkoren.dev/driver/chacha20`
and import `chacha20crypto` from `arkoren.dev/driver/chacha20/chacha20crypto`.

To bind ciphertext to a user, set additional authenticated data (AAD). It is
authenticated but not encrypted, and you must supply it again for decryption.
Given a `key.Ring[key.Shared]` value named `ring`, parsed with
`key.ParseRing` from `arkoren.dev/foundation/key`, and existing `app`, compose
the driver with `application` from `arkoren.dev/core/application`:

```go
err := app.Run(ctx, func(compose *application.Composer) {
	driver := compose.Must(chacha20crypto.New(ring, func(encrypter *chacha20crypto.Builder) {
		encrypter.AdditionalData([]byte("user-42"))
	}))
	compose.Cleanup(driver.Stop)
	// Pass driver to your crypto facade here.
})
```

Keys are copied. No callbacks means no AAD. Callbacks run directly once in
order on one fresh draft before ring validation; supplied nil entries panic,
expanded nil/empty slices omit callbacks. AdditionalData returns the same draft
and replaces/copies the context immediately; nil or empty clears it. Publication
copies AAD again, so retained drafts cannot alter a driver. Reusable callback
policies create independent drafts.

Encryption and decryption must use the same AAD. To rotate, put the new key first and
keep the old key after it: `Decrypt` returns `previous=true` for values the old key
opened, so re-encrypt those and drop the old key once none remain. The ciphertext
has no key identifier, so a decryption may try every key in the ring. Rotate keys before about 2^32
encryptions. Stop clears the driver's raw keys, not the caller's or the internal
AEAD key schedule, and later operations fail with crypto.ErrEncrypterClosed.
Stop waits for in-flight operations under its exclusive lock even if its context
has expired; cancellation does not skip key release. Keep the constructor pointer
rather than copying its synchronization state. Pointer formatting emits a fixed
redaction marker without reading the owned key or cipher state.

See the [API reference](https://pkg.go.dev/arkoren.dev/driver/chacha20/chacha20crypto).
