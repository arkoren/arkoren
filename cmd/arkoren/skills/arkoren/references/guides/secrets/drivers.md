---
title: Secrets Drivers
nav: Drivers
description: Choose and configure a secret provider with explicit payload, permission, retention, and lifecycle boundaries.
order: 20
summary:
  - Compare five drivers by payload format, shared storage, version selection, deletion, and native access.
  - Configure provider identities and clients with explicit startup, cleanup, and logging ownership.
  - Check disposable AWS and Vault fixtures without treating emulator success as production access verification.
---

# Secrets Drivers

Use memory for fixture credentials in tests and local development. For a deployed
service, choose the cloud store your organization operates, or Vault when you
already manage its KV v2 engine and authentication. Configure the workload's
identity and least-privilege policy explicitly: a default credential chain can
find credentials, but it does not prove they are the intended identity or grant
secret access.

The examples assume your application module already depends on
`arkoren.dev/core`. `ctx` is a startup or operation context. Check construction
errors before wrapping a driver; `secret.New` neither validates nor starts it.
Use a successful [credential read](overview.md#startup-loading) to check access,
then separately verify the credential with the system that accepts it.

## Compatibility Matrix

This comparison covers Arkoren's secret contract, not the provider's whole
administration API. Versioned reads select a retained concrete version within
one logical secret; they do not reserve its name or retain history.

| Driver | Versioned Reads | Arbitrary Bytes | Shared Storage | Permanent Delete | Native Access | Observed Mutations |
| --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | Yes | Yes | No | Yes | No | Yes |
| [AWS Secrets Manager](#aws-secrets-manager) | Yes | Yes | Yes | No — Schedules deletion with a recovery window. | Yes | Yes |
| [Azure Key Vault](#azure-key-vault) | Yes | No — Requires valid UTF-8 text. | Yes | No — Follows soft-delete retention. | Yes | Yes |
| [Google Cloud Secret Manager](#google-cloud-secret-manager) | Yes | Yes | Yes | Yes | Yes | Yes |
| [HashiCorp Vault](#hashicorp-vault) | Yes — Explicit selectors must be positive decimal versions. | No — Requires JSON objects and rejects numbers changed by native float64 JSON encoding. | Yes | Yes — Deletes metadata and all version history. | Yes | Yes |

Shared storage means processes address the same provider resource and have
permission to access it; memory is private to one process. Permanent deletion
describes removal through the contract, not erasure of provider backups or
application copies. Never use deletion as a rotation step.

Observed mutations are the facade's payload-free telemetry and optional
[notifications](managing.md#mutation-outcomes). Native access and raw driver calls
bypass them. All drivers support the facade's typed JSON definitions and literal
namespaces, subject to their own naming and payload rules. A namespace does not
create provider resources or permissions.

## Memory

The bundled `memorysecret` driver keeps a version history in process memory.
Use synthetic values only: it is neither durable storage nor a protected vault.

### Connecting

Seed a reporting credential for an isolated test:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/memorysecret"
)

driver := memorysecret.New(func(fixtures *memorysecret.Builder) {
	fixtures.Values(map[string][]byte{
		"reporting-client": []byte(`{"account":"local","token":"fixture"}`),
	})
})
secrets := secret.New(driver)
```

Each seed starts at version `"1"`. `Values` replaces and copies the map and
byte slices; nil or empty clears the seeds, and omitting it starts empty. A
retained construction draft cannot change the published store. There are no
start/stop hooks, native clients, timeout settings, or retry settings.

Use the [typed credential read](overview.md#typed-credentials) with this fixture.
The returned fields are `local` and `fixture`; do not print real credentials in
an equivalent production read.

### Storage Limits

Operations are concurrency-safe and copy payload bytes at their boundaries.
Already-canceled contexts change nothing; missing names or versions wrap
`secret.ErrNotFound`. Process exit loses all history. Deletion removes a name's
history immediately, and recreating it starts again at version `"1"`; an old
name/version selector can therefore select different contents.

See the [`memorysecret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret/memorysecret).

## AWS Secrets Manager

Choose AWS Secrets Manager for workloads with an explicitly configured AWS
identity. Runtime reads need `secretsmanager:GetSecretValue` on the required
resources; customer-managed encryption keys can require KMS permissions too.
Keep create, write, and delete permissions on provisioning identities.

### Connecting

From your application module, install the driver:

```sh
go get arkoren.dev/driver/aws
```

The AWS SDK discovers credentials and region through its standard sources,
including environment, shared configuration, and workload roles. Override the
region when the deployment does not supply it:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/aws/awssecret"
)

driver, err := awssecret.New(ctx, func(provider *awssecret.Builder) {
	provider.Region("eu-west-1")
})
secrets := secret.New(driver)
```

Construction loads SDK configuration; it does not test secret access. No driver
lifecycle hooks are required. Use the [startup read](overview.md#startup-loading)
with a provisioned name such as `reporting-token`.

### Provider Settings

`Credentials` supplies a shared credential provider instead of the default
chain; `Endpoint` overrides the service URL. Bound calls with operation contexts.
For transport, retries, and timeouts beyond those settings, configure a native
SDK client. The deprecated `Name` builder method is a compatibility no-op; it
does not select a default secret or namespace.

The driver accepts AWS secret names or supported identifiers. Reads accept text
or binary values; writes through Arkoren use binary payloads. Explicit
`Reference.Version` is an AWS `VersionId`, not a movable staging label. Missing
and pending-deletion secrets map to `secret.ErrNotFound`.

`Delete` schedules deletion using AWS's recovery window and does not immediately
free the name. A later same-name resource can have a different ARN. Keep the
logical secret and requested version retained for [pinned reads](managing.md#version-lifetimes).

### Native Access

If your application already owns an `aws.Config` named `config`, wrap its client:

```go
import "github.com/aws/aws-sdk-go-v2/service/secretsmanager"

driver := awssecret.NewFrom(secretsmanager.NewFromConfig(config))
native := driver.Client()
```

`native` provides SDK operations such as describing secrets. The driver does not
copy or close it. `Client()` returns nil for a nonnative test double. Native
operations bypass contract error mapping and facade observations.

The reporting-only `awssecret.Config` masks credential-provider contents by
type without retrieving credentials or invoking their formatter. Its endpoint
and name text, native objects, and returned errors are not sanitized. Keep
credentials out of endpoint URLs and ordinary diagnostics.

### Local Checks

With Docker, run an isolated MiniStack instance bound only to loopback. These
credentials are synthetic and must never be used as production credentials:

```sh
docker run --rm --name arkoren-secrets-aws -p 127.0.0.1:4566:4566 -e AWS_ACCESS_KEY_ID=000000000000 -e AWS_SECRET_ACCESS_KEY=test -e AWS_DEFAULT_REGION=us-east-1 ministackorg/ministack:1.5.8
```

After readiness, use this alternative construction in your local application:

```go
import "github.com/aws/aws-sdk-go-v2/credentials"

driver, err := awssecret.New(ctx, func(provider *awssecret.Builder) {
	provider.Region("us-east-1")
	provider.Endpoint("http://localhost:4566")
	provider.Credentials(credentials.NewStaticCredentialsProvider("000000000000", "test", ""))
})
secrets := secret.New(driver)
```

Run the disposable [version check](managing.md#writing-values) with this facade.
It verifies a synthetic write and pinned read, not AWS IAM permissions. Stop the
container with `docker stop arkoren-secrets-aws`; `--rm` discards its data.

See the [`awssecret` API reference](https://pkg.go.dev/arkoren.dev/driver/aws/awssecret).

## Azure Key Vault

Choose Key Vault when you have a vault and an Azure identity with secret access,
such as the Key Vault Secrets User role for runtime reads. Grant provisioning
permissions separately; default credential discovery is not an access check.

### Connecting

Install the driver from your application module:

```sh
go get arkoren.dev/driver/azure
```

Supply the vault's absolute HTTPS URL. The driver uses Azure's default credential
chain, including supported developer and managed identities:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/azure/azuresecret"
)

driver, err := azuresecret.New(ctx, "https://my-vault.vault.azure.net/")
secrets := secret.New(driver)
```

Construction validates the URL and creates SDK objects without contacting the
vault. There are no builder settings or driver lifecycle hooks. Use a
[startup read](overview.md#startup-loading) to verify access to the provisioned
name `reporting-token`.

### Provider Limits

Names are flat and allow letters, digits, and hyphens, not slash paths. Values
must be valid UTF-8; invalid bytes fail locally before a native write. Encode
binary material, for example as base64, and decode it after reading. Missing
names and versions wrap `secret.ErrNotFound`.

`Update` first reads for existence and then writes, so it requires read and write
permission. It has no native version precondition and is not an atomic
compare-and-set operation. Already-canceled operations fail before local UTF-8
or SDK parameter validation without a provider request.

`Delete` follows the vault's soft-delete retention. Writing a soft-deleted name
wraps `azuresecret.ErrSoftDeleted`; recovery restores the retained resource.
Do not automatically purge it to make provisioning succeed: purge is irreversible
and can be blocked by purge protection. Each value write creates a new version;
metadata and availability can still change independently.

### Native Access

For custom credentials, transport, retry, or timeout policy, use an existing
`azcore.TokenCredential` named `credential` and your HTTPS `vaultURL`:

```go
import "github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"

native, err := azsecrets.NewClient(vaultURL, credential, nil)
driver := azuresecret.NewFrom(native)
```

Replace nil with SDK client options when needed. `driver.Client()` returns the
shared client for recovery and other administration outside the contract; the
driver neither copies nor closes it. Keep SDK body logging and native error
details out of ordinary diagnostics. No offline emulator setup is provided by
this driver; use memory for fixture tests.

See the [`azuresecret` API reference](https://pkg.go.dev/arkoren.dev/driver/azure/azuresecret).

## Google Cloud Secret Manager

Choose Secret Manager when your project has its API enabled and the runtime
identity has Secret Manager Secret Accessor permission on the required secrets.
Creating resources, adding versions, and deleting require separate permissions.

### Connecting

Install the driver from your application module:

```sh
go get arkoren.dev/driver/gcp
```

The SDK uses Application Default Credentials. Configure the project for short
names such as `reporting-token`:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/gcp/gcpsecret"
)

driver, err := gcpsecret.New(ctx, func(provider *gcpsecret.Builder) {
	provider.Project("my-project")
})
secrets := secret.New(driver)
```

Register `driver.Stop` with [application cleanup](../application/lifecycle-and-ownership.md)
immediately after successful construction. It owns the native gRPC connection;
stop consumers before releasing it. The construction context controls SDK
construction and credential discovery, not future calls. Verify permissions with
the [startup read](overview.md#startup-loading).

`New` installs a discard logger for SDK request/response and credential logging,
even if `GOOGLE_SDK_GO_LOGGING_LEVEL=debug` is set. This does not disable the
facade's separate payload-free observations.

### Provider Settings

`Project` defaults to empty and is not inferred from credentials. Either set it
for unqualified secret IDs or use full names such as
`projects/my-project/secrets/reporting-token`. Fully qualified names keep their
explicit project. Slashes belong to that resource path, not inside a secret ID.
Use operation contexts to bound calls; native client settings own retry and
timeout policy.

`Put` creates an absent secret with automatic replication and then adds a
version. A failed version write can leave an empty resource. `Update` only adds
a version to an existing resource. Empty version selects `latest`; concrete
versions returned by the driver are strings such as `"1"`, not resource paths.
Missing resources or versions wrap `secret.ErrNotFound`.

`Delete` removes the resource and its versions. Recreation can reuse numeric
version selectors; a movable alias such as `latest` never pins contents.

### Native Access

If you configure SDK endpoint, credentials, retries, or transport yourself,
`NewFrom` preserves that client's logging policy.

> [!WARNING]
> SDK debug logging can include secret payloads, including when enabled by
> `GOOGLE_SDK_GO_LOGGING_LEVEL`. Configure a discard logger before supplying a
> native client, after other client options so that it takes precedence.

With existing `options` of type `[]option.ClientOption`, construct the client:

```go
import (
	"log/slog"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"google.golang.org/api/option"
)

clientOptions := append(options, option.WithLogger(slog.New(slog.DiscardHandler)))
native, err := secretmanager.NewClient(ctx, clientOptions...)
```

After checking the error, wrap it with the naming policy:

```go
driver := gcpsecret.NewFrom(native, func(provider *gcpsecret.Builder) {
	provider.Project("my-project")
})
```

`driver.Client()` exposes native version administration. `Stop` closes supplied
clients too: keep one cleanup owner and do not share the client with consumers
that outlive this driver. Native close has no context parameter, so an expired
Stop context still initiates release but cannot bound SDK connection/resolver
teardown. Use memory for offline fixtures; this driver supplies no emulator
setup.

See the [`gcpsecret` API reference](https://pkg.go.dev/arkoren.dev/driver/gcp/gcpsecret).

## HashiCorp Vault

Choose Vault when you operate a KV v2 mount and an authentication policy for your
application. Runtime readers need access to its data paths; provisioning tools
need appropriate write and metadata permissions too. This driver does not use
Vault's dynamic-secret engines.

### Connecting

Install the driver from your application module:

```sh
go get arkoren.dev/driver/vault
```

For managed login, supply an existing Vault SDK `api.AuthMethod` named
`loginMethod`. For example, the separate
[Kubernetes auth SDK](https://pkg.go.dev/github.com/hashicorp/vault/api/auth/kubernetes)
requires a Vault role bound to the workload's service account and namespace.
Configure that role and method before constructing the driver:

```go
import (
	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/vault/vaultsecret"
)

driver, err := vaultsecret.New(func(provider *vaultsecret.Builder) {
	provider.Address("https://vault.internal:8200")
	provider.Auth(loginMethod)
})
secrets := secret.New(driver)
```

Construction performs SDK validation but no login. Register `driver.Stop` as
cleanup and `driver.Start` as startup through the
[application lifecycle](../application/lifecycle-and-ownership.md). Register
credential loading after Start succeeds. With managed authentication, operations
before successful Start or after Stop return `vaultsecret.ErrNotStarted`.

### Authentication Lifetime

`Mount` defaults to `secret`; `Address` follows SDK defaults including
`VAULT_ADDR`. The SDK can load `VAULT_TOKEN`; a nonempty `Token` setting overrides
it. Static tokens are never renewed by the driver. With `Auth`, successful Start
replaces the token with the login result.

Renewable login tokens are renewed in the background. When renewal ends, the
driver logs in again, retrying with exponential waits from one second to one
minute. A nonrenewable login token is not automatically refreshed. Repeated
Start while ready is a no-op; malformed native renewability fails Start.
`Logger` defaults to `slog.Default()` and reports failed stages and retry delays,
not native diagnostics, tokens, or response bodies. Returned errors are not
sanitized.

Start and Stop serialize. Call Stop after Start returns; it cannot cancel a
blocked authentication callback. Stop signals managed renewal and waits for its
owner and native watcher within the caller's wait budget. An expired context
still signals cleanup, but an in-flight native renewal RPC depends on SDK
timeouts and transport cooperation. Ownership remains tracked after an early
return; restart is rejected until that worker drains, and a later Stop can wait
again. The native HTTP transport is not closed by the driver.

### JSON Credentials

Vault requires a non-null JSON object. With a provisioned `reporting-client`
object, use the [typed read](overview.md#typed-credentials). `String` would return
the entire JSON object as text, not its `token` field. Names are KV v2 paths
relative to the selected mount; do not add the mount's API envelope yourself.

Reads re-encode objects, so whitespace and member order can change. Writes
reject numbers whose value changes under native float64 JSON serialization,
including nested numbers. Store exact identifiers or decimals as JSON strings;
the driver does not convert them for you. For example, a provisioning tool can
encode this input with `encoding/json/v2` before writing it:

```go
type PartnerCredential struct {
	AccountID string `json:"account_id"`
	Token     string `json:"token"`
}

input := PartnerCredential{AccountID: "9007199254740993", Token: "fixture"}
```

Accepted numeric tokens remain numbers and are sent unchanged; equivalent
notations such as `1e3` and `1000` are accepted. Existing data is not migrated,
and reads cannot recover precision lost by an earlier writer or backend.

### Version Limits

Explicit selectors must be positive decimal versions. Empty selects current;
zero, negative, or unparseable selectors wrap `secret.ErrNotFound`. Missing,
deleted, or destroyed payloads also wrap that sentinel. Already-canceled calls
fail before version/JSON validation and provider requests.

`Update` reads the current version and uses its number as a native CAS
precondition, requiring read and write access. Concurrent changes fail without a
driver-level retry; detected absence after CAS rejection wraps
`secret.ErrNotFound`. Metadata deletion and recreation can restart at version
`1`, so this is not a cross-recreation identity check. Native SDK retry policy
remains unchanged. `Delete` permanently deletes metadata and all history, not
just the latest value through Vault's soft-delete API.

### Native Access

If you own authentication, logging, transport, and token renewal, wrap your
existing `*api.Client` named `native` from `github.com/hashicorp/vault/api`:

```go
driver := vaultsecret.NewFrom(native.KVv2("application"))
versions := driver.Client()
```

`versions` is the shared native `*api.KVv2`, usable for version administration.
`Client()` is nil for nonnative doubles. This path has no managed login or
renewal; Start/Stop are no-ops, as they are for static-token drivers. You retain
transport cleanup. Native calls bypass payload validation, error mapping, and
facade observation. The reporting-only `vaultsecret.Config` masks its static
Token, not Address or a shared Auth formatter.

### Local Checks

Run an isolated development server with Docker. Its root token and HTTP
transport are intentionally insecure: bind loopback and use only synthetic
secrets.

```sh
docker run --rm --name arkoren-secrets-vault -p 127.0.0.1:8200:8200 hashicorp/vault server -dev -dev-root-token-id=dev -dev-listen-address=0.0.0.0:8200
```

After readiness, the server has a `secret` KV v2 mount. Use this alternative
construction for the disposable check:

```go
driver, err := vaultsecret.New(func(provider *vaultsecret.Builder) {
	provider.Address("http://localhost:8200")
	provider.Token("dev")
})
secrets := secret.New(driver)
```

Run the JSON [version check](managing.md#writing-values). A successful pinned
read checks this local mount, not production authentication or TLS policy. Stop
the server with `docker stop arkoren-secrets-vault`; `--rm` discards its data.

See the [`vaultsecret` API reference](https://pkg.go.dev/arkoren.dev/driver/vault/vaultsecret).

For shared reads and writes, see the
[`secret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
