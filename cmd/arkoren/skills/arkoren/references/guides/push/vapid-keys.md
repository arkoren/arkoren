---
title: VAPID Keys
description: Generate a persistent P-256 identity once, load it safely, and publish only its Web Push public key.
order: 20
---
# VAPID Keys

## Introduction

A browser subscription is bound to your application server's public key. Web Push
uses **VAPID**, an application-server authentication scheme, to prove that each
send comes from the holder of the matching private key. This is separate from the
per-subscription encryption keys supplied by the browser.

Generate one P-256 key pair for the application, persist it in your secret store,
and load the same private key on every replica and restart. Publish the public
key to the browser as its `applicationServerKey`. The Web Push driver accepts your
key; it does not generate or persist an application identity for you.

> [!WARNING]
> Regenerating the key at each startup breaks sends to subscriptions bound to the
> previous key. Keep the private key out of source control, browser responses,
> images, and logs. Back it up and restrict access as you would any signing key.

## Generating the key once

Install the offline CLI with `go install arkoren.dev/cmd/arkoren@latest`, then
provision an identity in a protected directory:

```sh
arkoren key generate signing --out vapid-private.pem
arkoren key check signing vapid-private.pem
arkoren key public signing vapid-private.pem --format vapid
```

Generation creates a private `0600` PKCS #8 PEM file and refuses existing files
or symlinks. The check reports local format validity, not provider acceptance.
The explicit `--format vapid` prints only the browser-ready public key;
without it, public output is SPKI PEM, which is not `applicationServerKey`. Do not use
`--out -` unless you intentionally need private material on stdout. Nothing
modifies `.env` or rotates an existing identity.

### Manual generation

If you cannot use the CLI, use this standard-library fallback. Its output is
compatible with `key.LoadSigning` and the CLI commands above.

The following one-off program creates an unencrypted PKCS #8 PEM file in your
current directory. Run it in a protected provisioning environment, not in your
server's startup path. The exclusive file creation refuses to replace an existing
key, and mode `0600` restricts access on systems with POSIX file permissions.

Create `cmd/vapid-keygen/main.go` in your application:

```go
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"log"
	"os"
)

// main provisions a new identity without replacing an existing private key.
// Run this command once; servers load its persisted output instead.
func main() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		log.Fatal(err)
	}
	file, err := os.OpenFile("vapid-private.pem", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		log.Fatal(err)
	}
	if err := pem.Encode(file, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		_ = file.Close() // Encoding failed; report that failure rather than a close error.
		log.Fatal(err)
	}
	if err := file.Close(); err != nil {
		log.Fatal(err)
	}
}
```

Run `go run ./cmd/vapid-keygen`. Success produces `vapid-private.pem` without
printing private material. Move it into your deployment's protected secret
storage and mount it read-only for the application. If provisioning fails after
creating the file, inspect and remove the incomplete file before running the
command again; never replace a key already used for subscriptions.

## Loading and publishing the public key

Install `arkoren.dev/foundation/key` and `arkoren.dev/driver/webpush` in your
application module. At startup, load the persisted identity and pass the typed
value directly to the driver:

```go
import (
	"arkoren.dev/driver/webpush/webpushpush"
	"arkoren.dev/foundation/key"
)

identity, err := key.LoadSigning("vapid-private.pem")
driver, err := webpushpush.New(identity, "mailto:push@example.com")
publicKey := identity.VAPIDPublicKey()
```

The fragment provides `driver` for `push.New(driver)` and `publicKey` for your
application's public configuration. Loading rejects leading junk, extra PEM
blocks, other algorithms/curves, and malformed key pairs. For PEM bytes from a
secret store, use `key.ParseSigning(contents)` instead. Native integrations can
import existing material with `key.NewSigning(privateKey)`; the Web Push constructor
does not accept a raw native key.

Serve only `publicKey` from `GET /push/public-key`, as plain text,
or place it in your page's public configuration. That route must never serialize
the private key or return the PEM file. Public-key exposure is intentional; the
private scalar is secret. The driver has no `PublicKey` accessor because your
application already owns the key value. Its methods return detached exports;
formatting the value is redacted. See [Application Keys](../keys/overview.md)
for typed key selection and startup loading.

## Rotating the identity

Changing the public key requires new browser subscriptions. Keep a key identifier
with each registration so you can continue sending with its matching old private
key during a migration. Ask the browser to unsubscribe and resubscribe with the
new public key in an explicit user flow, then replace the application's stored
registration. Do not switch all existing subscriptions to the new signer.

To use the public key in a browser, continue with [browser integration](browser-integration.md).
For the driver's required key and contact inputs, see the
[Web Push API reference](https://pkg.go.dev/arkoren.dev/driver/webpush/webpushpush).
