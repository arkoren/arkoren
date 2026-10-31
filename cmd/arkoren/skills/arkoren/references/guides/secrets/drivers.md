---
title: Secrets Drivers
nav: Drivers
description: Choose the secret store of your cloud, Vault, or the memory driver, install and connect each, and know how each handles deletion.
order: 40
---

# Secrets Drivers

## Introduction

Use the **memory** driver for tests and local development. In production, prefer
the secret store of your cloud when its workload identity can authenticate your
service. Configure that identity and grant access explicitly; running in a cloud
does not automatically grant secret access. Use **Vault** when you run on several
clouds or your own hardware, or your organization already operates it.

The shared contract covers reads, writes, concrete versions, and
`secret.ErrNotFound`. Payload formats, naming, retention, and authentication
still differ. Choose those provider constraints before sharing definitions
between deployments; a shared interface does not verify production permissions.

## Compatibility matrix

Compare secret operations exposed by Arkoren, not every administration feature
of the provider. All drivers can read the current or a selected version and
create new versions. Version identity applies within an existing logical secret;
[permanent deletion and recreation](managing.md#versions) can reuse selectors.
Version history remains subject to provider retention and access policy.

| Driver | Versioned reads | Arbitrary binary values | Shared across processes | Permanent `Delete` | Native client access | Observed facade mutations | Namespace-derived JSON reads |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | Yes | Yes | No | Yes | No | Yes | Yes — Facade composition of literal fixture names. |
| [AWS Secrets Manager](#aws-secrets-manager) | Yes | Yes | Yes | No — Schedules deletion with the provider's recovery window. | Yes | Yes | Yes — Final names must satisfy Secrets Manager naming rules. |
| [Azure Key Vault](#azure-key-vault) | Yes | No — Values must be valid UTF-8 text; encode binary data yourself. | Yes | No — Uses the vault's soft-delete retention policy. | Yes | Yes | Yes — Use flat provider-valid names, not slash paths. |
| [Google Cloud Secret Manager](#google-cloud-secret-manager) | Yes | Yes | Yes | Yes | Yes | Yes | Yes — Resource paths and unqualified secret IDs have different rules. |
| [HashiCorp Vault](#hashicorp-vault) | Yes — Explicit versions must be positive decimal selectors. | No — Writes require non-null JSON objects; numbers that change under native float64 JSON encoding are rejected. | Yes | Yes — Deletes key metadata and version history. | Yes | Yes | Yes — Compose KV v2 paths within the configured mount. |

Arbitrary binary values means bytes round-trip without application encoding;
Azure needs an encoding such as base64, and Vault needs a JSON object. Shared
access assumes the same provider account, vault, project, or mount and secret
name, plus permissions for each process. Memory is process-local and loses all
versions on exit.

Permanent `Delete` means Arkoren removes the secret and all versions rather than
scheduling deletion or retaining a recoverable secret. It does not describe
provider backups. Never delete a production secret to rotate it. AWS and Azure
native clients expose administration outside this contract; their existence
does not make permanent deletion a portable operation. Native client access
assumes a real SDK client rather than a nonnative test double.

Observed facade mutations means `secret.Store.Put`, `Update`, and `Delete`
support payload-free telemetry and optional success/failure notifications for
every driver. Configure them as shown in [Managing Secrets](managing.md#writing-secrets).
These describe observed API outcomes, not global rotation, rollout, authoritative
audit, or durability. Raw driver/native access and provider-managed rotation bypass
them; version-pinned reads remain raw driver operations without facade telemetry.

All five drivers retain the same raw `Reference`/`Value` contract. Shared
`secret.Secret[T]` definitions belong to the facade, so typed JSON reads work with
every row above without changing provider payload formats or version selection.
They do not enforce a schema or remove the text/object restrictions in the matrix.

Optional [namespaces](overview.md#sharing-a-provider-specific-prefix) also operate
in the facade, not as native provider namespaces. All five rows receive the
final literal name; construction creates no provider resource or permission.
Choose the required separator for the provider rather than copying one across
clouds. Memory accepts fixture names; AWS names and Vault KV v2 paths can use `/`.
Azure names are flat and allow letters, digits, and hyphens, so use
`secret.NewNamespace[Credentials]("tenants", "-")` with your JSON-compatible
application type `Credentials`, and keep every component provider-valid.
For Google, `projects/my-project/secrets` with separator `/` can prefix a valid
secret ID such as `db-password`; `/` belongs to the full resource path, not
inside that ID. If you configure `Project` and use unqualified IDs, use a valid
flat convention such as `tenants-primary` instead. Namespace validation checks
nonempty names, not these provider restrictions or provider length limits.

`Update` is not a portable compare-and-set API. Vault checks the version it
observed before writing; Azure checks existence with a read and then writes
without a native version precondition. See their behavior sections before
relying on concurrent provisioning updates.

## Memory

The memory driver keeps secrets and their versions in a map inside the process.
It is included in core, so no separate installation is needed. Use fixture values
only: process-local storage is not durable and is not a production secret vault.

### Connecting

Seed the values your application expects and wrap the driver:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/memorysecret"
)

driver := memorysecret.New(func(secrets *memorysecret.Builder) {
	secrets.Values(map[string][]byte{
		"db-password": []byte("local-password"),
	})
})
secrets := secret.New(driver)
```

Construction seeds version `"1"` without I/O. No start or stop hook is needed.

### Configuration

`Values` replaces and copies the seed map and its byte slices. Nil or empty
clears the seeds; without it the store starts empty. Later mutation of your map
or a retained construction draft does not change the store. There are no retry,
timeout, or native-client settings.

### Reading a fixture credential

With a request or test context named `ctx`, read the seeded value:

```go
password, err := secrets.String(ctx, "db-password")
```

On success, `password` is `"local-password"`. Do not print real credentials;
this literal is only a fixture. [Managing Secrets](managing.md) covers versioned
writes with the same driver.

### Behavior and limits

Operations are safe for concurrent use. Missing names and versions wrap
`secret.ErrNotFound`; an already-canceled context changes nothing. Reads and
writes copy byte slices. Contents disappear when the process exits. There are
no shipped companion adapters or external services to start.
Deleting a name removes its history; a later `Put` starts again at version
`"1"`. A saved name/version pair can therefore select different fixture bytes
after recreation, not recover the deleted fixture.

### API references

For every option, see
the [`memorysecret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret/memorysecret).

## AWS Secrets Manager

The AWS driver uses AWS Secrets Manager. Provision secrets in your selected
region and allow the service identity `secretsmanager:GetSecretValue` for the
required resources. Provisioning tools additionally need create, write, or
delete permissions; customer-managed encryption keys can require KMS permissions.

### Installation

Run this command from your application's module directory:

```sh
go get arkoren.dev/driver/aws
```

### Connecting

It reads credentials and the region from the standard AWS sources: the
environment, the shared configuration files, or the role of the instance or
container. Set the region explicitly when it is not configured there:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/aws/awssecret"
)

driver, err := awssecret.New(ctx, func(secrets *awssecret.Builder) {
	secrets.Region("eu-west-1")
})
secrets := secret.New(driver)
```

`ctx` is your startup context. Construction loads SDK configuration, but does
not test secret access. A successful read is the access check. No lifecycle
hook is needed.

### Configuration

`Region` overrides region discovery, `Credentials` overrides the default
credential provider, and `Endpoint` overrides the service URL. Leave credentials
on the workload-identity chain in production. The deprecated `Name` setter is a
compatibility no-op: it retains no driver setting and does not select a secret.
Pass explicit names to operations or secret definitions. The default endpoint
is AWS. Configure
timeouts and retries on an AWS SDK client when you need more control; there are
no Arkoren retry or timeout builder settings. Bound operations with their contexts.

### Reading a database password

Use the [startup credential read](overview.md#loading-secrets-at-startup) with
`secrets`. A successful read supplies the value to your database configuration;
failure stops startup rather than using an empty password.

### Behavior and limits

Secrets created in the AWS console are usually stored as text, and the driver
reads them as such; values written through the driver are stored as binary.
Names are AWS secret names or identifiers accepted by Secrets Manager; version
IDs are opaque AWS `VersionId` strings. Missing or pending-deletion secrets read
as `secret.ErrNotFound`.
Deleting a secret schedules it for deletion after AWS's recovery window, during
which its name cannot be reused.

Concrete version IDs select immutable payloads within that secret resource;
AWS staging labels are movable selectors, not concrete version identities.
The driver passes an explicit `Reference.Version` as `VersionId`, not as a
staging label. After permanent deletion, a same-name secret has a different ARN.
Do not infer a permanent cross-recreation identity from the name/version pair.
Retained old versions can also become unavailable under provider policy.

### Using a configured AWS SDK client

If you already configure SDK transport, retry, or credential policy, wrap that
client instead. Here `config` is your existing `aws.Config`:

```go
import "github.com/aws/aws-sdk-go-v2/service/secretsmanager"

driver := awssecret.NewFrom(secretsmanager.NewFromConfig(config))
native := driver.Client()
```

`native` exposes SDK operations outside the contract, such as describing a
secret. The driver does not copy or close the supplied client. `Client` is nil
for nonnative test doubles. No other secret-related package ships in this module.

The reporting-only `awssecret.Config` masks credential-provider contents by
type without retrieving credentials or invoking the provider's formatter.
Endpoint and Name text are not sanitized. Native credential providers remain
shared; their own formatting and native errors are not covered by that masking.

### Running locally

With Docker installed, run an isolated MiniStack instance on loopback:

```sh
docker run --rm --name arkoren-secrets-aws -p 127.0.0.1:4566:4566 -e AWS_ACCESS_KEY_ID=000000000000 -e AWS_SECRET_ACCESS_KEY=test -e AWS_DEFAULT_REGION=us-east-1 ministackorg/ministack:1.5.8
```

Wait for MiniStack to report readiness. In another terminal, run your application
with the following development-only construction callback and the same
`awssecret.New(ctx, configure)` wiring:

```go
import "github.com/aws/aws-sdk-go-v2/credentials"

configure := func(secrets *awssecret.Builder) {
	secrets.Region("us-east-1")
	secrets.Endpoint("http://localhost:4566")
	secrets.Credentials(credentials.NewStaticCredentialsProvider("000000000000", "test", ""))
}
```

Use only synthetic credentials against this HTTP endpoint. Import `errors` from
the standard library for the assertion below. Create and read an
isolated fixture to verify the connection:

```go
name := "local-check-" + rand.Text() // crypto/rand
written, err := driver.Put(ctx, name, []byte("fixture"))
if err != nil {
	return err
}
read, err := driver.Get(ctx, secret.Reference{Name: name, Version: written.Version})
if err != nil {
	return err
}
if string(read.Data) != "fixture" {
	return errors.New("local secret round-trip mismatch")
}
```

Stop and remove the container with `docker stop arkoren-secrets-aws`; this
discards its isolated data. Emulator success does not verify AWS IAM permissions.

### API references

For every option, see the [`awssecret` API reference](https://pkg.go.dev/arkoren.dev/driver/aws/awssecret).

## Azure Key Vault

The Azure driver uses Azure Key Vault. Create a vault and grant your application
identity secret-read access, such as the Key Vault Secrets User role. Keep
write/delete permissions on provisioning identities rather than request-serving ones.

### Installation

Run this command from your application's module directory:

```sh
go get arkoren.dev/driver/azure
```

### Connecting

The required setting is the HTTPS address of your vault, and credentials come from
Azure's default sources, such as a managed identity:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/azure/azuresecret"
)

driver, err := azuresecret.New(ctx, "https://my-vault.vault.azure.net/")
secrets := secret.New(driver)
```

Construction validates the URL and creates SDK objects without contacting the
vault. Read a secret to verify permissions. There are no lifecycle hooks.

### Configuration

There are no optional builder settings. The default Azure credential chain
supports managed identity and developer credentials. If you need custom
credentials, transport, timeouts, or retry policy, construct a native client as
shown below. Use operation contexts to bound reads.

### Reading a database password

Use the [startup read](overview.md#loading-secrets-at-startup) with the Key Vault
name `prod-db-password` instead of `prod/db-password`. It supplies the credential
only after a successful read; do not continue startup on a read error.

### Behavior and limits

Key Vault names allow only letters, digits, and dashes, so use names such as
`prod-db-password` rather than paths. Deleted secrets are kept for the vault's
retention period, and writing to such a name fails with an error wrapping
`azuresecret.ErrSoftDeleted` until the secret is recovered or purged. Recover a
secret you still need; purging irreversibly removes it and may be blocked by
purge protection. Values must be valid UTF-8 text; invalid bytes are rejected
before a native write instead of being replaced by JSON encoding. Encode binary
keys as base64 before writing and decode after reading. Missing names or versions
wrap `secret.ErrNotFound`.

`Update` reads the current secret before writing, so it needs read and write
permission. This is an existence check, not a compare-and-set transaction:
concurrent changes are not protected by a native version precondition.
Already-canceled raw driver operations return the context error before local
UTF-8 or SDK parameter validation and send no provider request.

SetSecret creates a new system-generated version; changing native properties
cannot edit an existing version's value. Metadata and availability can still
change. Soft-delete recovery restores a retained resource; it is not creation
of a new logical secret. Neither this contract nor its opaque version strings
promise a permanent identity across deletion and recreation.

### Using custom Azure credentials

For an existing `credential` implementing `azcore.TokenCredential`, construct
the SDK client and retain access to its additional operations:

```go
import "github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"

native, err := azsecrets.NewClient(vaultURL, credential, nil)
driver := azuresecret.NewFrom(native)
```

Configure SDK client options instead of nil when needed. `driver.Client()`
returns this client, which exposes recovery and other native operations outside
the portable contract. It is not copied or closed by the driver. No emulator or
additional secret adapter ships here; use memory for offline development.
Keep native error details and any caller-enabled SDK body logging out of
ordinary diagnostics; wrapping a client does not sanitize its logging policy.

### API references

For every option, see the [`azuresecret` API reference](https://pkg.go.dev/arkoren.dev/driver/azure/azuresecret).

## Google Cloud Secret Manager

The Google Cloud driver uses Secret Manager. Enable the Secret Manager API in
your project and grant the runtime identity Secret Manager Secret Accessor on
the needed secrets. Creating or deleting secrets needs additional permissions.

### Installation

Run this command from your application's module directory:

```sh
go get arkoren.dev/driver/gcp
```

### Connecting

Credentials come from Application Default Credentials. Using your startup context
`ctx`, construct the driver with your project:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/gcp/gcpsecret"
)

driver, err := gcpsecret.New(ctx, func(secrets *gcpsecret.Builder) {
	secrets.Project("my-project")
})
secrets := secret.New(driver)
```

The first successful read verifies access, not client construction.
The driver owns a gRPC connection: register `driver.Stop` with
[application cleanup](../application/lifecycle-and-ownership.md) immediately
after construction, and stop it after its consumers.
The construction context controls client construction and credential discovery.

`gcpsecret.New` installs a discard logger for SDK request/response and credential
logs, even when `GOOGLE_SDK_GO_LOGGING_LEVEL=debug` enables ambient SDK logging.
This preserves Application Default Credentials and the construction context.
The facade's payload-free telemetry and mutation notifications are separate;
discarding SDK logs does not disable them.

### Configuration

Use full names such as `projects/my-project/secrets/db-password`, or configure
`Project` for unqualified names. `Project` qualifies reads, version writes, and
deletion and supplies the creation parent. Fully qualified names retain their
explicit project. The empty default is not inferred from credentials. There are
no Arkoren retry or timeout settings; configure those on the SDK client and use
bounded operation contexts.

### Reading a database password

Use the [startup read](overview.md#loading-secrets-at-startup) with `db-password`
in the configured project. Do not use the slash-separated `prod/db-password`
example as an unqualified Google secret ID.

### Behavior and limits

`Put` creates a secret with automatic replication if absent, then adds a version;
`Update` only adds a version. A failed version write can leave an empty secret
created by `Put`. Empty version selects `latest`; returned versions are opaque
strings, not resource paths. Missing names or versions wrap `secret.ErrNotFound`.
Deleting removes the secret and all versions; do not use it for rotation.
Concrete version IDs start at `1` within each secret resource and increment;
permanently deleting and recreating a resource can reuse those numeric selectors.
`latest` and custom aliases can move, so pin a concrete returned version only
while that secret and version remain retained and readable.

### Using a configured Google client

For native endpoint, credential, retry, or transport configuration, build the
SDK client yourself. Here `options` is your existing `[]option.ClientOption`
from `google.golang.org/api/option`, and `ctx` is your construction context.

> [!WARNING]
> `NewFrom` preserves the supplied client's logging policy. SDK debug logs can
> contain secret payloads, including when enabled by
> `GOOGLE_SDK_GO_LOGGING_LEVEL=debug`. Configure a discard SDK logger when creating
> the client; append that option after your other client options so it takes
> precedence.

```go
import (
	"log/slog"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"google.golang.org/api/option"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/gcp/gcpsecret"
)

clientOptions := append(options, option.WithLogger(slog.New(slog.DiscardHandler)))
native, err := secretmanager.NewClient(ctx, clientOptions...)
driver := gcpsecret.NewFrom(native, func(secrets *gcpsecret.Builder) {
	secrets.Project("my-project")
})
secrets := secret.New(driver)
```

`driver.Client()` returns the retained native client for operations outside the
contract, such as version administration. `Stop` closes it even when supplied to
`NewFrom`: do not share it with consumers that outlive this driver. No emulator
or additional secret adapter ships here; use memory for offline development.
Register `driver.Stop` with application cleanup immediately after wrapping the
client; keep one cleanup owner. Native Close does not accept a context: an expired
Stop context still initiates release but cannot bound SDK connection/resolver
teardown. Contexts do not force noncooperating native dependencies to finish.

### API references

For every builder setting, see the
[`gcpsecret` API reference](https://pkg.go.dev/arkoren.dev/driver/gcp/gcpsecret).

## HashiCorp Vault

The Vault driver uses Vault's key-value secrets engine, version 2. Provision a
KV v2 mount and an authentication role whose policy allows access only to the
application's paths. Runtime readers need read access to the data paths;
provisioning tools need separate write/delete and metadata permissions.

### Installation

Run this command from your application's module directory:

```sh
go get arkoren.dev/driver/vault
```

### Connecting

The driver can use a fixed token, but in production prefer one
of Vault's authentication methods, such as Kubernetes or AppRole. With a
renewable login token, it renews in the background and logs in again when
renewal ends. A nonrenewable login token is not automatically refreshed.
Start the driver before reading, so the login happens first. The example below
uses a Vault SDK authentication method named `kubernetesAuth`, already
configured for your cluster.

Configure the Vault Kubernetes auth role to bind the workload's service account
and namespace. Construct `kubernetesAuth` with the
[Vault Kubernetes auth SDK](https://pkg.go.dev/github.com/hashicorp/vault/api/auth/kubernetes),
using that role and the pod's service-account token. That SDK package is separate
from `vaultsecret`; its setup and the Vault role are application prerequisites.

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/vault/vaultsecret"
)

driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
	secrets.Address("https://vault.internal:8200")
	secrets.Auth(kubernetesAuth)
})
secrets := secret.New(driver)
```

Construction validates SDK settings but performs no login. Register `driver.Stop`
as cleanup and `driver.Start` as startup using the
[application lifecycle](../application/lifecycle-and-ownership.md). Register your
secret-loading startup hook after `driver.Start`, so a failed login stops startup.

### Configuration

`Mount` defaults to `secret`; `Address` follows the Vault SDK defaults, including
`VAULT_ADDR`. The SDK loads `VAULT_TOKEN` automatically. A nonempty `Token`
setting overrides that environment token. With `Auth`, successful login during
`Start` replaces either token with the login token. Static tokens are not renewed
by the driver. `Logger` defaults to `slog.Default()` for failed renewal/re-login
stages and retry delays, without native diagnostic text, tokens, or response
bodies. Returned operation errors can still contain sensitive provider details.
Configure SDK transport and retry settings through a native client if needed;
that conversion leaves authentication and transport cleanup with you.

### Reading a JSON credential

Vault stores each secret as a JSON object, so write JSON objects, and read
them with `Get` into a struct. Given an existing `app/db` JSON secret with a
`password` field, fail startup if it cannot be read:

```go
type DatabaseSecret struct {
	Password string `json:"password"`
}

var databaseSecret = secret.NewSecret[DatabaseSecret]("app/db")

config, err := secrets.Get(ctx, databaseSecret)
if err != nil {
	return err
}
```

On success, `config.Password` supplies your database credential. `String` would
return the entire object as JSON text, not just its password field.

### Behavior and limits

Write payloads must be JSON objects, not `null`, arrays, or scalar values.
Reads re-encode the object, so whitespace and object-key order need not match
the input. Vault's native JSON boundary can change numeric values: Vault 1.13.3
returns `9007199254740992` after receiving `9007199254740993`. The driver now
rejects numbers whose exact value would change under float64 JSON serialization,
including nested numbers, before sending a write. Equivalent notation such as
`1e3` and `1000` is accepted; this is a value check, not a spelling check.

When an identifier or decimal must retain every digit, encode it explicitly as
a JSON string and use a string field in your Go decoding type:

```go
_, err := driver.Put(ctx, "app/invoice", []byte(`{"id":"9007199254740993","amount":"0.123456789012345678901"}`))
```

The driver does not convert numbers to strings or change the native KV envelope.
It sends accepted numeric member tokens unchanged. Existing native data is not
migrated, and reads cannot reconstruct precision lost by an earlier writer or
backend. Do not blindly retry a rejected numeric payload: choose its explicit
representation first.

Versions are decimal strings returned by Vault; pass them back unchanged.
An explicit zero, negative, or unparseable version wraps `secret.ErrNotFound`;
an empty version selects current. Missing, deleted, and destroyed payloads also
wrap that sentinel rather than returning a successful JSON null. Already-canceled
operations fail before version or JSON validation and native requests. `Delete`
uses metadata deletion and permanently removes all versions, unlike Vault's
soft-delete API. Never delete a production secret merely to rotate it.

`Update` reads the current version and uses its number as a native check-and-set
precondition, requiring read as well as write permission. A concurrent version
change fails; absence detected after a CAS rejection wraps `secret.ErrNotFound`
instead of recreating the missing secret. The driver does not retry that
rejection; native SDK retry policy remains unchanged. Metadata deletion and
recreation can restart at version `1`, so CAS cannot distinguish a replacement
history with the same current number from the old one. It is not a
cross-recreation identity guarantee.

With `Auth`, reads before successful `Start` or after `Stop` return
`vaultsecret.ErrNotStarted`. Renewable login tokens are renewed until `Stop`;
when renewal ends, re-login retries with exponential waits from one second to
one minute. A nonrenewable login token is not automatically refreshed. Static
tokens are never renewed; their lifecycle is yours. Repeated Start while ready
is a no-op. Start reports malformed native renewability instead of silently
disabling renewal.

Start and Stop serialize; arrange Stop after Start returns, not as a way to
cancel an in-progress auth callback. Stop marks managed authentication unavailable,
signals renewal, and joins both its owner and native watcher within the caller's
wait budget. An expired context still signals cleanup, but cannot interrupt an
in-flight native renewal RPC. SDK timeouts, the transport, and cooperating auth
methods determine completion. Ownership stays tracked after an early return;
restart is rejected until the old worker drains, and a later Stop can wait again.

### Using a configured Vault SDK client

If you own authentication and transport policy, wrap your existing `*api.Client`
named `native` from `github.com/hashicorp/vault/api`:

```go
driver := vaultsecret.NewFrom(native.KVv2("application"))
versions := driver.Client()
```

`versions` is the native `*api.KVv2` for version administration outside the
contract. This path does not log in, renew tokens, or close the client; you own
that work. `Client()` is nil for nonnative test doubles. No additional secret
adapter ships in this module.
Start and Stop are no-ops on this conversion, as they are for static-token
drivers without managed authentication. The native HTTP transport is not closed
by the driver. Config formatting masks only the static Token field, not Address
or a shared Auth method's formatter; keep secrets out of those diagnostics.

### Running locally

With Docker installed, run an isolated development server. It uses an insecure
root token and volatile storage: bind loopback only and never use real secrets:

```sh
docker run --rm --name arkoren-secrets-vault -p 127.0.0.1:8200:8200 hashicorp/vault server -dev -dev-root-token-id=dev -dev-listen-address=0.0.0.0:8200
```

After the server reports readiness, configure your application explicitly:

```go
configure := func(secrets *vaultsecret.Builder) {
	secrets.Address("http://localhost:8200")
	secrets.Token("dev")
}
```

Pass this callback to `vaultsecret.New`. The development server provides the
default `secret` KV v2 mount. Verify a synthetic JSON write and versioned read:

```go
name := "local-check-" + rand.Text() // crypto/rand
written, err := driver.Put(ctx, name, []byte(`{"password":"fixture"}`))
if err != nil {
	return err
}
read, err := driver.Get(ctx, secret.Reference{Name: name, Version: written.Version})
if err != nil {
	return err
}
if !bytes.Contains(read.Data, []byte(`"fixture"`)) {
	return errors.New("local secret round-trip mismatch")
}
```

Here `bytes` and `errors` are standard-library imports. Stop and remove the
container with `docker stop arkoren-secrets-vault`; all its data is discarded.
This check does not verify production auth-method or TLS policy.

### API references

For every builder setting, see the [`vaultsecret` API reference](https://pkg.go.dev/arkoren.dev/driver/vault/vaultsecret).

For the store itself, see the [`secret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
