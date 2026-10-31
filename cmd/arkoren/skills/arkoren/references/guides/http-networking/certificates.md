---
title: TLS Certificates
nav: Certificates
description: Provision local TLS identities, load existing certificates, and configure private CA trust and mutual TLS with native Go settings.
order: 30
---

# TLS Certificates

## Introduction

When a local service needs HTTPS, provision its identity separately from starting
the process. An **identity** is a certificate chain and its matching private key.
A **certificate authority (CA)** signs certificates; clients must trust that CA
before accepting the identities it issues.

The `certificate` package generates a private root CA and local server
identities, and loads existing client or server identities. It exports native
Go TLS values for [servers](server.md) and [HTTP clients](http-client.md).
For commands that write the files and optionally change system trust, use
[Certificate Commands](../cli/certificates.md).

Focused provisioning snippets keep error results visible and assume success.
Trust and TLS setup examples retain checks that prevent using incomplete credentials
or falling back to unintended trust settings.

## Provisioning a local identity

Install the independent certificate module from your application's module:

```sh
go get arkoren.dev/foundation/certificate
```

The module works without Arkoren core. For the server and retrying-client
integrations below, also add `go get arkoren.dev/core` if your application does
not already use it.

> [!WARNING]
> A stolen CA private key lets an attacker impersonate sites to clients that
> trust the CA. Generate it in a protected environment, restrict access to its
> private exports and parent directories, and never commit private keys.
> Application startup must fail on missing material, not generate a replacement.

For an explicit provisioning operation, import
`arkoren.dev/foundation/certificate` and create a root:

```go
authority, err := certificate.GenerateRoot("Example Local Development")
```

This returns an immutable CA identity in memory. It neither writes files nor
installs trust. The default validity is 5 × 365 days. To select another duration,
also import `time` and pass a construction callback:

```go
authority, err := certificate.GenerateRoot("Example Local Development", func(root *certificate.RootBuilder) {
	root.Validity(365 * 24 * time.Hour)
})
```

Use that authority to issue a server identity for every name clients actually
use. A subject alternative name (SAN) identifies a DNS name or IP address; the
legacy common name is not a substitute:

```go
identity, err := certificate.GenerateServer(authority, []string{"localhost", "127.0.0.1", "::1"})
```

The generated root and server keys use ECDSA P-256. No hostnames are inferred.
The default server validity is 90 days, shortened if the issuer expires sooner.
If you explicitly request a lifetime with `ServerBuilder.Validity` that extends
beyond the issuer's validity, generation fails instead of silently shortening it.
Zero selects the default; negative and positive subsecond durations are invalid.
The issuer must be a currently valid, self-signed root authorized to sign
certificates with path-length-zero constraints, as generated above. Names are
bare ASCII DNS names or IP literals, not URLs or host-and-port pairs.

