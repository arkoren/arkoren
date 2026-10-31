---
title: Secrets Overview
nav: Overview
description: Read database passwords and API keys from a secret store at startup, keep them out of logs and responses, and use the memory driver in tests.
order: 10
---

# Secrets Overview

## Introduction

Every service has values that must stay private: the database password, the
API key of the payment provider, the [encryption key](../encryption/keys.md).
They cannot live in the source code or the container image, where everyone
with access to either would see them. Environment variables are a step better,
but they are visible to anyone who can inspect the process or its deployment,
and changing one usually means redeploying.

A **secret store** is a service built for these values. Managed stores encrypt
them at rest, control who may read each one, support access auditing, and keep
a history of versions. Configure auditing and retention for the store you
choose. Arkoren's secret service gives you one interface to the common
stores: AWS Secrets Manager, Azure Key Vault, Google Cloud Secret Manager, and
HashiCorp Vault, plus a memory driver for development and tests. Your code reads
secrets through the `secret.Store` facade and does not change when the store
does.

This page covers reading secrets and keeping them private. Writing secrets and
their versions are in [Managing Secrets](managing.md), changing them safely in
[Rotating Secrets](rotation.md), and each store in [Secrets Drivers](drivers.md).

## Creating a store

Run `go get arkoren.dev/core` in your application's module. For local development
or tests, create a store with fixture values using the included memory driver:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/memorysecret"
)

secrets := secret.New(memorysecret.New(func(secrets *memorysecret.Builder) {
	secrets.Values(map[string][]byte{
		"prod/db-password": []byte("local-password"),
	})
}))
```

This process-local store needs no lifecycle hooks and loses all versions on process exit.
Use fixture credentials only; memory is not a protected production vault.
For production, configure your [chosen secret driver](drivers.md).
Each cloud driver finds its credentials the way that cloud's own tools do, for
example from the instance's role on AWS or the workload identity on Google
Cloud, so production deployments usually need no credentials in the
configuration at all. Give the service's identity permission to read only the
secrets it uses, so that a compromised service cannot read every secret in the
account.

Focused examples keep error results visible but omit routine error handling.
Handle those errors before using results. The startup example below retains
the check that prevents a service from starting without its credential.
Subsequent snippets use your operation or startup context `ctx`.

## Reading secrets

`String` returns a secret as text, which suits passwords and API keys:

```go
password, err := secrets.String(ctx, "prod/db-password")
```

Secrets that hold several values are usually stored as JSON. With an existing
`prod/stripe` JSON secret, [provisioned separately](managing.md#writing-secrets),
declare one shared
`secret.Secret[T]` definition to bind the provider name to its decoding type,
then pass it to `Get`. Keep the definition in the application package that owns
the credential and reuse it wherever that credential is read:

```go
type Stripe struct {
	SecretKey     string `json:"secret_key"`
	WebhookSecret string `json:"webhook_secret"`
}

var stripeSecret = secret.NewSecret[Stripe]("prod/stripe")

stripe, err := secrets.Get(ctx, stripeSecret)
```

On success, `stripe` has type `Stripe`; callers do not independently choose its
type. Definitions are immutable and safe to share. With a live context, empty
names and zero definitions fail with `secret.ErrInvalidSecret` before contacting
the provider; an already-canceled context takes precedence. `Err()`
also checks a definition without reading it. Names are literal; provider naming
restrictions still apply. Ordinary formatting and structured logging redact the
definition's identity; `Name()` exposes it deliberately.

This is a shared Go definition, not schema enforcement. Independently declaring
the same name with another type remains possible, and JSON-compatible drift
(such as a missing field decoding to its zero value) can still succeed. Validate
required application values after decoding. Decoded values and returned errors
are not redacted; handle them without logging sensitive details.

For binary secrets on a driver that accepts arbitrary bytes, `Raw` returns
those bytes without application decoding. Azure requires valid UTF-8 text and
Vault returns a re-encoded JSON object; see the [payload matrix](drivers.md#compatibility-matrix).
All three read the current version; reading an older one
is described in [Managing Secrets](managing.md#versions).

A secret that does not exist returns an error wrapping `secret.ErrNotFound`.
Other errors, such as missing permissions or an unreachable store, are returned
as the store reported them. A read whose context is already canceled fails
with an error wrapping the context's error.

## Sharing a provider-specific prefix

Keep standalone `NewSecret` definitions for fixed credentials. When related JSON
secrets share a value type and provider naming convention, a namespace removes
repeated prefix construction. Choose the separator explicitly: there is no
separator that is valid for every secret provider. This example uses AWS Secrets
Manager or a Vault KV v2 path, not Azure or an unqualified Google secret ID:

```go
type DatabaseCredentials struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

