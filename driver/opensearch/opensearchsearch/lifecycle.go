package opensearchsearch

import (
	"context"
	"fmt"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
)

// Start creates the owned native client if absent, then checks connectivity
// with [Driver.Ping]. Native configuration errors preserve their SDK identities.
// A failed connectivity check retains the created client and its background work:
// call Stop even after a failed Start. Another Start reuses it and checks again.
// After Stop, Start can create a fresh client from the captured configuration
// without replaying the construction hook. NewFrom only checks connectivity.
//
// An already canceled ctx prevents owned acquisition. Connectivity checks use
// ctx, but native construction and waits for concurrent Start/Stop do not support
// forced cancellation. Native background work uses the configured native context,
// not the startup context, and is canceled by Stop. Native callbacks must not
// recursively call Start or Stop while a lifecycle operation is in progress.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	if !driver.owned {
		return driver.Ping(ctx)
	}

	driver.lifecycle <- struct{}{}
	defer func() {
		<-driver.lifecycle
	}()

	if err := ctx.Err(); err != nil {
		return err
	}

	if driver.client.Load() == nil {
		if err := driver.createClient(); err != nil {
			return err
		}
	}

	return driver.Ping(ctx)
}

// createClient acquires one native lifetime while lifecycle is held. A derived
// context also covers the SDK's separate initial discovery task. On constructor
// failure its cancellation retires partial background work; on success Stop owns
// cancellation and Close, even when the subsequent connectivity check fails.
func (driver *Driver) createClient() error {
	config := snapshotConfig(driver.config)
	parent := config.Client.Context

	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithCancel(parent)
	config.Client.Context = ctx

	client, err := opensearchapi.NewClient(config)

	if err != nil {
		cancel()

		return fmt.Errorf("opensearch: create client: %w", err)
	}

	driver.cancel = cancel
	driver.client.Store(client)

	return nil
}

// Stop closes the client of a driver built by [New], releasing its
// connections and canceling native background workers, including after failed
// connectivity checks. It waits for concurrent owned Start/Stop calls and the
// SDK's Close; ctx does not bound these waits or skip cleanup. It does not drain
// application-owned requests or forcibly interrupt uncooperative native hooks.
// Client returns nil after release. Stop before Start and repeated Stop are
// no-ops; later Start may acquire again. NewFrom clients are never closed.
//
// Example:
//
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Stop(context.Context) error {
	if !driver.owned {
		return nil
	}

	driver.lifecycle <- struct{}{}
	defer func() {
		<-driver.lifecycle
	}()

	client := driver.client.Swap(nil)

	if client == nil {
		return nil
	}

	driver.cancel()
	driver.cancel = nil

	if err := client.Close(); err != nil {
		return fmt.Errorf("opensearch: stop: %w", err)
	}

	return nil
}
