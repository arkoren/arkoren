---
title: TLS Certificates
nav: Certificates
description: Provision local identities, load certificate files, and configure server trust and mutual TLS without weakening verification.
order: 30
summary:
  - Distinguish publicly trusted production identities from explicit local-development provisioning.
  - Generate local server certificates and load existing identities without changing files or system trust.
  - Choose private-only or additive system roots while preserving hostname and certificate verification.
  - Configure both sides of mutual TLS and keep authentication separate from application authorization.
  - Share detached certificate exports safely without mistaking native TLS clones for deep copies.
---

# TLS Certificates

HTTPS needs two separate decisions: which identity a server presents, and which
issuers a client trusts. An **identity** contains a certificate chain and its
matching private key. A **certificate authority (CA)** signs certificates;
clients must independently trust that authority before accepting its identities.

The `certificate` package provisions local-development identities and imports
existing client or server credentials as native Go TLS values. Keep generic
[application key management](../keys/overview.md) separate from certificate
issuance. For file creation and trust-store command syntax, use
[Certificate Commands](../cli/certificates.md).

## Choose Trust

For a public production service, obtain a certificate from an issuer your
clients already trust, or terminate TLS at an ingress that manages those
certificates. This package's local CA is not publicly trusted and is not a
production PKI, ACME client, or renewal service. Generating a certificate for
a public hostname does not validate ownership of that name.

For local HTTPS, generate a dedicated development CA and configure a particular
client to trust its public certificate. A client-specific pool keeps that
decision out of the operating system's global trust store. Do not disable
certificate verification to make local requests succeed: fix the trusted issuer,
certificate names, or validity instead.

The library neither writes files nor changes system trust. The examples below
also do neither. If you later choose a CLI trust command, review its broader
scope and required permissions separately.

## Provision Locally

Add the independent module from your application's module directory:

```sh
go get arkoren.dev/foundation/certificate
```

