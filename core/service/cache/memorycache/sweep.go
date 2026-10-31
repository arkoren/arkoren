package memorycache

import (
	"context"
)

// Start launches the janitor unless Cleanup is negative. Starting an already
// started driver does nothing.
//
// Example:
//
//	driver := memorycache.New()
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
//	return driver.Stop(ctx)
func (driver *Driver) Start(ctx context.Context) error {
	return driver.store.Start(ctx)
}

// Stop ends the janitor and waits for it to exit or ctx to end. The driver
// stays usable afterwards; expired entries are then removed by access and by
// inline sweeps on writes.
//
// Example:
//
//	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
//	defer cancel()
//	if err := driver.Stop(cleanup); err != nil {
//		return err
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	return driver.store.Stop(ctx)
}
