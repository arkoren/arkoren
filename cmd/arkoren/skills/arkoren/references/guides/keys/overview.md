---
title: Application Keys
nav: Overview
description: Choose, provision, and load typed shared, signing, and exchange keys for your application.
order: 10
---

# Application Keys

## Introduction

Encryption and signatures must keep working across restarts and deployments.
Generate persistent key material once, protect it in your secret store, and load
the same material on every replica. The `key` package validates material before
you supply it to encryption drivers, link signers, cursor codecs, or P-256
identity consumers. It also provides X25519 keys for integration with an existing
key-agreement protocol. It does not choose configuration sources or rotate keys.

## Choosing the type

Run `go get arkoren.dev/foundation/key` in your application's module.
Choose the immutable value whose role matches your application. Generate separate
material for encryption, links, cursors, and each independent purpose; sharing a
type does not make sharing a key safe. Keep signing and exchange identities separate.

### Shared keys

Use `key.Shared` when an operation needs a symmetric secret: the same secret
protects and checks the data. It privately owns exactly 32 bytes, independently
of the input buffer. `key.GenerateShared()` creates cryptographically random
material; importing bytes validates their length, not their entropy. Never use a
password as a key.

AES-256-GCM and ChaCha20-Poly1305 use this material for encryption and decryption.
Link signers and cursor codecs instead derive purpose-bound keys with HKDF-SHA256
and use HMAC-SHA256 to detect changes. These are shared-secret checks, not
asymmetric signatures: `Shared` has no public counterpart that you can distribute
for verification without revealing the secret.

### Signing keys

Use `key.Signing` when a private key must sign data that a public key can verify.
It privately holds a validated ECDSA P-256 scalar, the secret number defining the
identity. Its native private and matching public key exports are derived from
that scalar and detached from the immutable value. The public key verifies
signatures; it does not encrypt data, and the private key is not a decryption key.

`key.GenerateSigning()` creates an independent identity suitable for VAPID, which
authenticates your application server to a Web Push service. Apple Sign In also
uses `Signing`, but you must load the key issued by Apple: a locally generated
identity does not establish the required association with your Apple account.

### Exchange keys

Use `key.Exchange` to supply keys to an existing authenticated key-agreement
protocol. It privately owns X25519 private bytes and derives its matching public
key; native exports are detached from the immutable value.
`key.GenerateExchange()` creates an independent exchange identity.

Within the protocol, each party combines its own private key with the peer's
public key to obtain shared material. This agreement neither encrypts data nor
creates signatures, and it does not authenticate the peer. Let the protocol
authenticate peer keys and derive purpose-separated encryption keys rather than
reusing raw agreement output as a key. Follow the
[exchange-key security requirements](#using-exchange-keys); these containers are
not a reason to design your own encryption protocol.

## Provisioning once

Install the CLI with `go install arkoren.dev/cmd/arkoren@latest`. In a protected provisioning
directory, choose the type and a separate file for each purpose:

```sh
arkoren key generate shared --out app-key.key
arkoren key generate shared --out link-key.key
arkoren key generate shared --out cursor-key.key
arkoren key generate signing --out vapid-private.pem
arkoren key generate exchange --out exchange-private.pem
```

Files must not exist, including symlinks. Private output uses mode `0600`; the
CLI does not create parent directories, edit `.env`, or print private contents
unless you explicitly use `--out -`. Protect the directory and its parents from
untrusted writers. Move the files into protected deployment storage, mount them
read-only, and back them up separately from public configuration.

If writing fails after creation, the CLI reports a possibly incomplete file and
leaves it for inspection. Do not replace an established key to recover from a
provisioning failure. See [Key Commands](../cli/keys.md) for validation and public
extraction. Manual alternatives remain in [Encryption Keys](../encryption/keys.md)
and [VAPID Keys](../push/vapid-keys.md).

## Loading at startup

Import `arkoren.dev/foundation/key`. For mounted files, use explicit paths and
handle errors before constructing the consumer. In application startup code:

```go
material, err := key.LoadShared("app-key.key")
if err != nil {
	return err
}
secret, err := key.LoadShared("link-key.key")
if err != nil {
	return err
}
identity, err := key.LoadSigning("vapid-private.pem")
if err != nil {
	return err
}
```

The resulting values are ready for `aescrypto.New(material)`, `link.New(secret)`,
and `webpushpush.New(identity, contact)` respectively. Use a separately loaded
cursor secret with `pagination.NewCursorCodec`. These constructors require typed keys.

For environment variables or secret-store text, call `key.ParseShared(text)`.
It expects padded standard base64 encoding exactly 32 bytes and accepts
surrounding whitespace, but rejects embedded line breaks and malformed encodings.
For signing PEM bytes, use `key.ParseSigning(contents)`: exactly one unencrypted PKCS #8
`PRIVATE KEY` block is accepted, with no extra headers, blocks, or non-whitespace
junk. Reads and encoded inputs are bounded to 64 KiB. Missing or malformed keys
fail startup rather than generating replacement identities.

For an exchange identity, use `key.LoadExchange(path)` or
`key.ParseExchange(contents)` with an X25519 PKCS #8 private key.
`PublicPEM()` on either asymmetric type exports its public key as SPKI PEM;
`Signing.VAPIDPublicKey()` exports the browser-ready base64url public point instead.
Formatting and structured logging redact key values. Mutable exports are detached
copies, but explicit private exports still contain secrets.

The package does not load an implicit `APP_KEY`, search parent directories, or
fetch secrets. Your application chooses the file, environment variable, or
[secret-store reference](../secrets/managing.md). File loaders follow the explicit path;
they do not enforce deployment file permissions or sandbox untrusted paths.

Rotation remains owned by the relevant consumer. Retain prior encryption keys
while stored data and backups need them; retain signing secrets while issued
links/cursors remain valid. VAPID requires subscription migration, not merely
swapping the server's identity. Provider API tokens, webhook secrets, password
hash salts, session identifiers, and TLS certificate provisioning are outside
this package.

## Using exchange keys

An `Exchange` value supplies native X25519 keys to an existing protocol through
`PrivateKey()` and `PublicKey()`. `key.ParseExchangePublic(contents)` reads a peer's
X25519 SPKI `PUBLIC KEY` PEM. Parsing validates the key's format, not who owns it.

> [!WARNING]
> Key agreement alone does not authenticate a peer. An attacker who substitutes
> a public key can establish a different shared secret with you. Authenticate peer
> keys through your protocol, and use its KDF with distinct domain-separation
> contexts for each purpose. Never use raw ECDH output directly as an encryption key.

The package does not provide a handshake, transport, or key-derivation API.
Keep peer authentication and derivation in the protocol that owns them rather
than assembling a new encryption protocol from these key containers.

For all constructors and methods, see the [`key` API reference](https://pkg.go.dev/arkoren.dev/foundation/key).
