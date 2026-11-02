---
title: Application Keys
nav: Overview
description: Choose typed application keys, validate stored material, and separate public output from private exports.
order: 10
summary:
  - Choose independent shared secrets, P-256 signing identities, or X25519 exchange identities for each purpose.
  - Generate material explicitly and load configured values with strict format and error checks.
  - Distinguish public exports from private credentials and understand detached ownership and redaction limits.
  - Keep peer authentication and key rotation with the protocols and consumers that own them.
---

# Application Keys

Your application's encrypted records and signed values must survive restarts.
Provision a key once, protect it, and load the intended material wherever that
purpose runs. The `key` package turns stored bytes into immutable, validated key
values; it does not choose your configuration source or manage rotation.

## Choose Material

Choose the type required by the consumer, not the file extension. A shared
secret protects data using the same secret on both sides. A signing identity
instead separates the private key that signs from the public key that verifies.

| Type | Material | Use |
| --- | --- | --- |
| `key.Shared` | Exactly 32 secret bytes | Symmetric encryption or shared-secret signing, with no public counterpart. |
| `key.Signing` | ECDSA P-256 private identity | Signatures verified with the matching public key. |
| `key.Exchange` | X25519 private identity | Key agreement inside an existing authenticated protocol. |

Use independent material for each purpose. For example, give encrypted records,
[signed links](../http-capabilities/secure-links.md), and
[pagination cursors](../queries/pagination.md) separate `Shared` values.
AES-256-GCM and ChaCha20-Poly1305 consume `Shared` for encryption; link signers and
cursor codecs use it for HMAC-based checks, not public-key signatures.

`Signing` is not an encryption key, and `Exchange` is neither a signer nor an
encryption protocol. These types do not accept arbitrary algorithms: an RSA or
Ed25519 key is not a `Signing` identity, and a P-256 ECDH key is not an `Exchange`
identity. For Web Push, follow [VAPID Keys](../push/vapid-keys.md). For Apple Sign
In, load the Apple-issued P-256 key; local generation cannot establish provider
provenance. Provider API tokens, webhook secrets, password-hash salts, session
identifiers, and [TLS certificates](../http-networking/certificates.md) are
different inputs, not additional key types.

## Generate Keys

Use [Key Commands](../cli/keys.md) to provision protected files, validate them,
and extract public components. That guide owns command syntax, output permissions,
and recovery from incomplete writes.

Add the package from your application's module directory:

```sh
go get arkoren.dev/foundation/key
```

Import `arkoren.dev/foundation/key`. If you write your own provisioning tool,
use the package's generators. This disposable in-memory example creates a shared
key without displaying or persisting its secret:

```go
material, err := key.GenerateShared()
```

Generation uses `crypto/rand` and does not store the result. `GenerateSigning`
creates an independent P-256 identity; `GenerateExchange` creates an independent
X25519 identity. Provision production material outside application startup and
store it in protected deployment storage with a protected backup. Never generate
a replacement to hide a missing startup key: existing data or identities still
depend on the original.

## Load Configuration

Your application selects configuration sources and resolves
[secret-store values](../secrets/managing.md). The package does not read an
implicit `APP_KEY`, fetch secrets, or search parent directories. For example,
an application's configuration layer can supply these named inputs:

```go
type KeyConfig struct {
	EncryptionBase64 string
	SigningFile      string
}
```

With an existing `config KeyConfig`, parse the encryption secret already read
from protected storage:

```go
material, err := key.ParseShared(config.EncryptionBase64)
```

Handle `err` before constructing an encryption consumer. On success, `material`
is a `key.Shared`; continue with [Encryption Keys](../encryption/keys.md) for
driver wiring and encrypted-data use. `ParseShared` requires padded standard
base64 decoding to exactly 32 bytes. It accepts surrounding whitespace but
rejects embedded line breaks, noncanonical padding bits, and malformed encoding.
Base64 is an encoding, not encryption. `NewShared(raw)` imports exactly 32 raw
bytes instead; neither operation proves entropy. Do not use a password or a
predictable 32-byte value as a key.

For a mounted signing file, restrict read access to the application and trusted
operators before loading it. Keep the file and its parent directories under
trusted control: loaders follow the supplied path, including symlinks, and do
not check deployment permissions or sandbox untrusted paths.

```go
identity, err := key.LoadSigning(config.SigningFile)
```

On success, `identity` is a `key.Signing`. Loading reads without modifying the
file. `LoadShared(path)` reads base64 text; `LoadExchange(path)` reads X25519
private PEM. If configuration already contains PEM bytes, use
`ParseSigning(contents)` or `ParseExchange(contents)` instead.

