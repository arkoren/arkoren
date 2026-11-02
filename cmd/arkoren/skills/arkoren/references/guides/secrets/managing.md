---
title: Managing Secrets
nav: Managing Secrets
description: Write versioned values, verify retained versions, observe facade outcomes, and retire secrets without mistaking deletion for rotation.
order: 30
summary:
  - Write JSON values, verify concrete versions, and distinguish updates from create-or-replace writes.
  - Handle uncertain write outcomes and retain the logical secret behind pinned version selectors.
  - Observe payload-free facade outcomes and retire secrets according to provider deletion policy.
---

# Managing Secrets

A runtime service usually reads credentials provisioned elsewhere. Use writes
from an authorized provisioning command, administrative job, or isolated test,
not as a fallback when a required startup read fails. The `secret.Store` facade
exposes writes with payload-free observations; provider policy still determines
which identity can perform them.

These operations change secret storage. They do not create a credential in the
external system that checks it or move running consumers to its new value. Use
the [rotation procedure](rotation.md) for that coordinated change.

## Writing Values

Use a configured [store](drivers.md) named `secrets` and operation context `ctx`.
Choose names and payloads that satisfy its provider. `Put` creates a missing
logical secret or adds a new current version to an existing one; it is not
create-only and has no portable idempotency key.

> [!WARNING]
> A successful write can replace the credential readers select as current.
> Give write access only to provisioning identities, and run the fixture below
> only against an isolated, disposable store—not a production vault.

For a cross-provider fixture, encode a small JSON object. Import
`encoding/json/v2` and `crypto/rand` from the standard library:

```go
type FixtureCredential struct {
	Token string `json:"token"`
}

input := FixtureCredential{Token: "fixture"}
payload, err := json.Marshal(input)
```

After successful encoding, write it under a fresh name. Lowercase the random
suffix with standard-library `strings` so it also fits flat provider naming:

```go
name := "local-check-" + strings.ToLower(rand.Text())
written, err := secrets.Put(ctx, name, payload)
```

On success, `written.Version` is the provider-issued concrete version and
`written.Data` is a caller-owned payload copy. To verify that version, import
`secret` from `arkoren.dev/core/service/secret` and select it explicitly:

```go
read, err := secrets.Driver().Get(ctx, secret.Reference{
	Name:    name,
	Version: written.Version,
})
```

After a successful read, decode the returned JSON rather than comparing byte
formatting: Vault re-encodes object members. With the standard-library `errors`
package, the success check is:

```go
var actual FixtureCredential
if err := json.Unmarshal(read.Data, &actual); err != nil {
	return errors.New("fixture decoding failed")
}
if actual.Token != input.Token {
	return errors.New("fixture version check failed")
}
```

