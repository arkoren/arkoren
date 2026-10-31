# Azure driver

Install with `go get arkoren.dev/driver/azure` to use
[Azure Key Vault secrets](azuresecret/README.md) with Arkoren.
Configure the default Azure credential chain and an existing vault; construction
does not verify permissions.

## Contributor tests

From the repository root, run `make check MODULE=driver/azure`.
Tests use local fakes and require no Azure account or credentials.
