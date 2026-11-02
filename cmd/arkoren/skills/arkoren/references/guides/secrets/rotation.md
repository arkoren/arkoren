---
title: Secret Rotation
nav: Rotation
description: Coordinate credential replacement, consumer refresh, verification overlap, and retained encryption material.
order: 40
summary:
  - Coordinate issuer credentials, stored versions, consumer rollout, and eventual revocation.
  - Refresh validated process-local snapshots without claiming atomic adoption across consumers.
  - Plan verification overlap and retain encryption material until data and backups no longer need it.
---

# Secret Rotation

A new secret-store version changes what the next current-value read selects.
It does not change an external credential, update an existing connection pool,
or make every application instance read again. **Rotation** coordinates creating
replacement material, moving consumers to it, and retiring the old material when
it is no longer needed.

Plan the overlap before writing: if old and new credentials can both work during
the rollout, consumers can switch independently. Not every issuer supports
overlap. If it cannot, use that system's supported cutover or maintenance
procedure rather than assuming Arkoren makes the change atomic or downtime-free.

## Credential Rollout

For a reporting API token or database credential, the issuer determines which
values are accepted. Use a separately authorized provisioning identity and keep
the existing logical secret's history intact:

1. Create a replacement at the issuer while the old credential remains valid,
   if supported—for example, a second API token or database user.
2. Validate the replacement against its intended permissions without logging it.
3. Write it as a [new secret version](managing.md#updating-values), using the
   payload shape your consumers expect.
4. Restart or refresh every consumer, including workers and scheduled jobs, and
   verify that the replacement actually works there.
5. Revoke the old credential only after consumers no longer depend on it and
   the issuer's overlap requirements are satisfied.

Track rollout state without recording values or private secret names. Provider
usage records or connection inventory can help establish that old credentials
are no longer used; a successful write or mutation notification cannot. Include
infrequent jobs and dormant instances in the verification, not just the web fleet.

If a write fails, reconcile its [possibly uncertain outcome](managing.md#updating-values)
before retrying. If consumers fail after publication, keep the old credential
valid while you investigate when the issuer permits it. Reading an old stored
version can recover its bytes only while retained and authorized; it cannot
reactivate a credential already revoked at the issuer.

After a compromise, the old credential may need immediate revocation even if
that interrupts service. Choose that incident policy explicitly; overlap extends
the time an attacker can use a leaked value. Do not delete the logical secret
as a substitute for revocation.

## Runtime Refresh

Startup-loaded credentials change when the consumer restarts. Prefer that path
when a client captures credentials during construction: a database pool, for
example, may need rebuilding and draining, not merely a new string in memory.
Read and validate credentials before building the replacement client, then
retire the old client according to its lifecycle.

For a credential read on each outgoing call, application-owned refresh can
publish a complete immutable snapshot. This is not a built-in secret-store
feature. The fragments below use the `ReportingCredentials` type and
`reportingSecret` definition from [Typed Credentials](overview.md#typed-credentials),
a configured `secrets` facade, and standard-library `errors` and `sync/atomic`.

Initialize the snapshot during startup, before any reader or refresh work runs:

```go
initial, err := secrets.Get(ctx, reportingSecret)
if err != nil || initial.Account == "" || initial.Token == "" {
	return errors.New("initial reporting credential is unavailable")
}

var active atomic.Pointer[ReportingCredentials]
active.Store(&initial)
```

The guarded success path publishes one validated credential. Keep `active` at
its application's owning scope and do not copy the atomic object after use.
Never modify a published credential through its pointer.

In each supervised refresh attempt, use a bounded operation context and replace
the snapshot only after both reading and application validation succeed:

```go
next, err := secrets.Get(ctx, reportingSecret)
if err != nil || next.Account == "" || next.Token == "" {
	return errors.New("reporting credential refresh failed")
}
active.Store(&next)
```

A failed attempt leaves the old snapshot unchanged. Schedule attempts through
your [managed background work](../application/lifecycle-and-ownership.md), stop
them on shutdown, and monitor failure counts and snapshot age. Choose the
interval and retry budget well inside the issuer's overlap window. Retaining
the last good snapshot preserves access only while that credential remains valid;
define when stale credentials must stop being used instead of retrying forever.

After successful startup initialization, a caller loads one snapshot for the
whole operation:

```go
snapshot := active.Load()
token := snapshot.Token
```

Use fields from that same snapshot if a request also needs `Account`; separate
loads could span a refresh. The pointer exchange is atomic in this process only.
It does not coordinate replicas, change in-flight requests, or prove the issuer
accepts the new value. Never call Load before initialization, and never print
`token` or the snapshot.

## Verification Secrets

When another system signs requests that your application verifies, overlap has
to exist in the verifier too. Follow the provider's signing format, timestamp
rules, replay policy, and supported rotation behavior. Do not replace a
provider-specific verifier with a generic string comparison.

If the protocol supports current and previous secrets together, you can keep
them in one JSON object so a read loads the pair from one stored version:

```go
type VerificationSecrets struct {
	Current  string `json:"current"`
	Previous string `json:"previous,omitempty"`
}

var verificationSecret = secret.NewSecret[VerificationSecrets]("partner-verification")
```

Here `secret` is imported from `arkoren.dev/core/service/secret`. Validate the
current field before publishing a decoded snapshot. Pass only nonempty values
to a verifier that explicitly supports overlap; this definition does not add
multi-key support to an existing webhook adapter. If your integration fixes its
secret at construction, rebuilding that verifier is part of adoption.

Keep old verification material until the sender has switched and the protocol's
allowed delivery/retry window has ended. Then remove it in a new version and
refresh consumers again. For application-owned HMAC verification, compare
computed MACs with `hmac.Equal`; use the provider's full verification procedure
for [incoming webhooks](../webhooks/receiving-webhooks.md), including replay defenses.

## Encryption Material

Encryption keys have a different retirement condition: data encrypted with an
old key still needs that key to decrypt. Publishing a new current key does not
rewrite stored ciphertext, and revoking an old key too soon can make retained
data or backups unreadable.

Provision replacement material explicitly using the
[key command workflow](../cli/keys.md), then load the stored representation as
[typed key configuration](../keys/overview.md#load-configuration). For shared
application keys, `key.ParseShared` from `arkoren.dev/foundation/key` parses the
encoded representation; encryption drivers consume `key.Shared`, not the
secret-store version string or unparsed text. Check read and parse errors before
constructing a driver. Never generate a replacement because startup loading
failed.

Follow [Encryption Keys](../encryption/keys.md) for previous-key configuration
and [Storing Encrypted Data](../encryption/storing.md) for re-encryption.
Retain old material until live records and retained backups no longer require
it, according to your recovery policy. A pinned [store version](managing.md#version-lifetimes)
is useful only while that logical secret and version remain readable; it does
not preserve material by itself.

For the storage operations used during rotation, see the
[`secret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
