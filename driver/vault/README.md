# HashiCorp Vault driver

Install with `go get arkoren.dev/driver/vault` to use
[Vault KV v2 secrets](vaultsecret/README.md) with Arkoren.
Use an existing mount and configure a caller-managed token or managed login.
Delete permanently removes metadata and all versions.

## Contributor tests

From the repository root, run `make check MODULE=driver/vault` and
`make integration MODULE=driver/vault`. Integration starts a disposable Vault
development server on loopback port 8200 and removes its own Compose project
and volumes. Do not use the development server or its token in production.
