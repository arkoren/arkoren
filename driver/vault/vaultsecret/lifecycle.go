package vaultsecret

import "context"

// Start logs in once with the configured auth method and renews renewable login
// tokens until Stop; see Config.Auth. Repeated calls while ready are no-ops.
// Static-token and NewFrom drivers are already ready and perform no login.
// Malformed native renewability returns an error instead of disabling renewal.
// ctx bounds the first login when the shared auth method honors cancellation;
// the construction/start context is not retained by background renewal.
//
// Start and Stop serialize. Arrange cleanup after Start returns: a Stop context
// does not cancel an in-progress Start/auth callback. After a Stop that timed out,
// Start rejects restart until the outstanding native renewal has returned.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	if driver.started.Load() {
		return nil
	}

	if err := driver.renewal.Start(ctx); err != nil {
		return err
	}

	driver.started.Store(true)

	return nil
}

// Stop marks a managed-auth driver unavailable, signals renewal to stop, and
// waits for both its worker and native watcher until ctx ends. Static-token and
// NewFrom drivers have no managed renewal and remain usable; Stop is a no-op.
// An expired ctx still signals cleanup and can cut the caller's wait short.
// Native watcher Stop cannot interrupt an in-flight renewal RPC: its SDK timeout,
// transport, and server determine when that RPC finishes. Ownership is retained
// after an early return; a later Stop can wait again. Lifecycle calls serialize,
// so call Stop after Start returns rather than using it to cancel a login.
//
// Example:
//
//	if err := driver.Stop(cleanupContext); err != nil {
//		return err
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	if driver.renewal != nil {
		driver.started.Store(false)

		return driver.renewal.Stop(ctx)
	}

	return nil
}
