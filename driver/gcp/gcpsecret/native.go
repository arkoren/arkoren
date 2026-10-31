package gcpsecret

import (
	"context"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
)

// Client returns the underlying Secret Manager client, or nil when the driver was
// built from a test double. The driver owns native connection release, including
// NewFrom clients; use Stop rather than registering a second cleanup owner.
//
// Example:
//
//	if native := driver.Client(); native != nil {
//		_ = native.CallOptions.AccessSecretVersion
//	}
func (driver *Driver) Client() *secretmanager.Client {
	client, _ := driver.client.(*secretmanager.Client)

	return client
}

// Stop releases the gRPC connection, including a client supplied to [NewFrom].
// Composition can register driver.Stop with Composer.Cleanup. It is a no-op for
// drivers built from a test double. Native Close waits for SDK connection and
// resolver teardown and does not accept a context. ctx is therefore not consulted:
// an expired ctx still initiates release, but cannot bound native teardown.
//
// Example:
//
//	return driver.Stop(ctx)
func (driver *Driver) Stop(context.Context) error {
	if client := driver.Client(); client != nil {
		return client.Close()
	}

	return nil
}
