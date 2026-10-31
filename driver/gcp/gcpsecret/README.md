# Google Cloud Secret Manager Driver

`arkoren.dev/driver/gcp/gcpsecret` implements `secret.Driver` with Google Cloud
Secret Manager. gRPC `NotFound` responses wrap `secret.ErrNotFound`.

`New` and `NewFrom` accept trailing `func(*gcpsecret.Builder)` callbacks:

Install `go get arkoren.dev/driver/gcp` from your application's module directory
and import `gcpsecret` from `arkoren.dev/driver/gcp/gcpsecret`. Configure
Application Default Credentials with read access to the required secrets:

Use your run context `ctx` and existing `app` for composition and cleanup.
Import `application` from `arkoren.dev/core/application`:

```go
err := app.Run(ctx, func(compose *application.Composer) {
	driver := compose.Must(gcpsecret.New(compose.Context(), func(secrets *gcpsecret.Builder) {
		secrets.Project("example-project")
	}))
	compose.Cleanup(driver.Stop)
	// Pass driver to your secret facade here.
})
```

Callbacks run directly once, synchronously in order on one fresh draft before
native client creation. Nil entries panic; expanded nil/empty slices omit edits.
`Project` returns the same draft. Publication copies the completed naming policy,
so retaining the draft cannot change a driver. Native clients are not cloned.
`Stop` closes native Secret Manager clients, including those supplied to `NewFrom`.
Register one cleanup owner. Native Close waits for SDK connection/resolver
teardown without accepting a context; an expired cleanup context does not skip
release, but cannot impose a hard deadline on that native teardown.

## SDK logging

`New` installs a discard SDK logger because native debug logs can contain secret
request and response payloads or credential material. This overrides
`GOOGLE_SDK_GO_LOGGING_LEVEL` for that client without changing Application Default
Credentials discovery or its construction context.

`NewFrom` does not modify the supplied client's logging policy. Before wrapping a
native client, pass `slog.New(slog.DiscardHandler)` through
[`option.WithLogger`](https://pkg.go.dev/google.golang.org/api/option#WithLogger)
to the SDK constructor. Otherwise, ambient or explicit SDK debug logging can
expose secret payloads.

## Provider limitations

Operations accept a qualified secret resource name,
`projects/{project}/secrets/{secret}`, or an unqualified name when `Project`
is configured. `Get` appends
`/versions/{Reference.Version}`; an empty version selects `latest`.
Concrete version IDs start at `1` within each secret resource and increment;
secret data in a concrete version is immutable. Deleting/recreating a secret
resource can therefore reuse numeric selectors at the same name. `latest` and
custom version aliases are movable selectors, not immutable identities; retain
the concrete version returned by the provider when pinning a value.

`Builder.Project` supplies the project for unqualified reads, version writes,
and deletion. `Put` also uses it to derive the `CreateSecret` parent when the
name is unqualified. Qualified names retain their explicit project. Configure
`Project` when using unqualified names; its empty default is not inferred from
application default credentials.

Optional typed namespaces compose final names in the facade. A namespace prefix
`projects/example-project/secrets` with explicit separator `/` can join a valid
secret ID such as `db-password`. The slash separates resource-path components;
it is not valid inside an unqualified secret ID. With `Project`, use flat IDs
such as `tenants-primary` and a suitable separator instead. Construction neither
creates nor authorizes provider resources or enforces their naming rules.

`app` is your existing application. Pass the driver to `secret.New`
from `arkoren.dev/core/service/secret`; construction does not verify secret
permissions. `Put` creates absent secrets with automatic replication, then
adds a version; a failed version write can leave an empty secret.

See [Google Secret Manager setup](../../../cmd/arkoren/skills/arkoren/references/guides/secrets/drivers.md#google-cloud-secret-manager)
and the [`gcpsecret` API reference](https://pkg.go.dev/arkoren.dev/driver/gcp/gcpsecret).