Both private PEM parsers require exactly one unencrypted PKCS #8 `PRIVATE KEY`
block with the matching algorithm. They accept surrounding whitespace, but
reject extra blocks, PEM headers, non-whitespace junk, trailing DER bytes, other
curves, and alternate forms such as SEC 1 `EC PRIVATE KEY`. Encoded inputs and
file reads are limited to 64 KiB, including surrounding whitespace.

For existing native keys, `NewSigning(private)` accepts an `*ecdsa.PrivateKey`
from `crypto/ecdsa`, validates P-256 and public/private consistency, and copies
the material. `NewExchange(private)` accepts an X25519 `*ecdh.PrivateKey` from
`crypto/ecdh`. Neither constructor retains the caller's mutable key object.

Malformed, unsupported, and oversized material returns `key.ErrInvalid`; check
it with `errors.Is` from the standard-library `errors` package. File operations
preserve OS error causes, so a missing file can match `os.ErrNotExist` instead.
Validation errors do not include key contents or input-derived parser diagnostics.
Do not log the source configuration when reporting a failure.

## Export Material

Before exporting a key passed in by another component, check its `Err()` result.
All three types have invalid zero values; `Err()` returns `key.ErrInvalid` for
them. An empty export is not a replacement for validation: zero shared values
export empty bytes or text, and zero asymmetric identities export nil native
keys or PEM bytes. A zero signing identity's `VAPIDPublicKey()` is empty text.

After successfully loading the signing `identity`, obtain only its public
component for a verifier:

```go
public := identity.PublicPEM()
```

`public` is SPKI `PUBLIC KEY` PEM without private material. `PublicKey()` returns
a native public key instead. Both asymmetric types support these exports.
`Signing.VAPIDPublicKey()` is a different representation: the uncompressed P-256
public point in unpadded base64url, not PEM or DER. Use it only where the
[Web Push setup](../push/vapid-keys.md#publishing-public-keys) requires it.

> [!WARNING]
> `Shared.Bytes()` and `Shared.Base64()` expose the shared secret. `PEM()` on
> either asymmetric type exports unencrypted PKCS #8 private material, and
> `PrivateKey()` exposes a native private key. Never send these exports to logs,
> browser responses, or public configuration. Persist them only through your
> protected provisioning path; an encoding does not protect a secret.

Formatting key values with `fmt` and logging them through `slog` produces fixed
redaction markers. That is not a persistence format or a general serialization
guarantee. Do not serialize an identity to publish a public key; explicitly
export its public component. Strings or native objects obtained through private
exports do not inherit the key value's redaction behavior.

Inputs and mutable exports are detached from the immutable value. Modifying an
exported slice or native key does not change the original or later exports.
This ownership rule does not promise to erase secret copies from process memory.

## Using Exchange Keys

Use an `Exchange` identity only when integrating a protocol that already defines
peer authentication and key derivation. `PrivateKey()` and `PublicKey()` supply
native X25519 keys; `ParseExchangePublic(contents)` imports a peer's X25519
SPKI `PUBLIC KEY` PEM under the same strict envelope and size rules as private
PEM. Parsing checks representation, not who owns the key. It also does not reject
every low-order point; native ECDH rejects those inputs when agreement is attempted.

> [!WARNING]
> An attacker who substitutes a peer public key can establish a different shared
> secret with you. Authenticate peer keys through your protocol and apply its
> key-derivation function with distinct purpose contexts. Never use raw ECDH
> output directly as an encryption key.

The package supplies no handshake, transport, or key-derivation API. Keep these
responsibilities in the existing protocol rather than designing a new encryption
protocol from the key containers.

## Rotate Keys

The application owns persistence, deployment, and rotation. Loading a new file
does not update an already constructed consumer. Keep the intended material
consistent across replicas serving the same purpose, and coordinate replacement
with the consumer's compatibility rules.

Retain previous encryption keys while stored values or backups need them; follow
[encryption rotation](../encryption/keys.md#rotating-keys) for the rollout and
re-encryption sequence. Replacing a link or cursor secret can invalidate issued
values; plan around their lifetime and the consumer's supported key handling.
VAPID replacement requires [subscription migration](../push/vapid-keys.md#rotating-identity),
not merely a different private key on the server.

For constructors, formats, and exports, see the
[`key` API reference](https://pkg.go.dev/arkoren.dev/foundation/key).
