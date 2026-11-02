---
title: Key Commands
description: Provision purpose-specific keys offline, check stored material, and export public PEM or VAPID values.
order: 60
summary:
  - Choose independent shared, signing, or exchange keys and create protected new files offline.
  - Validate stored material without disclosing it or implying deployment trust.
  - Export public PEM or signing-only VAPID values and recover safely from partial writes.
---

# Key Commands

Provision keys before deployment so application restarts reuse the intended
identity instead of silently replacing it. These commands use the same validated
[application key types](../keys/overview.md) your application loads. They run
locally without MCP discovery, endpoint settings, or `.env` loading.

Use the [installed CLI](overview.md#installing-arkoren) in a protected directory.
Never commit private material, and keep backups and parent-directory access
under your control.

## Choose Material

Choose the type your application's protocol requires:

| Type | Generated material | Ordinary purpose |
| --- | --- | --- |
| `shared` | 32 random bytes, padded standard base64 with a newline | Symmetric encryption or HMAC signing. |
| `signing` | ECDSA P-256, unencrypted PKCS #8 private-key PEM | Signing, including a Web Push VAPID identity. |
| `exchange` | X25519, unencrypted PKCS #8 private-key PEM | Key agreement within an authenticated protocol. |

Generate independent keys for unrelated purposes, even when the type matches.
An exchange identity alone supplies neither peer authentication nor an
encryption protocol. Provider-issued credentials have their own provenance:
an Apple Sign In key must come from Apple, not this generator.

## Generate Keys

For an application's encryption key, select a new explicit destination:

```sh
arkoren key generate shared --out app-key.key
```

Success prints JSON with `created: true`, `category: "shared"`, and the absolute
`path`, never the private contents. There is no implicit destination or overwrite
flag. Existing files and symlinks are refused, and missing parent directories
fail.

The file is created exclusively with POSIX mode `0600`, subject to umask.
On Windows, creation atomically installs a protected discretionary access
control list (DACL) granting access only to the current user, without temporarily
inheriting broader access. Native Windows ACL execution remains unverified.
The CLI does not secure parent directories for you.

If the application also sends Web Push notifications, provision a separate
signing identity:

```sh
arkoren key generate signing --out vapid-private.pem
```

This produces a new P-256 private identity under the same file-safety rules.
For a protocol requiring X25519 instead, use a separate exchange identity:

```sh
arkoren key generate exchange --out exchange-private.pem
```

Each invocation generates independent material. Neither uploads the key, edits
environment files, or replaces an application's deployed credential.

> [!WARNING]
> `--out -` explicitly sends newly generated private material to stdout instead
> of a JSON receipt. Use it only with a protected secret-store pipeline; terminal
> capture, shared logs, or a failed downstream consumer can expose or lose it.

## Check Material

Before configuring the application, check the file as its intended type:

```sh
arkoren key check shared app-key.key
```

Success prints `valid: true`, the selected `category`, and the absolute `path`,
without the secret. Shared material must decode to exactly 32 bytes from padded
standard base64. This checks representation and length, not entropy or
deployment permissions.

For the signing identity, select its type explicitly:

```sh
arkoren key check signing vapid-private.pem
```

Signing requires ECDSA P-256 PKCS #8 PEM; checking `exchange-private.pem` with
`arkoren key check exchange exchange-private.pem` requires X25519 PKCS #8 PEM.
A successful check does not prove provider registration, message delivery, or
peer trust. Missing or malformed files fail without printing private contents.

## Export Public Keys

If a peer needs a standard public key, export only that component:

```sh
arkoren key public signing vapid-private.pem
```

The result is raw SPKI `PUBLIC KEY` PEM, not JSON. `pem` is the default format.
An exchange public key uses the same representation:

```sh
arkoren key public exchange exchange-private.pem --format pem
```

Publishing an exchange public key does not authenticate it. Follow your
protocol's [peer-authentication and derivation requirements](../keys/overview.md#using-exchange-keys).
A shared key has no public component, so `public shared` is invalid.

For Web Push browser registration, request VAPID format instead:

```sh
arkoren key public signing vapid-private.pem --format vapid
```

This prints unpadded base64url for the uncompressed P-256 public point, suitable
for `applicationServerKey`; it does not include the private scalar. Only
`signing` supports `vapid`. Format flags can appear before or after the file;
invalid type/format combinations fail before file access.

## Handle Failures

Exclusive creation is not transactional. A write, sync, or close failure can
leave an incomplete new file. Inspect the reported destination before using or
removing it. Keep established identities unchanged; remove only a newly created
incomplete output after confirming it is not deployed, or choose a fresh path.
Do not regenerate keys on startup to hide a missing configuration file.

Relative paths resolve from the command's working directory. Success exits 0;
invalid usage exits 2; file, validation, or output failure exits 1. A broken pipe
or short stdout write is failure even if a file was already created. Inspect
state before retrying rather than treating missing output as proof no key exists.

Use `arkoren key --help` or `arkoren key generate --help` offline for syntax.
Continue with [Application Keys](../keys/overview.md) to load the provisioned
material in your application.
