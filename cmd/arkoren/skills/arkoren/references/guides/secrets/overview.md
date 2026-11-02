---
title: Secrets Overview
nav: Overview
description: Load private application credentials through a secret store without confusing storage with runtime configuration.
order: 10
summary:
  - Read current text, byte, or typed JSON values and distinguish missing secrets from provider failures.
  - Share literal name definitions without treating namespaces as schemas or authorization boundaries.
  - Validate required startup credentials and keep names, values, and owned copies out of ordinary diagnostics.
---

# Secrets Overview

A service needs credentials such as a reporting API token without putting them
in source code or its container image. A **secret store** holds those private
values outside the application; `secret.Store` reads them through your chosen
[driver](drivers.md). Provider permissions, retention, and auditing remain part
of that provider's configuration.

Reading a secret produces an ordinary Go value. The facade does not cache it,
refresh a running client, or revoke the credential in the system that accepts it.
[Rotation](rotation.md) coordinates those separate changes.

## Reading Values

With a configured `*secret.Store` named `secrets` and an operation context `ctx`,
read a provisioned text credential:

```go
token, err := secrets.String(ctx, "reporting-token")
```

On success, `token` contains the current value. `String` converts bytes to a Go
string without validating UTF-8. For binary material, use `Raw` instead; its
returned byte slice belongs to you. Azure accepts only UTF-8 text and Vault
stores JSON objects, so choose the [payload format](drivers.md#compatibility-matrix)
before provisioning.

A missing name or requested version wraps `secret.ErrNotFound`. Permission,
authentication, and connectivity failures are not missing-secret signals.
Use `errors.Is` for portable error checks rather than matching provider text.
An already-canceled context fails with its context error without changing storage.

Focused examples keep error results visible but omit routine propagation.
Handle errors before using results; the startup example below retains the
check that prevents using a missing credential.

## Typed Credentials

When one credential contains related fields, store a JSON object and share a
definition that binds its provider name to a Go type. Import `secret` from
`arkoren.dev/core/service/secret`:

```go
type ReportingCredentials struct {
	Account string `json:"account"`
	Token   string `json:"token"`
}

var reportingSecret = secret.NewSecret[ReportingCredentials]("reporting-client")
```

The definition performs no I/O and creates no provider resource. After
[provisioning](managing.md#writing-values) that JSON secret, read it through
the facade:

```go
credentials, err := secrets.Get(ctx, reportingSecret)
```

On success, `credentials` has type `ReportingCredentials`. Validate required
fields before using them: a shared definition is not a provider schema, and
independent definitions can assign different types to the same name.

Definitions are immutable and safe to share. An empty name or zero definition
reports `secret.ErrInvalidSecret`; `Err()` checks this without a read. `Get`
rejects invalid definitions before provider I/O, with an already-canceled context
taking precedence. JSON decoding errors can include the name and underlying
details; neither decoded values nor returned errors are redacted.

## Shared Names

For several JSON credentials of the same type, a namespace composes their names.
Choose a separator valid for your provider rather than assuming slash paths work
everywhere:

```go
reports := secret.NewNamespace[ReportingCredentials]("reports", "-")
archiveSecret := reports.Secret("archive")
credentials, err := secrets.Get(ctx, archiveSecret)
```

This reads the literal name `reports-archive`; it does not create a namespace in
the provider or grant access. Azure needs flat names. Google accepts a full
resource path or an unqualified ID with a configured project; slashes in a full
resource path are not valid separators inside that ID. AWS and Vault permit
path-style conventions. See [Drivers](drivers.md) before choosing deployed names.

`Derive` publishes an independent variant. `Subnamespace` joins a child prefix
with the parent's separator before applying settings for future joins:

```go
regional := reports.Subnamespace("eu")
replicaSecret := regional.Secret("replica")
```

The resulting name is `reports-eu-replica`. No escaping or normalization occurs,
and an empty separator concatenates components. Validate or unambiguously encode
untrusted components to prevent collisions. Namespaces are not authorization or
isolation boundaries.

Empty prefixes, empty child names, and zero namespaces are invalid; descendants
retain the first declaration error. `Name`, `Separator`, and `Err` inspect a
namespace. Construction callbacks run once in order, including on invalid
declarations; nil callbacks panic, and retained drafts cannot change published
values.

## Startup Loading

If a client uses its credential only during construction, load and validate the
credential before starting that client. A required-secret failure should stop
startup rather than silently select an empty value or generate a replacement.
For a safe application-owned error, import the standard-library `errors` package:

```go
token, err := secrets.String(ctx, "reporting-token")
if err != nil {
	return errors.New("reporting credential could not be loaded")
}
if token == "" {
	return errors.New("reporting credential is empty")
}
```

Only the success path passes `token` to the reporting client. The generic error
avoids exposing the provider error or secret name through ordinary startup logs;
use a protected diagnostic path if you need the underlying cause. A successful
read checks storage access, not whether the reporting API accepts that token.

Use [environment settings](../first-steps/configuration.md#environment-settings)
for nonsecret choices such as the deployment's project or vault address. Grant
each runtime identity access only to the secrets it needs. Keeping store reads
out of request handling removes those reads from the request path, but a store
outage can still prevent startup or [refresh](rotation.md#runtime-refresh).

## Private Values

Treat secret names as potentially private as well as their contents. Typed
definitions and namespaces redact identities under ordinary formatting and
structured logging, but `Name()` deliberately exposes them. Raw `Reference` and
`Value` records, strings, decoded structs, native objects, and returned errors do
not inherit that protection.

Do not log credentials or credential-bearing connection strings, include them
in public URLs, or return them in API responses. Limit which application objects
retain them. Driver diagnostic records mask selected fields only; consult the
[provider logging rules](drivers.md), particularly when supplying native clients.

Drivers copy input and returned bytes. Clearing your returned slice does not
clear storage, other copies, strings, or decoded fields; copying a `secret.Value`
record alone still shares its `Data` slice. Arkoren does not guarantee secret
zeroization. Deleting a secret does not erase values already held by consumers.

For all definitions and operations, see the
[`secret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
