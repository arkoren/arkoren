---
title: Managing Secrets
nav: Managing
description: Create, update, and delete secrets through the facade, observe mutation outcomes without payloads, and read specific versions through the driver.
order: 20
---

# Managing Secrets

## Introduction

Most services only read secrets created by people, infrastructure tools such as
Terraform, or the store's own rotation features. Provisioning commands, admin
jobs, and tests sometimes need to write them. Use the `secret.Store` facade for
those writes so you can observe their outcomes without recording secret contents.

This page covers those writes, the versions every write creates, and what
deleting means in each store.

## Writing secrets

> [!WARNING]
> Give write access only to the tools that need it. A service that only reads
> secrets should run with an identity that cannot write them, so that a
> compromised service cannot replace a credential with one an attacker knows.

Examples use `secret` from `arkoren.dev/core/service/secret` and the
standard-library `errors` and `fmt` packages. `ctx`, `token`, and `newSecret`
come from your provisioning command.

Use the `secrets` facade created in [the overview](overview.md#creating-a-store).
`Put` creates a secret, or adds a new version when it already exists, and returns
the value it wrote with its new version:

```go
v, err := secrets.Put(ctx, "prod/api-token", []byte(token))
```

`Update` only adds a version to an existing secret, and fails with an error
wrapping `secret.ErrNotFound`, creating nothing, when the name does not exist.
Use it when a missing secret means a typo rather than a first run, so the
mistake is reported instead of creating a second, unused secret:

```go
_, err := secrets.Update(ctx, "prod/stripe-webhook", []byte(newSecret))
if errors.Is(err, secret.ErrNotFound) {
	return fmt.Errorf("secret prod/stripe-webhook does not exist: create it first")
}
```

This is not a portable compare-and-set API. Azure checks existence with a read
before writing, without a native version precondition. Vault uses the observed
native version, but that check cannot distinguish deletion/recreation with a
reused version number. [Secrets Drivers](drivers.md) explains those boundaries.

Some stores restrict values too: Azure requires valid UTF-8 text; Vault requires
non-null JSON objects and rejects numbers that change under native float64 JSON
serialization. Use explicit JSON strings for exact identifiers or decimals on
Vault. Choose provider-valid names and payloads before provisioning.

To count provisioning outcomes without exposing the credential, configure the
facade with your existing telemetry service and event bus. Here `driver` is your
configured secret driver, `observability` is a started `*telemetry.Telemetry`,
and `bus` is your `*event.Bus`. The construction callback uses
`secret` from `arkoren.dev/core/service/secret`:

```go
secrets := secret.New(driver, func(secrets *secret.Builder) {
	secrets.Telemetry(observability)
	secrets.Events(bus)
})
```

With telemetry enabled, each `Put`, `Update`, or `Delete` emits one
`secret <operation>` span, increments `arkoren.secret.operations`, and measures
`arkoren.secret.operation.duration` in seconds.
The operation label is `arkoren.secret.operation`. Failures add only the bounded
`error.type`: `secret.ErrNotFound`, `context.Canceled`,
`context.DeadlineExceeded`, or `other`. Names, versions, values, payload sizes,
and native error types or messages are omitted. Omitted or nil telemetry follows
the process default on each call; a disabled default records nothing.

The bus receives `secret.MutationSucceeded` or `secret.MutationFailed` after the
driver returns. Their `secret.MutationEvent` contains only `Operation` and
`ErrorType`, using the same classes and an empty class on success. Nil events
disables notifications. Delivery is synchronous and best effort; listener errors
or panics cannot replace the result. A failed operation can still have made a
partial provider change, so always handle the returned error.

These notifications describe this facade's observed API outcomes, not global
rotation, completed rollout, durable delivery, or an authoritative audit.
`Driver()` and native-client calls bypass both channels; provider-managed
rotation is also outside their scope. Continue using the overlap and reload
procedure in [Rotating Secrets](rotation.md), rather than treating an event as
proof that consumers use the new value.

Reads emit no mutation notifications. `Raw`, `String`, and typed `Get` each
record one `secret get` provider observation. JSON decoding happens afterward:
a decode error from `Get` does not mark a successful provider read as failed.
Returned errors retain their original details, so do not log them blindly.

## Versions

Every successful write creates a new, immutable concrete **version** within the
existing logical secret. Later writes to that secret do not change an earlier
version's payload. Reads select current unless you ask for another; a selected
version must still be retained and readable under provider policy. Reading one
goes through the driver, with a `secret.Reference`:

```go
old, err := secrets.Driver().Get(ctx, secret.Reference{Name: "prod/api-token", Version: v.Version})
```

An empty `Version` selects the current value, so a `Reference` with only a
name reads the same as `secrets.Raw`. A version that was never issued returns
an error wrapping `secret.ErrNotFound`.

Version identifiers come from the store and look different in each: UUID-style
tokens on AWS, system-generated strings on Azure, and decimal selectors on Google
Cloud and Vault. Treat them as opaque strings: store and pass them unchanged;
do not derive or sort them. Provider aliases such as Google's `latest` can move
and do not pin contents. AWS `Reference.Version` selects `VersionId`, not a staging
label.

Pinning a version is useful when a value must not change under you, for
example while a migration job decrypts data with a specific key: record the
concrete version at the start and keep the secret and version retained and
readable until the job ends. Saving a reference does not retain either.

Permanent deletion followed by recreation under the same name creates a new
logical secret. The same name/version selector can then select different contents:
memory and Vault histories can restart at `"1"`. Reuse is permitted, not required;
providers can offer stronger identities or delay name reuse. Recovering a retained
secret is not recreation. Immutability covers concrete payloads, not provider
metadata, access policy, or movable aliases.

## Deleting secrets

> [!WARNING]
> Deletion makes every version unavailable to the application and can be
> permanent, depending on the provider. Check that no reader or retained backup
> still needs the secret. Rotate by writing a new version, not by deleting it.

`secrets.Delete(ctx, name)` removes a secret and all its versions. Afterwards,
reading the name or any of its versions fails with `secret.ErrNotFound`, and
deleting a name that does not exist fails the same way. Later recovery or
recreation can make the name readable again; an old reference is not a permanent
identity across recreation.

Managed stores keep deleted secrets for a while so they can be recovered: AWS
schedules deletion after a recovery window, and Azure keeps deleted secrets for
the vault's retention period. During that time the name usually cannot be
reused, so a test or a setup script that deletes and then recreates the same
name fails. Use fresh names in tests that run against a real store, and do not
rely on deleting a secret to make its name free again.

## Values in memory

Data passed to and returned from a driver is copied, so you may modify or
clear your own slices without affecting the store or other readers. A value
read from the store is an ordinary Go value afterward: it stays in memory
until the garbage collector reclaims it, and the guidance in
[Keeping secrets secret](overview.md#keeping-secrets-secret) applies to it.

For every function, see the [`secret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
