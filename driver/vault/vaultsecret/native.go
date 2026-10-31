package vaultsecret

import "github.com/hashicorp/vault/api"

// Client returns the underlying Vault KV v2 client, or nil when the driver was
// built from a test double. It remains shared; direct native calls bypass JSON
// payload validation, contract error mapping, and the facade's observations.
//
// Example:
//
//	if native := driver.Client(); native != nil {
//		borrowed := vaultsecret.NewFrom(native)
//		_ = borrowed
//	}
func (driver *Driver) Client() *api.KVv2 {
	client, _ := driver.client.(*api.KVv2)

	return client
}
