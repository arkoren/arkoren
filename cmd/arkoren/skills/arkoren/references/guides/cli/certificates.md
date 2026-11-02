---
title: Certificate Commands
description: Generate local TLS identities, check file pairs, and explicitly manage exact-root system trust.
order: 70
summary:
  - Generate protected root and server file pairs with explicit hostnames and no automatic trust installation.
  - Check certificate structure and key matching without mistaking them for deployment verification.
  - Approve exact-root system trust changes within each platform's supported scope.
  - Recover from partial file, refresh, and native-cleanup failures without assuming rollback.
---

# Certificate Commands

Local HTTPS needs both a server identity and client trust in its issuer. These
are separate decisions: generating a private root CA and a server certificate
never installs trust. Use the [installed CLI](overview.md#installing-arkoren)
before starting the application. Certificate commands and their help run
without MCP, `.env`, or endpoint settings.

## Generate Roots

> [!WARNING]
> Anyone holding a root's private key can impersonate sites to clients that
> trust that root. Generate as your normal user in a protected directory, keep
> private files and backups out of version control, and restrict parent-directory
> access. Do not elevate generation merely because trust installation needs it.

Choose a recognizable local CA name and two unused output paths:

```sh
arkoren certificate root generate --name "Example Local Development" --cert root.pem --key root-key.pem
```

Success creates a self-signed ECDSA P-256 root and an unencrypted PKCS #8 private
key. Its default validity is 5 × 365 days, and it cannot issue intermediate CAs.
Distribute only the public `root.pem`; keep `root-key.pem` private. The JSON
receipt contains `created: true`, both absolute paths in `certificate` and
`key`, and `trust_changed: false`, never the private contents.

Both destinations are required. Parent directories must already exist without
symlinked parents. Existing outputs, including dangling symlinks, are refused;
the two paths must not identify the same destination, including case-only names
in one parent. `-` does not select stdout. The CLI preflights both paths and
then creates them exclusively, never overwriting established material.

Public files use POSIX mode `0644`; private files use `0600`, subject to umask.
Windows private-file creation atomically installs a protected current-user-only
discretionary access control list (DACL), without temporarily inheriting broader
parent access. Native Windows ACL execution remains unverified. Parent-directory
protection is still your responsibility on every platform.

Pair creation is not transactional. A failure can leave an incomplete file or
only part of the pair; inspect the exact reported paths before retrying as
described under [Recover Failures](#recover-failures).

## Issue Server Certificates

With the root pair available, name every DNS name and IP address clients will
use. For a loopback server:

```sh
arkoren certificate generate --ca-cert root.pem --ca-key root-key.pem --dns localhost --ip 127.0.0.1 --ip ::1 --cert server.pem --key server-key.pem
```

This creates a new ECDSA P-256 server-auth identity: the public chain is leaf
then root, and `server-key.pem` contains its private key. Default validity is
90 days, shortened if the issuer expires sooner. The issuer must be a currently
valid self-signed signing root with matching key and the supported no-intermediate
profile.

At least one explicit subject alternative name (SAN) is required. Repeat `--dns`
or `--ip` rather than supplying comma-separated lists. Names must be bare DNS
names or IP addresses, not URLs or host:port values; no localhost or loopback
SAN is added implicitly. A connection using an unlisted name fails hostname
verification even if the client trusts the root.

The same exclusive-pair and partial-write rules apply. Load the resulting pair
with [Serving HTTPS](../http-networking/server.md#serving-https). Missing files
must fail startup, not trigger replacement identities. These commands do not
renew certificates, create intermediates or client identities, validate DNS
ownership, or contact ACME.

## Check Files

To check that the public root bundle parses, without loading a private key:

```sh
arkoren certificate check --cert root.pem
```

The JSON result has `parsed: true`, `key_matched: false`,
`expiry_checked: false`, and `trust_checked: false`. No private material is
printed. To check the server certificate's matching private key as well:

```sh
arkoren certificate check --cert server.pem --key server-key.pem
```

Success now has `key_matched: true` and includes the key path. A malformed bundle,
mismatched key, or missing file fails. Supported unencrypted PEM identities can
also contain existing RSA, ECDSA, or Ed25519 keys.

These checks establish structure and optional key matching, not validity dates,
hostname, TLS purpose, revocation, or client trust. Verify deployment by making
a request from the intended client with the intended URL and trust configuration.
A file check cannot prove the service is running or a browser trusts it.

## Choose Trust

Prefer [client-specific trust](../http-networking/certificates.md#choose-client-trust)
when only one application needs the root. System trust affects other applications
using that store. Install only a root you control and intend to trust broadly;
neither public-file generation nor structural checking grants that trust.

System trust is not universal. Browsers with separate stores, NSS, Java,
containers, remote machines, and independently configured applications can need
their own setup. Long-running clients can cache roots or TLS connections;
restart them after changes and verify the actual client. Do not disable
certificate verification to conceal a mismatch.

## Install Trust

The input must contain exactly one public self-signed CA certificate, with valid
CA constraints and certificate-signing usage. A private key, arbitrary chain,
leaf, or intermediate is not accepted. Installation also requires current
validity.

To request installation, use the public root only:

```sh
arkoren certificate root trust root.pem
```

Before mutation, stderr receives a JSON `warning` naming the action, SHA-256
fingerprint, concrete target, and system-wide effects. Inspect those values
before answering the text prompt. Confirmation defaults to no; only a complete
`y` or `yes` line approves it, ignoring case and surrounding whitespace.

If a reviewed automation procedure deliberately changes this system store,
approve it explicitly:

```sh
arkoren certificate root trust root.pem --yes
```

Noninteractive use requires bare `--yes`. `-yes`, `--yes=true`, and other
value-bearing approval forms are invalid. The warning still appears with
`--yes`. Success prints JSON with `action`, `completed: true`, `sha256`, and
`target`; it does not prove every client uses that store.

The CLI never invokes `sudo`, requests UAC elevation, or relaunches itself.
If permissions are insufficient, rerun only the trust operation with the
privileges required by the selected store. Keep generation and private-key
access under your normal user.

## Supported Stores

Unsupported systems or Linux layouts fail rather than guessing a location.
Native Windows trust execution remains unverified.

| System | Target and prerequisites |
| --- | --- |
| macOS | `/Library/Keychains/System.keychain`, administrator SSL trust, using `/usr/bin/security`; changes require root. |
| Debian/Ubuntu Linux | Existing `/usr/local/share/ca-certificates` plus executable `/usr/sbin/update-ca-certificates`. |
| Fedora/RHEL Linux | Existing `/etc/pki/ca-trust/source/anchors` plus executable `/usr/bin/update-ca-trust extract`. |
| Windows | Crypt32 operations on the LocalMachine `Root` store; administrative permissions are required for changes. |

Operations match exact DER certificate content and fingerprint, not merely
subject names. Matching installation and established absence are repeatable
without replacing unrelated certificates.

On macOS, installation sets this root's administrator policy to SSL trust.
Removal clears its administrator trust settings but keeps the public System
keychain certificate, avoiding deletion of an item other tools might use.
User-domain and vendor trust are unchanged. Unrecognized or localized
missing-record diagnostics fail closed instead of establishing absence.

On Linux, Arkoren owns only its `arkoren-<SHA-256>.crt` or `.pem` anchor in the
selected directory. Existing anchors must contain the exact canonical bytes;
conflicting files and symlinks are refused. Install and removal run the native
refresh tool even on repeat operations, allowing a repeat to repair failed
refresh. Copies installed elsewhere are not managed. Serialize changes with
other administrators' edits; refreshing the store is not transactional.

## Remove Trust

Retain the exact public root file so removal identifies the same certificate,
not every certificate sharing its name:

```sh
arkoren certificate root untrust root.pem
```

Inspect the warning's fingerprint and target before confirming. Default-no
confirmation and noninteractive `--yes` rules are the same as installation.
Removal accepts an expired root but still validates exactly one self-signed CA.
An established absence succeeds; unknown native diagnostics remain errors.

On Linux, removal affects only Arkoren's anchor. On macOS, it leaves the public
keychain item. Removing trust neither deletes your generated files nor revokes
certificates for clients using separate stores. Verify the resulting behavior
with the client that matters.

## Recover Failures

### Partial Files

After generation failure, inspect only the named output paths. Do not delete an
established CA or assume a reported file is complete. Check any pair you intend
to keep. Remove only new incomplete outputs after confirming they are not
deployed, or choose fresh destinations. There is no automatic overwrite or
rollback.

### Partial Trust

A Linux anchor write, sync, or close failure can leave an incomplete file at the
reported path. The CLI neither refreshes the store nor deletes that pathname.
Inspect it and remove it only after confirming it is incomplete and not deployed;
then retry with the same public root. Conflicting existing anchors remain
protected.

An anchor change can succeed before native refresh fails. The error names the
path, requested state, and refresh command; inspect or repair that state, then
repeat the same operation with the same root. A nonzero exit does not establish
rollback.

Native cleanup can fail after effects too. macOS staging errors distinguish no
attempt, potentially partial change, and completed native commands. Windows
handle-cleanup errors preserve completed or failed outcomes; partial removal can
report how many exact roots were removed. Completed effects remain in place,
and these errors do not produce a success receipt. Inspect the reported outcome
before retrying. There is no certificate status command.

All relative paths resolve from the working directory. Invalid usage exits 2;
file, validation, trust, or output failures exit 1; success exits 0. Broken pipes
and short stdout writes fail even after files or trust changed. Continue with
[TLS Certificates](../http-networking/certificates.md) for application loading
and client-specific trust.
