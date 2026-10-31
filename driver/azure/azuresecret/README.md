# Azure Key Vault Driver

`arkoren.dev/driver/azure/azuresecret` implements `secret.Driver` with Azure
Key Vault. `New(ctx, vaultURL)` takes the vault URL as a required argument
(an absolute https URL) and uses the default Azure credential chain;
`NewFrom` wraps an existing client. HTTP 404 responses wrap `secret.ErrNotFound`.

There are no optional constructor settings, callbacks, or empty builders.
The supplied client is not copied or closed by the driver.

Install `go get arkoren.dev/driver/azure` from your application's module directory
and import `azuresecret` from `arkoren.dev/driver/azure/azuresecret`. Create the
driver with your startup context and existing vault URL:

```go
driver, err := azuresecret.New(ctx, "https://my-vault.vault.azure.net/")
```

Pass it to `secret.New` from `arkoren.dev/core/service/secret`. Construction does
not test permissions; the first read does. Names use letters, digits, and dashes.
Values must be valid UTF-8 text. Invalid UTF-8 fails before a write instead of
being silently replaced by the SDK JSON encoder; base64-encode binary keys.
Update reads before writing and therefore needs read permission as well as
write permission. This is an existence check, not a native CAS transaction.
Already-canceled operations return the context error before local or SDK
validation. Native errors can retain provider diagnostics; do not log them as
though they were sanitized public messages.

Deleted names remain reserved by
soft deletion, and writes to them wrap `azuresecret.ErrSoftDeleted`. Recover a
needed secret rather than purging it irreversibly.

Each native SetSecret creates a new system-generated version; existing version
values cannot be edited through UpdateSecretProperties. Metadata and availability
can change independently. Soft-deleted names cannot be recreated until the
provider permits reuse. Do not infer a cross-recreation generation identity from
the opaque version string; this driver returns it unchanged.

For related JSON definitions, `secret.NewNamespace[T]("tenants", "-")` uses
Azure's flat naming convention. Keep every component and final name within
Key Vault's rules; slash paths are not valid secret names. Namespace construction
does not create a secret, grant permissions, or enforce a provider schema.

See [Key Vault setup](../../../cmd/arkoren/skills/arkoren/references/guides/secrets/drivers.md#azure-key-vault)
and the [`azuresecret` API reference](https://pkg.go.dev/arkoren.dev/driver/azure/azuresecret).
