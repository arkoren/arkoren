package opensearchsearch

import (
	"slices"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
)

// Client returns the OpenSearch client the driver sends requests with: the one
// passed to [NewFrom], or the one [Driver.Start] created. Use it for operations
// outside the contract, such as cluster health or snapshot calls. The client of a
// [New] driver is closed by [Driver.Stop]; do not close it yourself.
// Native calls bypass search facade validation and telemetry. Configure shared
// native objects before concurrent use and serialize native index administration.
// For an owned driver it returns nil before successful native creation and after
// Stop. A failed connectivity check can leave a non-nil client owned by Stop.
// The accessor is synchronized, but retaining its result does not prevent Stop
// from closing that client; callers coordinate native work with shutdown.
// Native document APIs expose the physical source/strings/numbers/booleans
// wrapper, not the original application JSON returned by Driver.Search. Use
// Driver.Put to retain original bytes and populate the typed index fields.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err // The lifecycle owner must still call Stop.
//	}
//	client := driver.Client()
//	_ = client.Cluster // Native cluster operations are explicit application work.
func (driver *Driver) Client() *opensearchapi.Client {
	return driver.client.Load()
}

// snapshotConfig detaches the SDK record's owned containers and scalar pointers.
// Native transports, routers, observers, contexts, functions and their captures
// remain shared dependencies: configure them before concurrent use. Copying at
// publication protects against retained hook drafts; copying at each acquisition
// keeps SDK mutation from changing the next Start's configuration.
func snapshotConfig(config opensearchapi.Config) opensearchapi.Config {
	config.Client.Addresses = slices.Clone(config.Client.Addresses)
	config.Client.Header = config.Client.Header.Clone()
	config.Client.CACert = slices.Clone(config.Client.CACert)
	config.Client.RetryOnStatus = slices.Clone(config.Client.RetryOnStatus)

	if config.Client.DiscoverNodesOnStart != nil {
		config.Client.DiscoverNodesOnStart = new(*config.Client.DiscoverNodesOnStart)
	}

	if config.Errors != nil {
		config.Errors = new(*config.Errors)
	}

	return config
}
