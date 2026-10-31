package launchdarklyfeature

import ld "github.com/launchdarkly/go-server-sdk/v7"

// Client returns the LaunchDarkly client the driver evaluates with: the one
// passed to [NewFrom], or, for a driver built by [New], the client [Driver.Start]
// created. It returns nil for a driver built by [New] that is not running.
// Use it for what the contract does not cover, such as event tracking,
// all-flags state, or hooks. The client of a [New] driver is closed by
// [Driver.Stop]; do not close it yourself.
//
// Example:
//
//	if client := driver.Client(); client != nil {
//		_ = client.Initialized()
//	}
func (driver *Driver) Client() *ld.LDClient {
	return driver.client.Load()
}
