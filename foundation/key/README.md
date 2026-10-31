# Application Keys

`arkoren.dev/foundation/key` provides immutable `Shared`, `Signing`, and
`Exchange` values using only the standard library. Generate material once, store it
securely, and load it explicitly at startup. Encryption drivers, link signers,
cursor codecs, VAPID, and Apple Sign In accept the corresponding typed values.
`Shared` requires exactly 32 bytes. Generate independent secrets for encryption,
links, and cursors; sharing a type does not make sharing a key safe.

Install with `go get arkoren.dev/foundation/key`. From application startup code:

```go
import "arkoren.dev/foundation/key"

material, err := key.LoadShared("app-key.key")
if err != nil {
	return err
}
```

Pass `material` directly to an encryption driver. Provision a file offline with
`arkoren key generate shared --out app-key.key`; it refuses existing paths.
Use `LoadShared` for symmetric material, `LoadSigning` for ECDSA P-256 PKCS #8
PEM, and `LoadExchange` for X25519 PKCS #8 PEM.
Parse functions accept secret-store contents; New functions import raw/native
material. Exports are detached; formatting and structured logging redact secrets.

Asymmetric `PublicPEM()` exports SPKI PEM; `Signing.VAPIDPublicKey()` exports
the browser-ready base64url point. Apple Sign In requires Apple's issued key,
not a locally generated replacement. Keep signing and exchange keys separate.

Exchange keys integrate with an existing authenticated protocol. They do not
authenticate peers or implement a handshake or derivation API. Authenticate peer
keys and use your protocol's KDF with purpose-specific domain separation; never
pass raw ECDH output directly to an encryption driver.

See [Application Keys](../../cmd/arkoren/skills/arkoren/references/guides/keys/overview.md)
and the [API reference](https://pkg.go.dev/arkoren.dev/foundation/key).
