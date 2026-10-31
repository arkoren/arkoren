package meilisearchsearch

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"arkoren.dev/core/service/search"
)

const (
	// maxPrefixLength bounds [Config.IndexPrefix]. A uid holds at most 400
	// bytes and an index name at most 64.
	maxPrefixLength = 100

	// maxBatchBytes is the encoded size at which [Driver.Put] starts a new
	// request. It stays far below the 100 MB payload limit of Meilisearch.
	maxBatchBytes = 8 << 20

	// maxDeleteIDs is how many IDs one delete request carries.
	maxDeleteIDs = 1000

	// minMajor and minMinor are the oldest supported Meilisearch version,
	// 1.15: the first whose filters compare strings.
	minMajor, minMinor = 1, 15
)

// validPrefix matches the index prefixes [New] accepts: the characters of a
// Meilisearch uid, at most [maxPrefixLength] of them.
var validPrefix = regexp.MustCompile(`^[A-Za-z0-9_-]{0,100}$`)

// Driver implements [search.Driver] on a Meilisearch server. It keeps no
// state besides its HTTP client, so it is safe for concurrent use and several
// drivers can share a server. Create it with [New] or [NewFrom].
//
// Example:
//
//	driver, err := meilisearchsearch.New("http://localhost:7700")
//	if err != nil {
//		return err
//	}
//	_ = driver.Client()
type Driver struct {
	// client sends the requests.
	client *http.Client

	// host is the server URL without a trailing slash.
	host string

	// config holds the API key and index prefix.
	config Config
}

var _ search.Driver = (*Driver)(nil)

// New creates a [Driver] for the Meilisearch server at host, a URL such as
// http://localhost:7700. It validates the arguments and does no I/O: the
// first request happens in [Driver.Start] or the first call.
//
// host must be an absolute http or https URL; a path prefix is kept, which
// serves a server behind a reverse proxy. Queries and fragments are rejected.
// The native default transport is cloned; an application-installed transport
// wrapper remains shared, including its idle-connection cleanup behavior.
// Policies set [Builder.APIKey] and
// [Builder.IndexPrefix] directly once in order before validation. Nil entries
// panic; expanded nil/empty slices omit edits. Published settings detach from
// retained drafts. New returns an error for an invalid host or prefix.
//
//	driver, err := meilisearchsearch.New("http://localhost:7700", func(engine *meilisearchsearch.Builder) {
//		engine.APIKey(os.Getenv("MEILI_MASTER_KEY"))
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func New(host string, configure ...BuilderFunc) (*Driver, error) {
	config := configureSettings(configure)

	// transport clones the native pool; application wrappers remain shared.
	transport := http.DefaultTransport

	if native, ok := transport.(*http.Transport); ok {
		transport = native.Clone()
	}

	client := &http.Client{
		Transport: transport,
	}

	return newFrom(client, host, config)
}

// NewFrom creates a [Driver] that sends requests with a caller-owned client,
// for custom transports, timeouts, or proxies. It validates host and the
// settings like [New], and every builder setting applies. The client needs no timeout:
// every call is bounded by its context.
//
//	driver, err := meilisearchsearch.NewFrom(&http.Client{Timeout: time.Minute},
//		"https://search.internal:7700")
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func NewFrom(client *http.Client, host string, configure ...BuilderFunc) (*Driver, error) {
	return newFrom(client, host, configureSettings(configure))
}

// newFrom validates the completed settings and host without replaying policies.
// It retains the supplied client without copying or changing its transport.
func newFrom(client *http.Client, host string, config Config) (*Driver, error) {
	address, err := url.Parse(host)

	if err != nil {
		return nil, fmt.Errorf("meilisearch host %q: want an absolute http or https URL", host)
	}

	httpScheme := address.Scheme == "http" || address.Scheme == "https"

	if !httpScheme || address.Host == "" {
		return nil, fmt.Errorf("meilisearch host %q: want an absolute http or https URL", host)
	}

	if address.RawQuery != "" || address.ForceQuery || address.Fragment != "" {
		return nil, fmt.Errorf("meilisearch host: query strings and fragments are not supported")
	}

	if !validPrefix.MatchString(config.IndexPrefix) {
		return nil, fmt.Errorf("meilisearch index prefix %q: want at most %d bytes of A-Z, a-z, 0-9, _, and -", config.IndexPrefix, maxPrefixLength)
	}

	return &Driver{
		client: client,
		host:   strings.TrimRight(address.String(), "/"),
		config: config,
	}, nil
}

// Ping checks that the server answers and accepts the API key, by asking for
// its version. It suits an application readiness check.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	return driver.do(ctx, http.MethodGet, "/version", nil, nil)
}

// Start verifies connectivity, the API key, and that the server is
// Meilisearch 1.15 or later, so a misconfigured server fails fast at startup.
// Nothing else is set up: [Driver.Sync] creates the indexes.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err // Check credentials and the supported server version.
//	}
func (driver *Driver) Start(ctx context.Context) error {
	var version struct {
		// PkgVersion is checked against the minimum supported server version.
		PkgVersion string `json:"pkgVersion"`
	}

	if err := driver.do(ctx, http.MethodGet, "/version", nil, &version); err != nil {
		return err
	}

	var major, minor int

	if _, err := fmt.Sscanf(version.PkgVersion, "%d.%d", &major, &minor); err != nil {
		return fmt.Errorf("meilisearch version %q: %w", version.PkgVersion, err)
	}

	if major < minMajor || (major == minMajor && minor < minMinor) {
		return fmt.Errorf("meilisearch %s is too old: version %d.%d or later is required", version.PkgVersion, minMajor, minMinor)
	}

	return nil
}

// Stop closes the idle connections of the HTTP client. It does not wait for
// anything, because requests end with their contexts, and it is safe to call
// again.
// A supplied HTTP client remains caller-owned. Cancellation does not skip idle
// cleanup, and this operation does not cancel or wait for in-flight requests.
//
// Example:
//
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Stop(context.Context) error {
	driver.client.CloseIdleConnections()

	return nil
}

// uid returns the Meilisearch index uid of schema: the prefix plus the name.
func (driver *Driver) uid(schema search.Schema) string {
	return driver.config.IndexPrefix + schema.Name
}

// notFound maps the index_not_found error of Meilisearch to an error
// wrapping [search.ErrNotFound] and passes every other error through.
func notFound(err error, uid string) error {
	if hasCode(err, codeIndexNotFound) {
		return fmt.Errorf("meilisearch index %s: %w", uid, search.ErrNotFound)
	}

	return err
}
