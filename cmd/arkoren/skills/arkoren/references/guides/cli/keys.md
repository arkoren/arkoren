---
title: Key Commands
description: Generate keys offline, validate stored material, and export PEM or VAPID public keys.
order: 60
---

# Key Commands

## Introduction

Use the offline key commands to provision shared, signing, or exchange material
before deploying an application. They use the
same validated [`key` types](../keys/overview.md) that applications load.
They do not connect to an inspector, read `.env`, or modify existing credentials.

Install the CLI with `go install arkoren.dev/cmd/arkoren@latest`. Run provisioning
in a protected directory; do not generate replacement keys on server startup.

## Generating private material

Select a type and an explicit destination:

```sh
arkoren key generate shared --out app-key.key
arkoren key generate shared --out link-key.key
arkoren key generate shared --out cursor-key.key
arkoren key generate signing --out vapid-private.pem
arkoren key generate exchange --out exchange-private.pem
```

Each invocation creates independent material. `shared` creates 32 random bytes
encoded as padded standard base64 plus a newline, for symmetric encryption or
HMAC signing. `signing` creates an ECDSA P-256 identity; `exchange` creates an
X25519 identity. Both asymmetric types use unencrypted PKCS #8 private-key PEM.
Generate separate keys for unrelated purposes even when their types match.
An Apple Sign In key must come from Apple, not this generator.

Successful file creation prints JSON with `created: true`, `category`, and the
absolute `path`, never its contents.
The destination must not exist, including as a symlink. On POSIX, it is created
with mode `0600`. On Windows, exclusive creation installs a protected
discretionary access control list (DACL) granting full access only to the current
user, without temporarily inheriting broader parent permissions. Protection is
applied at file creation, not repaired afterward. Native Windows ACL execution
remains unverified.

There is no overwrite flag or implicit destination; missing parent directories
fail. Protected creation does not make the content write transactional: a write,
sync, or close failure can leave an incomplete file. Inspect it before using or
removing it, and keep established identities unchanged.

For a secret-store pipeline, `--out -` explicitly exports private material to
stdout. Avoid terminal capture, shared shell logs, and committed configuration.
The CLI does not upload material, edit environment files, or manage permissions
on parent directories. Protect those directories and store backups securely.

## Checking a stored key

To verify local encoding, type, and length without printing the secret:

```sh
arkoren key check shared app-key.key
arkoren key check signing vapid-private.pem
arkoren key check exchange exchange-private.pem
```

Success prints JSON with `valid: true`, the selected type in `category`, and the
absolute file `path`.
`shared` requires exactly 32 decoded bytes, represented as padded standard base64.
`signing` requires ECDSA P-256 PKCS #8 PEM; `exchange` requires X25519 PKCS #8 PEM.
This does not verify entropy, provider registration, delivery, or deployment
permissions. Malformed/missing files fail without printing their contents.

## Extracting a public key

For a signing or exchange identity, extract a public key without its private
material:

```sh
arkoren key public signing vapid-private.pem
arkoren key public exchange exchange-private.pem --format pem
```

Both commands print SPKI `PUBLIC KEY` PEM. `pem` is the default format. A shared
key has no public component, so `public shared` is invalid.

For Web Push browser registration, explicitly select VAPID format:

```sh
arkoren key public signing vapid-private.pem --format vapid
```

This prints the unpadded base64url encoding of the uncompressed P-256 public
point, suitable for `applicationServerKey`. Only `signing` accepts `--format vapid`.
Public output never includes the private scalar. Publishing an exchange public
key does not authenticate it; follow your protocol's peer-authentication and
[key-derivation requirements](../keys/overview.md#using-exchange-keys).

All relative paths resolve against the command's working directory. Invalid
commands exit 2; file/validation/output errors exit 1; success exits 0. Existing
stdout failure rules apply, including short writes and broken pipes.
Public-format flags can appear before or after `FILE`. Invalid type/format combinations fail
before any file is read or created.

Continue with [Application Keys](../keys/overview.md) to load the generated
material, or use `arkoren key --help` and `arkoren key generate -h` offline.
