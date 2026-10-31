package azuresecret

import "github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"

// Client returns the underlying Key Vault client, or nil when the driver was
// built from a test double. It remains borrowed; native operations bypass
// contract error mapping and do not transfer transport or credential ownership.
//
// Example:
//
//	if native := driver.Client(); native != nil {
//		alternate := azuresecret.NewFrom(native)
//		_ = alternate
//	}
func (driver *Driver) Client() *azsecrets.Client {
	client, _ := driver.client.(*azsecrets.Client)

	return client
}