This verifies the stored fixture without printing it. Remove it with
`secrets.Delete(ctx, name)` after the check and handle cleanup errors. AWS and
Azure retain deleted resources according to their policies; stopping an isolated
[local container](drivers.md#local-checks) discards that container's data instead.
Use a fresh name for each run rather than relying on immediate reuse.

Input and returned byte slices are detached from storage. Clearing your copy
does not clear the stored payload or any other copy. A shallow copy of a
`secret.Value` still shares its `Data` slice; strings and decoded structs are
ordinary application values, not zeroizing containers.

## Updating Values

If a missing name indicates a provisioning mistake, use `Update` rather than
`Put`. It adds a version only to an existing secret. Here `name` is a validated,
provider-valid provisioning target and `replacement` is its encoded payload:

```go
updated, err := secrets.Update(ctx, name, replacement)
if errors.Is(err, secret.ErrNotFound) {
	return errors.New("required secret is missing; provision it separately")
}
```

On success, retain `updated.Version` if you need to verify that exact write.
On missing-secret failure, the operation creates nothing. Do not interpret
permission or authentication failures as absence and fall back to `Put`.
Other provider errors remain errors; returned diagnostics can expose names or
provider details and must not be logged blindly.

`Update` is not portable compare-and-set. Azure reads for existence, then writes
without a native version precondition. Vault reads a version and uses native
CAS, but deletion/recreation can reuse that number. Inspect the
[driver limits](drivers.md) before coordinating concurrent provisioning writers.

A failed remote write does not always mean nothing changed. For example, Google
can create a resource before adding its first version fails; a timeout can also
leave the final write outcome unknown. Reconcile through authorized provider
inspection or a protected read before deciding whether to retry. Another `Put`
or `Update` can create another version; the facade does not promise exactly-once
writes or roll back partial provider changes. Already-canceled contexts fail
without changing storage, but that guarantee does not describe cancellation
after a remote request begins.

## Version Lifetimes

A **concrete version** identifies an immutable payload within one existing
logical secret. Later writes do not edit that payload. Current-value reads
select again on each call; a `secret.Secret[T]` definition does not pin the value
that existed when the definition was declared.

To repeat work against one payload, use the returned version as shown above.
Keep the logical secret and version retained and readable until that work ends.
Saving a `secret.Reference` does not retain either. Raw driver reads bypass
facade telemetry and notifications, so own their observation policy separately.

Treat version strings as opaque: pass them back unchanged rather than parsing,
sorting, or synthesizing them. AWS uses `VersionId`, not a staging label; Google
aliases such as `latest` can move. Vault expects positive decimal selectors,
but applications should still retain its returned string rather than derive one.
A never-issued version wraps `secret.ErrNotFound`; provider retention, deletion,
or access policy can also make a previously readable version unavailable.

Permanent deletion and recreation starts a new logical secret, not a continuation
of its old history. Memory, Google, and Vault can reuse numeric selectors, so a
saved name/version pair can select different contents afterward. Provider
recovery of a retained resource is not recreation. Immutability does not cover
metadata, permissions, aliases, or permanent cross-recreation identity.

## Mutation Outcomes

To count provisioning results without recording credentials, configure your
existing telemetry service and event bus when constructing the facade. Here
`driver` is configured, `observability` is a started `*telemetry.Telemetry` from
`arkoren.dev/core/service/telemetry`, and `bus` is an `*event.Bus` from
`arkoren.dev/core/event`:

```go
secrets := secret.New(driver, func(store *secret.Builder) {
	store.Telemetry(observability)
	store.Events(bus)
})
```

Each facade `Put`, `Update`, or `Delete` records one provider-operation
observation. With telemetry enabled, spans are named `secret put`,
`secret update`, or `secret delete`; counters use `arkoren.secret.operations`,
and `arkoren.secret.operation.duration` measures seconds. The operation label is
`arkoren.secret.operation`.

Failures add only the bounded `error.type`: `secret.ErrNotFound`,
`context.Canceled`, `context.DeadlineExceeded`, or `other`. Names, versions,
values, sizes, and native error types/messages are omitted. Nil or omitted
telemetry follows the process default per call; a disabled default records
nothing. The facade does not start telemetry itself.

The optional bus receives `secret.MutationSucceeded` or
`secret.MutationFailed` after the driver returns. Each `secret.MutationEvent`
contains only `Operation` and `ErrorType`; success has an empty error class.
Delivery is synchronous and best effort. Listener errors or panics do not replace
the operation result. Nil events disables notifications.

These outcomes are observations, not an authoritative audit, durable event
delivery, global rotation notification, or proof that consumers adopted a value.
Raw driver/native calls and provider-managed rotation bypass them. A failed
mutation event does not prove absence of side effects.

Reads emit no mutation events. `Raw`, `String`, and typed `Get` each observe one
provider read, named `secret get`. JSON decoding happens afterward, so a decode
failure does not mark a successful provider read as failed. Returned errors
still retain details that the telemetry deliberately omits.

## Deleting Values

Deletion retires storage, not the credential in its issuing system. Revoke a
credential there when appropriate, and check that running consumers, retained
data, and backups no longer need its stored value before deleting it.

> [!WARNING]
> `Delete` makes the name and all versions unavailable through the driver and
> can permanently remove history. Rotate by adding a version, not by deleting
> the secret. A successful delete does not erase consumer copies or backups.

With an authorized retirement target `name`, delete the logical secret:

```go
err := secrets.Delete(ctx, name)
```

After success, reads of that name and its versions return `secret.ErrNotFound`.
Deleting a missing name returns the same sentinel. Recovery or recreation can
later make a name readable again, without guaranteeing the identity of saved
selectors across recreation.

Memory, Google, and Vault remove the secret/history through this contract. AWS
schedules deletion with its recovery window; Azure uses soft-delete retention.
Those retained names cannot be assumed immediately reusable. Review
[provider deletion behavior](drivers.md#compatibility-matrix) instead of using a
delete-and-recreate loop for tests or provisioning.

For all mutation and version-selection operations, see the
[`secret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
