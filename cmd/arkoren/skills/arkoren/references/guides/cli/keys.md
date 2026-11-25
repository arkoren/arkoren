---
title: Key Commands
description: Provision purpose-specific keys offline, check and rotate shared key rings, and export public PEM or VAPID values.
order: 60
summary:
  - Choose independent shared, signing, or exchange keys and create protected new files offline.
  - Validate stored material and list ring fingerprints without disclosing keys or implying deployment trust.
  - Rotate, promote and retire shared key rings in a file or through a pipe.
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
| `shared` | A [ring](../keys/overview.md#key-rings) of one 32-byte key: one line of padded standard base64 with a newline | Symmetric encryption or HMAC signing, rotated with `rotate`. |
| `signing` | ECDSA P-256, unencrypted PKCS #8 private-key PEM | Signing, including a Web Push VAPID identity. |
| `exchange` | X25519, unencrypted PKCS #8 private-key PEM | Key agreement within an authenticated protocol. |

Generate independent keys for unrelated purposes, even when the type matches.
An exchange identity alone supplies neither peer authentication nor an
encryption protocol. Provider-issued credentials have their own provenance:
an Apple Sign In key must come from Apple, not this generator.

## Generate Keys

For an application's encryption key, select a new explicit destination:

```sh
arkoren key generate shared --out .secrets/app-key
```

Success reports the `summary` "Generated shared key", the `kind`, and a
`changes` entry that creates the absolute `path`, never the private contents. There is no implicit destination or overwrite
flag. Existing files and symlinks are refused. A missing parent directory is
created with mode `0700`, so the first key of a new `.secrets/` directory needs no
setup; keep that directory out of version control.

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
arkoren key check shared .secrets/app-key
```

Success prints `ok: true`, the selected `kind`, the absolute `path`, the `keys` of
the ring and an empty `problems` array, without the secret. Each entry of `keys`
holds a `fingerprint`, the full SHA-256 digest in JSON and its first 12 digits in
terminal output, and `current: true` for the first key. The fingerprint identifies
a key for [promote and retire](#rotate-rings) without revealing it.

Material that does not parse is `ok: false` with a problem and exit 1; only a file
that cannot be read is an error. A shared file is a ring: one or more padded
standard base64 keys of exactly 32 bytes each, separated by commas on one line,
with one trailing newline allowed. A duplicate key or an empty entry is invalid.
`FILE` may be `-` to read the ring from standard input, as in
`aws secretsmanager get-secret-value ... | arkoren key check shared -`. This checks
representation and length, not entropy or deployment permissions.

For the signing identity, select its type explicitly:

```sh
arkoren key check signing vapid-private.pem
```

Signing requires ECDSA P-256 PKCS #8 PEM; checking `exchange-private.pem` with
`arkoren key check exchange exchange-private.pem` requires X25519 PKCS #8 PEM.
A successful check does not prove provider registration, message delivery, or
peer trust. Missing or malformed files fail without printing private contents.

## Rotate Rings

Three commands change a shared [key ring](../keys/overview.md#key-rings) without
you editing base64 by hand. They take the ring from `FILE` and apply to shared
keys only, so they have no `KIND` argument. The
[rotation procedure](../keys/overview.md#rotate-keys) says when to run each one.

```sh
arkoren key rotate --stage .secrets/app-key
```

`rotate` generates a key and adds it to the ring. By default it becomes the current
key and the old keys follow it. With `--stage` it is appended at the end instead,
as a previous key, so you can deploy it to every instance before any of them uses it.
The report names the staged key's fingerprint in a `next_steps` entry with the
`promote` command.

```sh
arkoren key promote 3f9a1c07be52 .secrets/app-key
```

`promote` moves the key whose fingerprint starts with the given digits to the
front of the ring, making it current. The order of the other keys is kept, and
promoting the current key changes nothing.

```sh
arkoren key retire 8c20d4f6a1e9 .secrets/app-key
```

`retire` removes a key from the ring. It refuses the current key, which includes
the only key of a one-key ring: promote another key first. Data protected only by
a retired key can no longer be read or verified.

`promote` and `retire` match a fingerprint prefix of at least 12 digits, as
`key check` prints them, case-insensitively. A prefix that is shorter, matches no
key or matches more than one key is invalid input and changes nothing. Every
command refuses text that is not a valid ring without printing it.

For a file, the command rewrites it atomically: it writes a temporary file in the
same directory with mode `0600`, syncs it and renames it over the original, so a
failure leaves the old ring intact. A symlink is followed and its target replaced.
The result has the `summary`, the `kind`, the `keys` of the new ring and an `edit`
change for the file; it never contains key material.

With `FILE` set to `-`, the commands read the ring from standard input and write
only the new ring to standard output, so you can move it through a secrets manager
without a file. The report goes to standard error:

```sh
aws secretsmanager get-secret-value --secret-id link-key --query SecretString --output text \
  | arkoren key rotate --stage - \
  | aws secretsmanager put-secret-value --secret-id link-key --secret-string file:///dev/stdin
```

Standard output carries private material in this form: send it only to a protected
pipeline, and check the pipeline's exit status, since a failed downstream command
leaves the stored ring unchanged while your terminal shows nothing. Both commands
above are the provider's own CLI, which this CLI does not call or depend on.

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
invalid usage exits 2; a refused overwrite, an unreadable file, invalid material
or an output failure exits 1. A broken pipe
or short stdout write is failure even if a file was already created. Inspect
state before retrying rather than treating missing output as proof no key exists.

Use `arkoren key --help` or `arkoren key generate --help` offline for syntax.
Continue with [Application Keys](../keys/overview.md) to load the provisioned
material in your application.
