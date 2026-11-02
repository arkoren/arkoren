---
title: VAPID Keys
description: Keep a persistent Web Push signing identity and publish only its browser-ready public key.
order: 30
summary:
  - Persist a dedicated VAPID signing identity instead of regenerating it at startup.
  - Load protected private material and expose only the browser-ready public key.
  - Rotate identities through new subscriptions while preserving registration versions.
---
# VAPID Keys

A browser subscription created with your application server's public key requires
sends authenticated by the matching private key. **VAPID** is that server
authentication scheme; it is separate from the browser-generated encryption
keys in `push.Subscription.Keys`.

Provision a dedicated P-256 signing identity once, persist it in protected secret
storage, and load the same identity on every application replica and restart.
The [Web Push driver](drivers.md#web-push) consumes your identity; it does not
generate or persist one.

> [!WARNING]
> Generating a new identity at startup breaks sends to existing subscriptions.
> Protect and back up the private key. Never put it in source control, browser
> responses, container images, or logs; publish only the public component.

## Provisioning Identity

Use the [installed CLI](../cli/overview.md#installing-arkoren) in a protected
provisioning directory, outside server startup:

```sh
arkoren key generate signing --out vapid-private.pem
```

This creates an unencrypted PKCS #8 P-256 private-key PEM file and refuses to
replace an existing file or symlink. Move the identity into your deployment's
secret storage and mount it read-only for the application. Follow the generic
[key commands](../cli/keys.md) for file permissions, backups, and partial-write
recovery rather than regenerating an established identity.

Before deployment, check the stored representation and export its public point:

```sh
arkoren key check signing vapid-private.pem
arkoren key public signing vapid-private.pem --format vapid
```

The check establishes local format validity, not provider acceptance. Explicit
`--format vapid` prints the public point as unpadded base64url, suitable for
`applicationServerKey`; the default public output is SPKI PEM and is not the
browser registration value. These commands do not upload keys or edit `.env`.

## Loading Identity

Install `arkoren.dev/foundation/key` in your application module and import
`arkoren.dev/foundation/key`. Load the protected file once during startup:

```go
identity, err := key.LoadSigning("vapid-private.pem")
```

Handle failure before publishing configuration or constructing a sender.
Loading rejects malformed key pairs, other curves or algorithms, leading junk,
and extra PEM blocks. If your secret store already supplies PEM bytes, use
`key.ParseSigning(contents)` instead. See [Application Keys](../keys/overview.md)
for typed key selection and loading, including importing native signing keys.

Pass this `key.Signing` value to the
[driver constructor](drivers.md#installing-web-push). The driver snapshots its
own key material; your application retains ownership of the loaded identity.

## Publishing Public Keys

Export only the browser-ready public component from the successfully loaded
identity:

```go
publicKey := identity.VAPIDPublicKey()
```

Serve `publicKey` as plain text from your application's `GET /push/public-key`,
or place it in public page configuration. That route is application-owned, not
automatically installed by Arkoren. Do not serialize the identity or return its
private PEM. Public-key exposure is intentional and does not disclose the
private scalar.

Keep the served public key aligned with the signer used for new subscriptions.
Store a server-selected key identifier with each registration so later sends
can select the matching identity without trusting a client-supplied key choice.

## Rotating Identity

Changing the public key requires new subscriptions. Retain old identities for
registrations still bound to them during a migration; do not send every existing
subscription with the new private key.

Use an explicit browser flow to unsubscribe and resubscribe with the new public
key, then replace the stored registration and its key identifier atomically.
Keep a registration version as well, so cleanup after an older failed send
cannot remove its replacement. Losing the old identity prevents continued sends
to subscriptions bound to it; generating a replacement does not restore that
relationship.

Continue with [Browser Integration](browser-integration.md) to use the public key
in subscription registration. For typed exports and parsing, see the
[key API reference](https://pkg.go.dev/arkoren.dev/foundation/key).