It works without Arkoren core. The server and retrying-client integrations use
`arkoren.dev/core`; follow
[existing application installation](../first-steps/installation.md#existing-applications)
if that dependency is not already present.

> [!WARNING]
> Anyone with a trusted CA's private key can issue identities accepted by its
> clients. Protect private exports and their storage directories, never commit
> them, and keep the CA key out of ordinary server deployments. Provision
> deliberately; do not regenerate missing credentials during application startup.

In an explicit provisioning operation, import
`arkoren.dev/foundation/certificate` and generate the development authority:

```go
authority, err := certificate.GenerateRoot("Reports Local Development")
```

This creates an in-memory ECDSA P-256 root with a default lifetime of
5 × 365 days. After checking the error, issue a server identity for the names
your local clients actually use:

```go
identity, err := certificate.GenerateServer(authority, []string{"localhost", "127.0.0.1", "::1"})
```

The server certificate also uses P-256 and has server-authentication purpose.
Its subject alternative names (SANs) are exactly the supplied DNS names and IP
addresses; no localhost name is inferred. Supply bare ASCII DNS names or IP
literals, not URLs or host-and-port pairs. A common name does not replace SANs.

The default server lifetime is 90 days, shortened to the issuer's remaining
lifetime. To choose a shorter lifetime, import `time` and use a construction
callback instead:

```go
identity, err := certificate.GenerateServer(authority, []string{"localhost"}, func(leaf *certificate.ServerBuilder) {
	leaf.Validity(7 * 24 * time.Hour)
})
```

An explicit lifetime beyond the issuer's expiry fails rather than being
shortened. Zero selects the default; negative or positive subsecond lifetimes
are invalid. `RootBuilder.Validity` similarly selects a root lifetime. The
issuer must be a currently valid, self-signed, certificate-signing root with
path-length-zero constraints, as generated above; intermediate CA issuance is
not supported.

`identity.Bundle().PEM()` exports the public chain, leaf first.
`identity.PrivatePEM()` explicitly exports the unencrypted private key. Your
provisioning operation owns secure persistence and distribution; the
[CLI provisioning commands](../cli/certificates.md) offer exclusive file
creation when you do not need custom storage. The package provides no automatic
renewal or client-certificate generator.

## Load Identities

During application startup, load previously provisioned credentials instead of
generating replacements. With `certificate` imported, read the deployment's
server chain and private key:

```go
identity, err := certificate.LoadIdentity("server.pem", "server-key.pem")
if err != nil {
	return err
}
credentials, err := identity.TLS()
if err != nil {
	return err
}
```

These checks stop setup if files are missing or credentials cannot be converted.
With `crypto/tls` imported, prepare the server's native configuration:

```go
settings := &tls.Config{
	MinVersion:   tls.VersionTLS12,
	Certificates: []tls.Certificate{credentials},
}
```

Pass it to the [server's HTTPS setup](server.md#serving-https) before startup.
The loader accepts RSA, ECDSA, and Ed25519 identities, not just generated P-256
keys. Chains are leaf-first and can omit the root. Private keys must be
unencrypted PKCS #1, SEC 1, or PKCS #8 PEM. Each input or file read is bounded
to 1 MiB; malformed blocks, extra keys, headers, and non-whitespace junk fail
parsing. File errors preserve their OS causes.

Parsing checks encoding, supplied chain order and signatures, and the key match.
It does not establish trust, current validity, revocation, hostname coverage,
or suitability for a particular TLS purpose. Peer verification during a native
TLS handshake applies its policy; use `x509.Certificate.Verify` with explicit
roots and options if you need a separate verification step.

## Choose Client Trust

For a client that must trust only your private CA, obtain its public root through
a trusted distribution path. Parsing a downloaded certificate does not establish
its provenance. Load that public file, not the CA's private key:

```go
roots, err := certificate.LoadBundle("development-ca.pem")
if err != nil {
	return err
}
pool, err := roots.Pool()
if err != nil {
	return err
}
```

`Pool` creates an independent private-only trust pool. It adds no system roots,
so this pool does not also accept ordinary public issuers. With `net/http`,
`time`, `crypto/tls`, and `arkoren.dev/core/http/client` imported, give the pool
to a transport:

```go
transport, err := client.NewTransport(&tls.Config{
	MinVersion: tls.VersionTLS12,
	RootCAs:    pool,
})
if err != nil {
	return err
}
outgoing := &http.Client{Transport: transport, Timeout: 8 * time.Second}
```

Use this client with the [request example](http-client.md#read-responses).
For the generated localhost identity, request `https://localhost:8443` from a
server configured to listen there. The URL must match a SAN; trusting the CA
does not bypass name checks. `RootCAs` verifies the server and does not present
a client identity.

`NewTransport` clones the current default native transport, preserving proxy,
pooling, and HTTP/2 settings. A default that is not a nonnil `*http.Transport`
returns an error; for a base you already own, use
[custom transport ownership](http-client.md#own-transports). When retiring the
owned pool, call `outgoing.CloseIdleConnections()`; active requests continue.

### Add Public Roots

If this client intentionally calls both private services and public services,
replace `roots.Pool()` with an additive system pool:

```go
pool, err := roots.SystemPool()
if err != nil {
	return err
}
```

Use this pool in the same `RootCAs` field. It retains system roots and adds the
bundle's certificates to an independent pool; it does not alter the OS store.
A system-root loading error is returned with its cause, not silently replaced
by private-only trust. A zero bundle fails before system roots are loaded.
Add only issuers you intend this client to trust.

## Require Client Identity

Mutual TLS (mTLS) adds client-certificate verification to server verification.
Obtain a client-purpose identity from your issuer; `GenerateServer` deliberately
does not issue client identities. Load the supplied client chain and key with
the same checked conversion:

```go
identity, err := certificate.LoadIdentity("client.pem", "client-key.pem")
if err != nil {
	return err
}
credentials, err := identity.TLS()
if err != nil {
	return err
}
```

On the client, use `serverRoots`, a pool of CAs trusted to issue server
certificates, and present these credentials:

```go
transport, err := client.NewTransport(&tls.Config{
	MinVersion:   tls.VersionTLS12,
	RootCAs:      serverRoots,
	Certificates: []tls.Certificate{credentials},
})
if err != nil {
	return err
}
outgoing := &http.Client{Transport: transport, Timeout: 8 * time.Second}
```

Sending a client identity does not replace verification of the server. Keep
that separate trust policy and retire the owned client pool as above.

On the server, use `settings` containing its own server identity from
[Load Identities](#load-identities). Before starting it, require verified client
certificates with `clientRoots`, a pool made from the public client-issuing CA
bundle:

```go
settings.ClientCAs = clientRoots
settings.ClientAuth = tls.RequireAndVerifyClientCert
```

`ClientCAs` and the outgoing client's `RootCAs` have different roles and need
not contain the same authorities. A missing, untrusted, expired, or wrong-purpose
client certificate fails the handshake. After authentication, your application
still decides which operations that identity may perform; trusting its CA does
not grant unrestricted access.

## Share Safely

`Identity` and `Bundle` are immutable. Native certificates, pools, PEM buffers,
and TLS exports are detached, including the private key returned by `TLS()`.
Changing an export does not change the original value. Their zero values are
invalid; handle conversion errors or `Err()` before use. Do not log secret
exports even though ordinary identity formatting is redacted.

Once you place exported values into a native `tls.Config`, Go's ownership rules
apply. `tls.Config.Clone` is shallow: referenced pools, certificates, keys, and
callback state remain shared. Finish configuration before sharing it with a
running server or client, and do not mutate it afterward. Native certificate
callbacks can select prepared identities; they do not provide automatic renewal.

The retrying client's default policy treats TLS errors as transport errors for
eligible requests. Retrying does not repair bad trust or names. This differs
from the CLI's [condition-wait policy](../cli/reference.md#retry-rules), which
treats certificate-verification failures as terminal.

For parsing formats, generation constraints, and every export, see the
[`certificate` API reference](https://pkg.go.dev/arkoren.dev/foundation/certificate).
