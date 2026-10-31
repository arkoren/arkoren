package opensearchsearch

import (
	"fmt"
	"slices"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"arkoren.dev/core/service/search"
)

// BuilderFunc is an ordinary alias for reusable construction policies. Policies
// run once in order before validation; native Client hooks run only in New.
//
// Example:
//
//	var catalog opensearchsearch.BuilderFunc = func(engine *opensearchsearch.Builder) {
//		engine.IndexPrefix("shop_")
//	}
//	driver, err := opensearchsearch.New("http://localhost:9200", catalog)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// Builder configures New and NewFrom before publishing a driver. NewFrom uses
// only IndexPrefix and never invokes Client. Callbacks run directly once in
// order, including on invalid inputs; nil entries panic, nil slices omit edits.
// Completed settings and owned node lists detach from retained drafts.
//
// Example:
//
//	driver, err := opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
//		engine.IndexPrefix("shop_")
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
type Builder struct {
	// config owns node lists, but borrows native hooks and their captures.
	config settings
}

// configureSettings invokes policies on a fresh draft and detaches owned nodes.
// Native hook functions remain shared and are not invoked by this step.
func configureSettings(configure []BuilderFunc) settings {
	search := &Builder{}

	for _, configure := range configure {
		configure(search)
	}

	config := search.config
	config.Nodes = slices.Clone(config.Nodes)

	return config
}

// IndexPrefix replaces the index prefix and returns this draft. Empty uses bare
// names; constructors require at most 64 bytes of [a-z0-9_] otherwise.
//
// Example:
//
//	driver, err := opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
//		engine.IndexPrefix("shop_")
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (engine *Builder) IndexPrefix(value string) *Builder {
	engine.config.IndexPrefix = value

	return engine
}

// Nodes replaces additional cluster addresses and returns this draft. It copies
// the input slice; NewFrom ignores nodes because its client is already built.
//
// Example:
//
//	driver, err := opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
//		engine.Nodes("http://os2:9200", "http://os3:9200")
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (engine *Builder) Nodes(values ...string) *Builder {
	engine.config.Nodes = slices.Clone(values)

	return engine
}

// Username replaces the HTTP basic-auth username and returns this draft.
// Empty sends no username; NewFrom ignores client connection settings.
//
// Example:
//
//	driver, err := opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
//		engine.Username("search")
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (engine *Builder) Username(value string) *Builder {
	engine.config.Username = value

	return engine
}

// Password replaces the HTTP basic-auth password and returns this draft.
// The native client retains it; NewFrom ignores client connection settings.
//
// Example:
//
//	driver, err := opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
//		engine.Password(os.Getenv("OPENSEARCH_PASSWORD"))
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (engine *Builder) Password(value string) *Builder {
	engine.config.Password = value

	return engine
}

// Client replaces the native construction hook and returns this draft. New
// invokes it directly once after base settings and basic validation, then
// snapshots the completed configuration. Start constructs the client later and
// never replays the hook. Nil disables it. NewFrom never invokes it. Native
// objects and hook captures are shared; owned containers and scalar pointers
// detach from retained hook drafts. Hooks must not perform lifecycle I/O.
//
// Example:
//
//	driver, err := opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
//		engine.Client(func(config *opensearchapi.Config) {
//			config.Client.DisableRetry = true
//		})
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (engine *Builder) Client(configure func(*opensearchapi.Config)) *Builder {
	engine.config.Client = configure

	return engine
}

// maxPrefixLength is the longest [Builder.IndexPrefix], in bytes. With the
// longest index name, 64 bytes, the result stays far below OpenSearch's limit
// of 255 bytes.
const maxPrefixLength = 64

// settings configures the OpenSearch [Driver] built by [New].
//
// [NewFrom] uses only IndexPrefix: the client it wraps is already configured.
type settings struct {
	// IndexPrefix is put in front of every index name, so several
	// applications or environments can share a cluster: the index "products"
	// of an engine with the prefix "shop_" is the OpenSearch index
	// "shop_products". It is 1 to 64 bytes of [a-z0-9_], or empty (the
	// default) for no prefix. Without a prefix, an index name that starts with
	// an underscore is rejected, because OpenSearch forbids it.
	IndexPrefix string

	// Nodes lists further node addresses, such as "http://os2:9200", beside
	// the address passed to [New]. The client spreads requests over all of
	// them. The default is none. [NewFrom] ignores it.
	Nodes []string

	// Username and Password enable HTTP basic authentication. The default,
	// empty, sends no credentials. [NewFrom] ignores them.
	Username, Password string

	// Client adjusts the client configuration after the settings above are
	// applied and before the client is built. Use it for what the driver does
	// not cover: a CA certificate, an API key, request signing, retries, or
	// node discovery. A nil function changes nothing. [NewFrom] ignores it.
	Client func(*opensearchapi.Config)
}

// checkPrefix reports whether prefix is empty or 1 to [maxPrefixLength] bytes
// of [a-z0-9_]. The error wraps [search.ErrInvalidName].
func checkPrefix(prefix string) error {
	if len(prefix) > maxPrefixLength {
		return fmt.Errorf("opensearch: index prefix: %w: want at most %d bytes", search.ErrInvalidName, maxPrefixLength)
	}

	for _, char := range []byte(prefix) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '_':
		default:
			return fmt.Errorf("opensearch: index prefix %q: %w: want only a-z, 0-9, and _", prefix, search.ErrInvalidName)
		}
	}

	return nil
}
