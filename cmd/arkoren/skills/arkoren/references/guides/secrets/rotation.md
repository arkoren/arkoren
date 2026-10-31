---
title: Rotating Secrets
nav: Rotation
description: Replace credentials without downtime by overlapping old and new values, rotate encryption keys and signing secrets, and reload live.
order: 30
---

# Rotating Secrets

## Introduction

Rotating a secret means creating a new value, moving everything that uses it
over, and then revoking the old one. Regular rotation limits how long a leaked
value stays useful, and a rotation you have practiced is the one that works
when a value has actually leaked.

The rule that makes rotation safe is **overlap**: for a while, both the old and
the new value must work. How you achieve it depends on what the secret is: a
credential for another system, an encryption key, or a secret that others use
to verify requests. This page covers each.

## Credentials for other systems

A database password or a provider's API key is checked by the other system, so
the overlap must happen there. For a service that reads secrets at startup,
the steps are:

1. Create the new credential where it is used, for example a second database
   user or a second API key, while the old one keeps working.
2. Write it to the secret store as a new version.
3. Restart or redeploy the service, which reads the new version.
4. Revoke the old credential once no instance uses it.

Between steps 3 and 4, check that no instance still uses the old value, for
example with the provider's "last used" timestamp or the database's list of
connections. Revoking too early takes down the instances that have not
restarted yet.

## Encryption keys

Encryption keys are rotated differently, because data encrypted with the old
key must stay readable. Store the old key as a separate secret, pass it to the
encrypter as a previous key, and re-encrypt the data before removing it:

```go
current, err := secrets.String(ctx, "prod/app-key")
previous, err := secrets.String(ctx, "prod/app-key-previous")
```

These values use the base64 text format from the encryption guide. Parse each
with `key.ParseShared` from `arkoren.dev/foundation/key`, checking both read
and parse errors before constructing the cipher drivers. Drivers accept the
resulting `key.Shared` values, not encoded text or raw bytes.
[Encryption Keys](../encryption/keys.md#rotating-keys) describes the whole
procedure, and [Storing Encrypted Data](../encryption/storing.md#re-encrypting-stored-values)
the re-encryption job.

## Secrets others verify

Some secrets are checked by your service, not by another system: the secret a
payment provider uses to sign its webhooks, or a shared key a partner uses to
sign requests to you. During the overlap, accept both values. Store the
current and the previous value in one JSON secret, and try each. The
`validSignature` function below is your application's verifier for the
provider's signing format, not an Arkoren helper:

```go
type WebhookSecrets struct {
	Current  string `json:"current"`
	Previous string `json:"previous,omitempty"`
}

func (secrets WebhookSecrets) verify(payload []byte, signature string) bool {
	for _, key := range []string{secrets.Current, secrets.Previous} {
		if key != "" && validSignature(payload, signature, key) {
			return true
		}
	}

	return false
}
```

Once the other side has switched to the new value, write the secret again
without `previous`. Compare signatures in constant time, for example with
`hmac.Equal`, inside `validSignature`.

## Picking up new values without a restart

Reading at startup means a new value takes effect when instances restart. When
a restart is too slow or too disruptive, for example for a value rotated by
the store itself every few hours, read the secret again periodically and swap
it in atomically.

Read the first value at startup and fail startup if it is missing, as in
[Loading secrets at startup](overview.md#loading-secrets-at-startup). Store
it with `refreshing.value.Store(&initial)` before calling either `Run` or
`Get`; otherwise `Get` dereferences nil. `Refreshing` is application code,
not a shipped helper. Run it under your application's managed background
work, and pass a positive refresh interval to `time.NewTicker`.

The helper imports standard-library `context`, `log/slog`, `sync/atomic`, and
`time`, and `secret` from `arkoren.dev/core/service/secret`:

```go
type Refreshing struct {
	value atomic.Pointer[string]
}

func (refreshing *Refreshing) Run(ctx context.Context, secrets *secret.Store, name string, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			v, err := secrets.String(ctx, name)
			if err != nil {
				slog.WarnContext(ctx, "secret refresh failed; keeping previous value")
				continue // keep serving the last good value
			}
			refreshing.value.Store(&v)
		}
	}
}

func (refreshing *Refreshing) Get() string {
	return *refreshing.value.Load()
}
```

A failed refresh keeps the last good value. That preserves service only while
the old credential is still accepted; if the store stays unavailable past
revocation, calls using it fail. Monitor refresh failures and choose the
interval well inside the overlap: if the old value
stays valid for an hour after rotation, refreshing every few minutes leaves
plenty of margin.

The warning deliberately excludes the secret name and native error text, which
can contain sensitive provider details. Add only safe failure classifications
to ordinary diagnostics. A stored version reference also does not retain its
history; follow the [version lifetime rules](managing.md#versions) when pinning
reads instead of reloading current.

This pattern suits values used on each call, such as an API key sent with
every request. A value used once to build something, such as a database
password inside a connection pool, needs that thing rebuilt as well, and a
restart is usually simpler.

For every function, see the [`secret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
