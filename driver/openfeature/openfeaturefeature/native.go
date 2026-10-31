package openfeaturefeature

import "github.com/open-feature/go-sdk/openfeature"

// Client returns the OpenFeature client the driver evaluates with: the one
// passed to [NewFrom], or, for a driver built by [New], a client of the
// private API instance that owns the provider. Use it for what the contract
// does not cover, such as event handlers or hooks. For a driver built by
// [New], evaluating with it before [Driver.Start] returns the SDK's no-op
// results, not [feature.ErrNotReady].
// Native context/hooks remain shared and bypass facade validation and telemetry;
// do not rebind or shut down an owned provider through this escape hatch.
//
// Example:
//
//	client := driver.Client()
//	_ = client.State()
func (driver *Driver) Client() *openfeature.Client {
	return driver.client
}
