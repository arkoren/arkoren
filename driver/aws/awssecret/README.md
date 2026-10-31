# AWS Secrets Manager Driver

`arkoren.dev/driver/aws/awssecret` implements `secret.Driver` with AWS Secrets
Manager. `New` accepts trailing `func(*awssecret.Builder)` callbacks with
`Region`, `Endpoint`, and `Credentials` settings. The deprecated `Name` setter
is a compatibility no-op; pass explicit names to secret operations. The redacted
`Config` record remains for reporting, not as constructor input.

Use `New` for the AWS default credential chain or `NewFrom` with an existing SDK
client. Missing secrets wrap `secret.ErrNotFound`.

Install `go get arkoren.dev/driver/aws` from your application's module directory
and import `awssecret` from `arkoren.dev/driver/aws/awssecret`. With a startup
context `ctx`, select the region where your secrets live:

```go
driver, err := awssecret.New(ctx, func(secrets *awssecret.Builder) {
	secrets.Region("eu-west-1")
})
```

Callbacks run directly once, synchronously in order on one fresh draft before
AWS configuration loads. Nil entries panic; expanded nil/empty slices omit
edits. Methods return the same draft. Publication copies settings, not native
credential providers. `NewFrom(client)` has no settings or callback and does
not close the client.

Formatting `Config` masks credential providers by type without retrieving their
credentials or invoking their formatter. It does not recursively sanitize shared
native objects or arbitrary diagnostic text: keep secrets out of Endpoint and
Name, and do not log native credential providers directly.

Pass the driver to `secret.New` from `arkoren.dev/core/service/secret`.
No lifecycle hook is needed; construction loads SDK configuration but does not
verify read permissions. Writes store binary values; reads also accept
console-created string values. Deletion schedules the AWS recovery window and
does not immediately free the name for reuse.

Version IDs identify immutable values within a secret resource. AWS's SDK creates
per-write request tokens; this driver preserves the returned IDs unchanged.
After permanent deletion, a new secret with the same name has a different ARN.
Do not treat a saved name/version pair as a global identity across recreation;
deletion retention and name availability remain provider-controlled.

Optional `secret.NewNamespace[T]("tenants", "/")` composes typed definitions
using AWS secret names; provision and authorize each final name separately.
This facade-only composition creates no secret, schema, or isolation boundary.
The separator is explicit because other providers have different naming rules.

For permissions, native-client settings, and a local emulator, see
[AWS secret setup](../../../cmd/arkoren/skills/arkoren/references/guides/secrets/drivers.md#aws-secrets-manager)
and the [`awssecret` API reference](https://pkg.go.dev/arkoren.dev/driver/aws/awssecret).
