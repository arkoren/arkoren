---
title: Certificate Commands
description: Generate and check local TLS files offline, explicitly install or remove a root CA, and recover from partial operations.
order: 70
---

# Certificate Commands

## Introduction

Use the offline certificate commands to provision a local HTTPS identity before
starting an application. A private root CA signs the server certificate; clients
must separately trust the root's public certificate. Generating files never
installs that trust automatically.

With Go 1.27 or later, install the CLI with
`go install arkoren.dev/cmd/arkoren@latest`. These commands and
`arkoren certificate --help` work without an MCP
endpoint and do not read `.env` or endpoint settings. For Go applications, the
[TLS Certificates guide](../http-networking/certificates.md) shows loading the
files and configuring a server or client.

## Generating a local root

> [!WARNING]
> Anyone holding your root's private key can impersonate sites to clients that
> trust that root. Generate as your normal user in a protected directory, protect
> the private key and its parent directories, and never commit it. Do not run
> certificate generation as an administrator merely to install trust later.

Choose an identifying CA name and two explicit, previously unused file paths:

```sh
arkoren certificate root generate --name "Example Local Development" --cert root.pem --key root-key.pem
```

Success creates a public root certificate and an unencrypted private key. The
root uses ECDSA P-256 and a default validity of 5 × 365 days. Keep `root-key.pem`
private; distribute only `root.pem` to clients that need to trust this CA.
The JSON result names both created paths in `certificate` and `key`, with
`created: true` and `trust_changed: false`;
it never prints the private key.

Both output paths are required. The CLI refuses existing files, symlinks, and
paths that refer to the same destination, including names differing only by
case in the same parent. Parent directories must already
exist and must not be reached through symlinked parents. `-` is not a stdout
destination. It preflights both destinations, then creates files exclusively;
it does not overwrite established material. POSIX modes are `0644` for public
certificates and `0600` for private keys, subject to umask. On Windows, private
files receive a protected discretionary access control list (DACL) granting
access only to the current user. It is installed atomically with exclusive
creation, before any content is written, without temporarily inheriting broader
parent access. Native Windows ACL execution remains unverified.
Parent-directory permissions remain your responsibility on every platform.

