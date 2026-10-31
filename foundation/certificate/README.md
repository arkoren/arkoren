# certificate

Provision a local-development root and server certificates, or load existing
TLS identities. The module uses only the Go standard library and requires Go 1.27.
An `Identity` owns a certificate chain and its matching private key; a `Bundle`
contains public certificates only.

```sh
go get arkoren.dev/foundation/certificate
```

## Generate a local identity

Keep the root private key separate from the server key. Anyone who obtains the
root key can issue certificates trusted by clients that use that root. This
library neither writes keys nor installs operating-system trust.

To generate a server identity for explicit hosts and verify it against an
explicitly selected root, run:

```go
package main

import (
	"crypto/x509"
	"fmt"
	"log"

	"arkoren.dev/foundation/certificate"
)

func main() {
	root, err := certificate.GenerateRoot("Example development")
	if err != nil {
		log.Fatal(err)
	}
	leaf, err := certificate.GenerateServer(root, []string{"localhost", "127.0.0.1"})
	if err != nil {
		log.Fatal(err)
	}
	pool, err := root.Bundle().Pool()
	if err != nil {
		log.Fatal(err)
	}
	cert := leaf.Bundle().Certificates()[0]
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"}); err != nil {
		log.Fatal(err)
	}
	fmt.Println(cert.DNSNames[0])
}
```

The program prints `localhost`. To configure a TLS server, call `leaf.TLS()` and
place the returned `tls.Certificate` in `tls.Config.Certificates`. To configure
client trust, use the root's public `Bundle().Pool()` as `tls.Config.RootCAs`.
Neither operation changes the system root store.

Generation uses ECDSA P-256 and random positive serials of at most twenty bytes.
Roots expire after `5*365` days by default, can sign certificates, and have
`pathLen=0`, which prohibits intermediate CAs. Server leaves have only
server-auth extended key usage and expire after 90 days, clamped to issuer expiry.
`RootBuilder.Validity` and `ServerBuilder.Validity` set a different lifetime.
Zero restores the default; negative and positive subsecond durations fail with
`ErrInvalid`. An explicit server lifetime exceeding issuer expiry also fails
instead of being shortened.

Lifetimes are measured from generation time, rounded down to whole seconds.
`NotBefore` is backdated five minutes for clock skew, but a leaf never starts
before its issuer. An issuer must be one currently valid, self-signed root with
a matching key, certificate-signing usage, and `pathLen=0` CA constraints.
Imported roots satisfying this profile are accepted; no provenance is implied.

Hosts are explicit bare DNS names or IP addresses. No SAN is inferred from a
common name or added for localhost. DNS names use ASCII hostname labels, with
an optional leading `*.` wildcard; use punycode for international names. URLs,
ports, bracketed IPv6, whitespace, and empty host lists are rejected. Generation
does not prove DNS ownership. Callbacks run once in order, even for invalid
inputs; supplied nil callbacks panic. Retained drafts cannot change the result.

## Import existing certificates

Use `LoadIdentity("server.pem", "server-key.pem")` for explicit files or
`ParseIdentity(certPEM, keyPEM)` for bytes. Imported identities support RSA,
ECDSA, and Ed25519, including client certificates. Keys accept one unencrypted
PKCS #1, SEC 1, or PKCS #8 PEM block. `PrivatePEM()` always exports unencrypted
PKCS #8; protect this buffer as a secret. Ordinary formatting and slog output
are redacted, but explicit secret exports are not.

Certificate chains are leaf-first. Supplied adjacent certificates must have
matching issuer names and valid issuer signatures; partial chains are allowed.
Parsing does **not** establish trust, check expiry, validate hostnames, or check
revocation. An expired or untrusted client identity can parse successfully.
Use `crypto/x509` verification and your application's policy before trusting it.

Use `ParseBundle` or `LoadBundle` for public certificates without keys. Bundles
preserve order and can contain unrelated trust anchors; they do not validate
chain relationships. `Pool()` creates a new pool containing exactly those
certificates. Choosing them as trust anchors is your security decision.

If a client needs both private and public trust, use `SystemPool()` instead.
With a previously loaded `public` bundle, obtain the additive pool before
configuring `tls.Config.RootCAs`:

```go
pool, err := public.SystemPool()
if err != nil {
	return err
}
```

The returned pool includes system roots and detached bundle certificates; it
does not change the operating-system store. System-root loading errors retain
their causes and stop setup; there is no private-only fallback. Both pool
methods reject a zero bundle with `ErrInvalid`.

Each PEM input and each file read is limited to one MiB, including whitespace.
Junk, PEM headers, unexpected blocks, malformed DER, and extra key blocks fail
with `ErrInvalid`. OS read errors preserve their causes for `errors.Is` and
`errors.As`; private-key parser diagnostics are redacted. Missing files are never
repaired or replaced.

## Ownership and limits

Zero identities and bundles are invalid; `Err()` detects them. Byte inputs are
copied. PEM, native certificates, TLS keys and chains, and certificate pools are
fully detached on export. Immutable values can be shared concurrently, although
mutable construction drafts and exported native objects require your own
synchronization.

This is not a production PKI, ACME client, renewal service, intermediate issuer,
or operating-system trust manager. It performs no network requests or writes.
See the [package reference](https://pkg.go.dev/arkoren.dev/foundation/certificate)
for signatures and caller examples.
