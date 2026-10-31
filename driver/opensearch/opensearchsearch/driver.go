package opensearchsearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/opensearch-project/opensearch-go/v5"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"arkoren.dev/core/service/search"
)

// Driver implements [search.Driver] on top of OpenSearch, one OpenSearch index
// per search index. It is safe for concurrent use. Build it with [New], which
// owns the client, or [NewFrom], which wraps one the caller manages.
// Do not copy a Driver after first use; share the pointer returned by construction.
//
// The driver sends plain JSON requests through the client's transport. It
// needs a server that supports the bulk, search, and mapping APIs as of
// OpenSearch 2.19 or 3.x, the versions the client supports.
//
// Example:
//
//	driver, err := opensearchsearch.New("http://localhost:9200")
//	if err != nil {
//		return err
//	}
//	compose.Cleanup(driver.Stop)
//	compose.Startup(driver.Start)
type Driver struct {
	// client publishes a native client independently of lifecycle serialization.
	// Requests can observe absence without waiting for Start or Stop.
	client atomic.Pointer[opensearchapi.Client]

	// lifecycle serializes owned acquisition, connectivity checks, and release.
	// It does not protect application-owned native calls or drain requests.
	lifecycle chan struct{}

	// config is the completed native configuration captured by New. Each Start
	// acquisition detaches its containers again before handing them to the SDK.
	config opensearchapi.Config

	// cancel retires the owned native lifetime, including on-start discovery.
	// It is set only with a created client, under lifecycle.
	cancel context.CancelFunc

	// prefix is put in front of every index name.
	prefix string

	// owned reports whether Stop closes the client.
	owned bool
}

var _ search.Driver = (*Driver)(nil)

// New creates a Driver that owns a client for the cluster at address, a URL
// such as "http://localhost:9200". New does not construct a native client or
// perform lifecycle I/O. [Driver.Start] creates the client and checks connectivity;
// [Driver.Stop] closes it and retires native background work. New fails when
// address is empty or the prefix is invalid. Native configuration errors,
// including malformed URLs and certificates, are reported by Start instead.
// Policies run directly once in order on one fresh draft before validation.
// Nil entries panic; expanded nil/empty slices omit edits. Owned node lists
// detach from retained drafts. The native Client hook runs directly once here,
// after basic validation and base settings, never again at Start. Its completed
// configuration containers are copied, not live native objects or captures.
//
//	driver, err := opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
//		engine.IndexPrefix("shop_")
//		engine.Username("search")
//		engine.Password(os.Getenv("OPENSEARCH_PASSWORD"))
//	})
//	if err != nil {
//		return err
//	}
//	compose.Cleanup(driver.Stop)
//	compose.Startup(driver.Start)
func New(address string, configure ...BuilderFunc) (*Driver, error) {
	config := configureSettings(configure)

	if address == "" {
		return nil, errors.New("opensearch: address is required")
	}

	if err := checkPrefix(config.IndexPrefix); err != nil {
		return nil, err
	}

	clientConfig := opensearchapi.Config{
		Client: opensearch.Config{
			Addresses: append([]string{address}, config.Nodes...),
			Username:  config.Username,
			Password:  config.Password,
			// Discovery is opt-in; all SDK construction waits until Start.
			DiscoverNodesOnStart: new(false),
		},
	}

	if config.Client != nil {
		config.Client(&clientConfig)
	}

	return &Driver{
		config:    snapshotConfig(clientConfig),
		lifecycle: make(chan struct{}, 1),
		prefix:    config.IndexPrefix,
		owned:     true,
	}, nil
}

// NewFrom creates a Driver that sends its requests with client, which the
// caller built and remains responsible for: [Driver.Stop] does not close it.
// Only [Builder.IndexPrefix] applies; other settings configure the client
// that [New] builds. NewFrom fails when the prefix is invalid.
// Its policies have New's direct once-only ordering, nil-entry panic, and
// detached publication semantics; the native Client hook is never invoked.
//
//	client, err := opensearchapi.NewClient(opensearchapi.Config{
//		Client: opensearch.Config{Addresses: []string{"https://search.internal:9200"}},
//	})
//	if err != nil {
//		return err
//	}
//	driver, err := opensearchsearch.NewFrom(client, func(engine *opensearchsearch.Builder) {
//		engine.IndexPrefix("shop_")
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func NewFrom(client *opensearchapi.Client, configure ...BuilderFunc) (*Driver, error) {
	config := configureSettings(configure)

	if err := checkPrefix(config.IndexPrefix); err != nil {
		return nil, err
	}

	driver := &Driver{
		prefix: config.IndexPrefix,
	}

	driver.client.Store(client)

	return driver, nil
}

// Ping checks that a node answers a request for the cluster description. It
// does not check cluster health or that indexes exist. Before owned Start or
// after Stop it returns a not-started error without creating a client. Request
// methods have the same no-implicit-acquisition behavior; empty Sync/Check still
// perform no requests. A canceled context takes precedence over readiness.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	reply, err := driver.do(ctx, http.MethodGet, "/", nil, "", nil)

	if err != nil {
		return err
	}

	return reply.err("ping")
}

// indexName returns the OpenSearch index of the search index called name:
// the prefix and the name. It fails with an error wrapping
// [search.ErrInvalidName] when the result starts with an underscore.
func (driver *Driver) indexName(name string) (string, error) {
	full := driver.prefix + name

	if full[0] == '_' {
		return "", fmt.Errorf("opensearch: index %q: %w: OpenSearch index names cannot start with an underscore (set an index prefix)", name, search.ErrInvalidName)
	}

	return full, nil
}
