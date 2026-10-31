# HashiCorp Vault Driver

`arkoren.dev/driver/vault/vaultsecret` implements `secret.Driver` with Vault KV v2. An empty
`Builder.Mount` uses `secret`. Missing or HTTP 404 secrets wrap
`secret.ErrNotFound`.

## Authentication

A static token (`Builder.Token`) is the caller's to renew. With `Builder.Auth`, `Start` logs in, renews a renewable token in
the background, and logs in again with backoff when renewal ends. `Stop` signals
renewal and waits for both the owner and native watcher. `Builder.Logger`
(default `slog.Default`) receives failed stages and retry delays, not native
diagnostic text, tokens, or response bodies.

Install `go get arkoren.dev/driver/vault` from your application's module directory
and import `vaultsecret "arkoren.dev/driver/vault/vaultsecret"`.
With an existing KV v2 mount and a caller-managed token, configure the driver:

```go
driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
	secrets.Address("https://vault.internal:8200")
	secrets.Mount("application")
	secrets.Token(os.Getenv("VAULT_TOKEN"))
})
```

Callbacks run directly once in order on one fresh draft before SDK validation.
Nil entries panic; expanded nil or empty slices omit edits. Published settings
do not retain the draft; native auth methods and loggers remain shared. `NewFrom`
still takes only an existing KV v2 client. The exported redacted `Config` record
is not constructor input.

## Provider limitations

`Reference.Version` must be a positive decimal integer accepted by `strconv.Atoi`; an
empty version reads the current Vault version. `Put` and `Update` require a JSON
object payload, not `null`. Vault's native JSON boundary can round numbers:
Vault 1.13.3 returns `9007199254740992` after receiving `9007199254740993`.
The driver rejects numbers whose value changes under float64 JSON serialization
before writing, including nested numbers. Equivalent notation such as `1e3` and
`1000` is accepted. Encode exact identifiers and decimals as JSON strings yourself;
the driver never converts them or introduces a private persisted envelope.
Existing native data is not migrated, and a read cannot reconstruct digits
already lost by an earlier writer or backend. `Get` JSON-encodes the data map, so its returned bytes need not
preserve the formatting of the value supplied to a write.

Update uses the observed native version as a CAS precondition. Concurrent
changes fail instead of overwriting blindly; a deletion detected after CAS
rejection wraps `secret.ErrNotFound` and does not recreate the name. The driver
does not retry rejected writes; native SDK retry policy remains unchanged.
Missing, deleted, and destroyed version payloads also
wrap `secret.ErrNotFound` rather than returning a successful JSON null.

Version numbers belong to the current key metadata. Delete removes that metadata;
recreating the same path starts a new history and can reuse version `1`. A saved
name/version pair is not an identity for the old secret after deletion/recreation.
CAS likewise compares the current numeric version, not a global generation token.

Pass the driver to `secret.New` from `arkoren.dev/core/service/secret`.
The Vault SDK loads `VAULT_TOKEN` automatically; a nonempty `Builder.Token`
overrides it. Successful `Builder.Auth` login replaces either token. Static
tokens are not renewed by the driver. With `Builder.Auth`,
register `driver.Start` before secret-loading startup hooks and `driver.Stop`
after its consumers; operations before login return `vaultsecret.ErrNotStarted`.
A nonrenewable login token is not automatically refreshed. `Delete` removes
metadata and all versions permanently, not just the current version.

Repeated Start while ready is a no-op. Arrange Stop after Start returns: lifecycle
calls serialize, and a cleanup context does not interrupt an in-progress auth
callback. Stop with an expired context still signals cleanup but can return before
native renewal finishes. Vault's watcher cannot cancel an in-flight SDK renewal
RPC; its native timeout/transport determines completion. The worker stays owned,
and restart is rejected until it has drained. Call Stop again to wait longer.
Static-token and NewFrom drivers have no managed renewal; Start/Stop are no-ops
and do not close the caller-owned native HTTP transport.

`Config` formatting masks only its static Token field. Native Auth methods and
their formatters remain shared and caller-owned; do not put credentials in their
diagnostic formatting or in Address. Returned operation errors can retain native
provider details even though automatic renewal logs exclude those details.

Optional `secret.NewNamespace[T]("tenants", "/")` composes typed definitions
for KV v2 paths within the configured mount. The definition does not select a
different mount, create a secret, grant path permissions, or enforce a schema.
The separator is explicit; Azure and Google secret IDs need different choices.

For auth methods, native-client ownership, and a reproducible development
server, see [Vault setup](../../../cmd/arkoren/skills/arkoren/references/guides/secrets/drivers.md#hashicorp-vault)
and the [`vaultsecret` API reference](https://pkg.go.dev/arkoren.dev/driver/vault/vaultsecret).