A write failure can leave a partial pair or an incomplete file at the exact
reported paths. Creation is not transactional and there is no rollback promise.
Follow [Partial file creation](#partial-file-creation) before retrying.

## Generating a server identity

With the root pair above, name every DNS name and IP address that clients will
use. Repeat `--dns` and `--ip` as needed:

```sh
arkoren certificate generate --ca-cert root.pem --ca-key root-key.pem --dns localhost --ip 127.0.0.1 --ip ::1 --cert server.pem --key server-key.pem
```

Success creates the server's public certificate chain and matching private key.
The generated server key uses ECDSA P-256. Its default validity is 90 days,
shortened if the issuer expires sooner. At least one explicit DNS name or IP
address is required; the CLI does not discover hostnames. A connection to an
unlisted name fails hostname verification even if the client trusts the root.

The same exclusive-output rules apply to `server.pem` and `server-key.pem`.
Load them with the [server HTTPS setup](../http-networking/server.md#serving-https).
Startup must fail if files are missing, not generate a new root or server
identity. These commands do not create intermediates or client identities,
renew certificates, or contact ACME.

## Checking stored material

Check the public bundle, or also supply the private key to check the match:

```sh
arkoren certificate check --cert root.pem
arkoren certificate check --cert server.pem --key server-key.pem
```

Success prints JSON with `parsed: true`, `expiry_checked: false`, and
`trust_checked: false`. `key_matched` is true only when a supplied key matched;
without a key it is false.
Neither prints private contents. This is structural validation, not an expiry,
hostname, TLS-purpose, or trust test. Existing RSA, ECDSA, and Ed25519 identities
in supported unencrypted PEM formats can be checked as well. A malformed bundle, mismatched key, or missing
file fails without exposing the private material.

To verify deployment, connect through a client configured with the intended
trust and URL, and check that the request succeeds. A successful file check
alone does not prove that a service is running or that a browser trusts it.

## Installing system trust

Prefer a [client-specific CA pool](../http-networking/certificates.md#trusting-a-private-ca-in-an-http-client)
when only one application needs the CA. System trust affects other applications
that use that system store, so install only a root you control and intend to
trust broadly.

The trust commands accept the public PEM containing exactly one self-signed CA
certificate, never a private key or arbitrary chain. Installation additionally
requires the root to be currently valid. Inspect the SHA-256 fingerprint, selected
system store or anchor directory, and system-wide scope before approving:

```sh
arkoren certificate root trust root.pem
```

Confirmation defaults to no. Noninteractive use requires an explicit `--yes`:

```sh
arkoren certificate root trust root.pem --yes
```

Before mutation, stderr receives a JSON `warning` object with the action,
fingerprint, concrete target, and scope warning, including with `--yes`.
Interactive confirmation prompts remain text. Success returns JSON on stdout
with `action`, `completed: true`, `sha256`, and `target`; this does not prove
that every client uses that store.

The CLI does not invoke `sudo`, request UAC elevation, or relaunch itself. If
permissions are insufficient, rerun only the trust command with the privileges
required by your system. Keep generation and private-key access under your
normal user.

### Supported system stores

| System | Target and prerequisites |
| --- | --- |
| macOS | System keychain (`/Library/Keychains/System.keychain`), using `security` and administrator SSL trust. |
| Debian and Ubuntu Linux | `/usr/local/share/ca-certificates` and `/usr/sbin/update-ca-certificates`. |
| Fedora and RHEL Linux | `/etc/pki/ca-trust/source/anchors` and `/usr/bin/update-ca-trust extract`. |
| Windows | Native Crypt32 operations on the LocalMachine `Root` store; administrative permissions are required for changes. |

Native Windows trust execution remains unverified.

Other Linux layouts and unsupported systems return an error; the CLI does not
guess an installation path. The operation identifies certificates by exact DER
content and fingerprint, not only a subject name. Installing an already
installed matching root is idempotent.

On macOS, installation sets this root's administrator trust policy to SSL
trust. Removal clears administrator trust settings for the exact root but
retains its public System keychain certificate, avoiding deletion of items
other tools might use. Neither operation changes user-domain or vendor trust.
Unrecognized or localized missing-record diagnostics fail closed rather than
being treated as proof that trust settings are absent.

On Linux, Arkoren manages only its own `arkoren-<SHA-256>` anchor file in the
selected directory. An existing anchor must contain the expected canonical
certificate bytes; conflicting files or symlinks are refused. Installing or
removing an anchor also runs the native refresh tool, including on repeat
operations, so a repeat can repair a previous refresh failure. This does not
manage copies installed elsewhere by an administrator.
Serialize these changes with other administrators' trust-store edits;
refreshing the system store is not transactional.

System trust is not universal trust. Browsers with separate stores, NSS,
Java, containers, remote machines, and independently configured applications
can require separate setup. Restart long-running clients after changing trust:
they can cache root sets or established TLS connections. Check the actual client
after installation; do not disable certificate verification to hide a trust-store
mismatch.

## Removing system trust

Retain the exact public root file so you can identify and remove that certificate
later. Removal accepts an expired root, but still validates that it is exactly
one self-signed CA:

```sh
arkoren certificate root untrust root.pem
```

As with installation, inspect the fingerprint and target; confirmation defaults
to no, and noninteractive use requires `--yes`. Removal targets only the exact
matching certificate, not every certificate with the same name. An established
absence is idempotent; unknown native diagnostics remain errors. On Linux, removal affects only the
Arkoren-managed anchor, not copies installed elsewhere. Removing trust does not
delete generated files or revoke certificates for clients that maintain
separate trust stores.

## Recovering from failures

### Partial file creation

If generation fails after creating a file, inspect only the reported output
paths. Do not delete an established CA or assume that every reported file is
complete. Use `certificate check` on any pair you intend to keep. Remove only
new, incomplete provisioning outputs after confirming they are not deployed,
or select fresh output paths and retry. The CLI never overwrites them for you.

### Partial trust changes

On Linux, an anchor write, sync, or close failure can leave a possibly incomplete
file at the exact reported path. The CLI does not refresh the store or delete
that pathname. Inspect the file and remove it only after confirming that it is
incomplete and not deployed; then retry with the same public root. Existing
conflicting anchors remain protected from replacement.

A store update can succeed before its refresh step fails. Follow the error's
named path, store, and refresh command to inspect or repair that partial state;
a nonzero exit does not prove rollback.

Cleanup can also fail after a native action. On macOS, staging-cleanup errors
retain whether no change was attempted, the command completed, or changes may
be partial. On Windows, native handle-cleanup errors retain completed or failed
action outcomes; a partial removal can report how many exact roots were already
removed. Completed changes remain in place. These errors do not produce a
success receipt and do not undo the action.

Inspect the reported outcome before retrying. After repair, repeat the same
operation with the same root file and verify with the client that actually uses
the store. Exact-certificate matching avoids removing unrelated roots, but does
not make the operation transactional. There is no certificate status command.

All relative file paths resolve from the command's working directory. Invalid
usage exits 2; file, validation, trust, or output failures exit 1; success exits
0. Broken pipes and short stdout writes are failures even if files or trust
have already changed.

Continue with [TLS Certificates](../http-networking/certificates.md) to load
the identity and configure application-specific trust or mutual TLS.