database := secret.NewNamespace[DatabaseCredentials]("tenants", "/")
primary := database.Secret("primary") // primary.Name() == "tenants/primary"
credentials, err := secrets.Get(ctx, primary)
```

The result has type `DatabaseCredentials`. Validate required fields before
using it, just as with a standalone definition. Construction neither creates a
provider secret nor grants access or enforces a schema. Provision the exact name
and permissions separately. See [provider naming constraints](drivers.md#compatibility-matrix)
before choosing names: Azure uses flat names such as `tenants-primary`; Google
resource paths and secret IDs are different things.

Ordinary formatting and structured logging redact namespace identities;
`Name` exposes them deliberately. `Name`, `Separator`, and `Err` inspect a
namespace. `Derive` returns an independent
variant; `Subnamespace` joins another prefix before applying callbacks to its
future settings. For example, to retain `/` for the existing child prefix but
use `-` for its future children:

```go
regional := database.Subnamespace("eu", func(namespace *secret.NamespaceBuilder) {
	namespace.Separator("-")
})
replica := regional.Secret("replica") // replica.Name() == "tenants/eu-replica"
```

This is still an AWS/Vault naming example, not a portable provider path.
An empty separator concatenates names. Names are literal, without escaping or
normalization; `a/b` plus `c` and `a` plus `b/c` can address the same secret.
Validate allowed components or encode them with a provider-valid unambiguous
scheme before composing untrusted names. A namespace is not an authorization or
isolation boundary. Child names are strings; convert numeric IDs explicitly
with standard-library `strconv`.

Empty namespace or child names and zero namespaces report
`secret.ErrInvalidSecret`. The first error survives derivation and reaches child
definitions. Callbacks run once in order even for invalid namespaces; a nil entry
panics. Published values detach from retained drafts and remain immutable.

## Loading secrets at startup

Most services read their secrets once, while starting, and pass the values on
like any other configuration. This keeps secret stores out of request handling,
so a slow or unavailable store can delay a deployment but never a request:

Use standard-library `fmt` and `net/url`, and import `postgresdatabase` from
`arkoren.dev/driver/postgres/postgresdatabase` for this example. Install it
with `go get arkoren.dev/driver/postgres` before configuring the database.

```go
password, err := secrets.String(ctx, "prod/db-password")
if err != nil {
	return fmt.Errorf("load prod/db-password: %w", err)
}

dsn := url.URL{Scheme: "postgres", User: url.UserPassword("app", password), Host: "db.internal", Path: "/app"}
driver, err := postgresdatabase.New(dsn.String())
if err != nil {
	return err
}
```

A missing secret should stop the service from starting, with an error that
names the secret, as above. That turns a configuration mistake into a failed
deployment rather than a running service that fails on its first request.
[Loading from the environment](../first-steps/configuration.md#loading-from-the-environment)
shows where this fits among your other settings.

Name secrets by environment and purpose, such as `prod/db-password` and
`staging/db-password`, and read the prefix from configuration. The same code
then runs in every environment, and each environment's identity can be allowed
to read only its own prefix.

## Keeping secrets secret

Reading a secret is only half the job. The value must not leak afterward:

- Never log a secret, and never add one to an error message. Native errors can
  already contain sensitive provider details; wrapping one does not sanitize
  it. Report a safe failure classification in ordinary logs instead.
- Never return a secret in an API response, even to administrators.
- Keep values in the variables that need them, rather than in a global map
  that any code can read.
- Never put a secret in a public URL, request path, or query string, where
  proxies, browsers, and access logs keep it. A credential-bearing connection
  string, such as the database DSN above, is itself a secret: never log it.

Some Arkoren diagnostic records mask specific credential fields. Vault's Config
masks its static Token, not a shared Auth formatter or Address; AWS records mask
credential providers by type, not arbitrary endpoint text. Native objects,
operation errors, and decoded payloads are not recursively sanitized. Follow the
[driver-specific logging rules](drivers.md), including GCP's supplied-client
policy. For your own settings type, implement `LogValue` to omit the secret:

Use standard-library `log/slog` for this method. This only controls structured
logging through `slog`; it does not redact ordinary formatting or JSON encoding.

```go
type Settings struct {
	APIURL string
	APIKey string
}

func (settings Settings) LogValue() slog.Value {
	return slog.GroupValue(slog.String("api_url", settings.APIURL), slog.String("api_key", "REDACTED"))
}
```

## Testing and local development

The memory driver keeps secrets in the process. Give it the values your code
expects:

```go
secrets := secret.New(memorysecret.New(func(secrets *memorysecret.Builder) {
	secrets.Values(map[string][]byte{
		"prod/db-password": []byte("local-password"),
		"prod/stripe":      []byte(`{"secret_key":"sk_test_123"}`),
	})
}))
```

The same setup works for local development, filled from a `.env` file that is
never committed, so the code that reads secrets runs unchanged on a laptop. To
check a driver of your own, run the `secrettest` suite, as described in
[Conformance Suites](../testing/conformance-suites.md).

For every function, see the [`secret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