`identity.Bundle().PEM()` exports the public chain; `identity.PrivatePEM()`
explicitly exports the unencrypted private key. Your provisioning operation owns
secure persistence. The [CLI](../cli/certificates.md#generating-a-local-root)
provides exclusive file creation if you do not need custom persistence.
There is no ACME integration, renewal loop, intermediate CA generation, or
client-certificate generation.

## Loading a server identity

With previously provisioned `server.pem` and `server-key.pem` in your deployment,
load the same identity on each startup. Add `crypto/tls` to your imports:

```go
identity, err := certificate.LoadIdentity("server.pem", "server-key.pem")
if err != nil {
	return err
}
credentials, err := identity.TLS()
if err != nil {
	return err
}
settings := &tls.Config{
	MinVersion:   tls.VersionTLS12,
	Certificates: []tls.Certificate{credentials},
}
```

Pass `settings` to `server.Builder.TLS` as shown in
[Serving HTTPS](server.md#serving-https). Missing or malformed files stop this
setup before a listener is created. Loading also supports existing RSA, ECDSA,
and Ed25519 identities; it does not restrict imported identities to P-256.
The certificate chain is leaf-first and can omit its root. Private PEM accepts
unencrypted PKCS #1, SEC 1, and PKCS #8; parsing rejects extra key blocks,
headers, and non-whitespace junk. Each input or file read is bounded to 1 MiB.

Parsing checks encoding, chain structure, and the key match. It does not prove
that a certificate is currently valid, trusted, valid for the URL's hostname,
or authorized for the intended TLS purpose. Native TLS verification performs
those checks when peers connect. Use `x509.Certificate.Verify` if you need a
separate verification step with explicit roots and policy.

## Trusting a private CA in an HTTP client

A server sends its identity; a client separately decides which CAs it trusts.
For a client that should trust only your private CA, load the public root and
create a dedicated pool. This fragment also uses `net/http`, `time`, and
`arkoren.dev/core/http/client`:

```go
roots, err := certificate.LoadBundle("root.pem")
if err != nil {
	return err
}
pool, err := roots.Pool()
if err != nil {
	return err
}
transport, err := client.NewTransport(&tls.Config{
	MinVersion: tls.VersionTLS12,
	RootCAs:    pool,
})
if err != nil {
	return err
}
outgoing := &http.Client{
	Transport: transport,
	Timeout:   10 * time.Second,
}
```

Use `outgoing` with the [request example](http-client.md#sending-a-request).
Its URL must match a certificate SAN, such as `https://localhost:8443`.
`RootCAs` controls verification of servers; it does not send a client identity.
The bundle pool contains no system roots, so this client does not also trust
ordinary public CAs. Never bypass verification with `InsecureSkipVerify`.

`NewTransport` clones the current default's native proxy, connection-pool, and
HTTP/2 settings. It returns an error if `http.DefaultTransport` is not a nonnil
`*http.Transport`; for a custom base, use `client.Transport(base)` instead.
The supplied TLS configuration is shallow-cloned, not mutated. Configure its
referenced pools and certificates before sharing them. Call
`outgoing.CloseIdleConnections()` when retiring the owned pool; active requests
are not interrupted.

### Keeping system roots as well

If the same client calls both private and public services, start with the system
pool instead. With the loaded `roots` bundle above:

```go
pool, err := roots.SystemPool()
if err != nil {
	return err
}
```

Use this `pool` in `RootCAs` instead of the private-only pool. Handle a system-pool
error rather than silently falling back to private-only trust: `SystemPool`
returns the loading error with its cause preserved. The returned pool is
independent and does not modify the OS store or bundle. A zero bundle fails
before system-root loading. Add only CA certificates you intend to trust;
parsing a bundle does not establish its provenance.

## Presenting a client identity with mutual TLS

Mutual TLS (mTLS) also authenticates the client to the server. Obtain a
client-purpose certificate from your issuer; the local server generator does
not create client identities. Load the public chain and matching private key
with the same API:

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

Using `pool` from the private-CA client above, create a transport that both
verifies the server and presents this client identity:

```go
transport, err := client.NewTransport(&tls.Config{
	MinVersion:   tls.VersionTLS12,
	RootCAs:      pool,
	Certificates: []tls.Certificate{credentials},
})
if err != nil {
	return err
}
outgoing := &http.Client{Transport: transport, Timeout: 10 * time.Second}
```

Use `outgoing` to send requests and call `outgoing.CloseIdleConnections()` when
retiring it. Sending a client certificate does not replace verification of the
server; keep `RootCAs` configured for that separate role.

On the receiving server, `ClientCAs` names the CAs trusted to issue client
certificates. With `pool` built from your client CA's public bundle and
`settings` from [Loading a server identity](#loading-a-server-identity), require
verified client certificates before handing `settings` to the server:

```go
settings.ClientCAs = pool
settings.ClientAuth = tls.RequireAndVerifyClientCert
```

A missing, untrusted, expired, or wrong-purpose client certificate fails the
handshake. Your application still owns authorization after TLS authentication;
trusting a client CA is not permission to perform every operation.

## Ownership and operational limits

`Identity` and `Bundle` retain private immutable state. Their byte slices,
certificates, pools, and TLS exports are detached; mutating an export does not
change the original value. `TLS()` also detaches its private key. A zero value
is invalid; handle `Err()` or the conversion error before use.

Once exported values are placed in a native `tls.Config`, normal Go ownership
rules apply. `tls.Config.Clone` is not a recursive copy of pools, certificate
slices, keys, or callbacks. Configure them before sharing and do not mutate
them while a server or client uses them. Dynamic certificate callbacks remain
native TLS behavior; they are not an automatic renewal facility.

The retrying HTTP client retains its ordinary outcome policy: TLS certificate
errors are transport errors and can be retried for eligible requests. Retrying
does not repair bad trust or names. This differs from the CLI's
[condition-wait retry policy](../cli/reference.md#retry-rules), which treats
certificate-verification failures as terminal.

For parsing formats, bounds, and all exported operations, see the
[`certificate` API reference](https://pkg.go.dev/arkoren.dev/foundation/certificate).
