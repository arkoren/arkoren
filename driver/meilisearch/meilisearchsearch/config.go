package meilisearchsearch

import (
	"fmt"
	"log/slog"
)

// BuilderFunc is a reusable construction policy alias. Policies run once in
// order before validation; nil entries panic and an empty slice makes no edits.
//
// Example:
//
//	var catalog meilisearchsearch.BuilderFunc = func(engine *meilisearchsearch.Builder) {
//		engine.IndexPrefix("shop_")
//	}
//	driver, err := meilisearchsearch.New("http://localhost:7700", catalog)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// Config is a redacted settings record for diagnostics, not constructor input.
// Configure New and NewFrom through Builder instead. The server URL remains
// positional, and formatting this record never reveals a nonempty API key.
//
// Example:
//
//	config := meilisearchsearch.Config{APIKey: "secret", IndexPrefix: "shop_"}
//	slog.Info("search settings", "config", config)
type Config struct {
	// APIKey authenticates every request as a bearer token: the master key or
	// a key allowed to manage indexes, documents, settings, and tasks. Leave
	// it empty for a server started without a master key. It is never printed
	// by [Config.String], [Config.GoString], or [Config.LogValue].
	//
	// Example:
	//
	//	config := meilisearchsearch.Config{APIKey: "test-key"}
	//	fmt.Println(config.String()) // Prints a redacted token, never test-key.
	APIKey string

	// IndexPrefix is prepended to every index name to form the Meilisearch
	// index uid, so several applications or environments can share a server.
	// It holds at most 100 bytes of A-Z, a-z, 0-9, _, and -, the characters
	// a uid allows. The default, empty, uses the index name as the uid.
	//
	// Example:
	//
	//	config := meilisearchsearch.Config{IndexPrefix: "shop_"}
	//	fmt.Println(config.IndexPrefix)
	IndexPrefix string
}

// Builder configures authentication and index naming for New and NewFrom.
// Its zero value sends no API key and leaves index names unchanged. Completed
// settings detach from retained drafts; clients and transports remain shared.
// Callbacks run directly once in order; nil entries panic and nil slices omit.
//
// Example:
//
//	driver, err := meilisearchsearch.New("http://localhost:7700", func(engine *meilisearchsearch.Builder) {
//		engine.IndexPrefix("shop_")
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
type Builder struct {
	// config holds private draft settings; Config remains a redacted record.
	config Config
}

// configureSettings applies callbacks directly to one fresh draft and returns
// an independent scalar record. Callback panics propagate before validation.
func configureSettings(configure []BuilderFunc) Config {
	search := &Builder{}

	for _, configure := range configure {
		configure(search)
	}

	return search.config
}

// APIKey replaces the bearer token and returns this draft. Empty disables
// authentication; the token is retained, not validated against a server.
//
// Example:
//
//	driver, err := meilisearchsearch.New("http://localhost:7700", func(engine *meilisearchsearch.Builder) {
//		engine.APIKey(os.Getenv("MEILI_MASTER_KEY"))
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (engine *Builder) APIKey(value string) *Builder {
	engine.config.APIKey = value

	return engine
}

// IndexPrefix replaces the index uid prefix and returns this draft. Empty
// leaves names unchanged; constructors validate the final prefix's characters.
//
// Example:
//
//	driver, err := meilisearchsearch.New("http://localhost:7700", func(engine *meilisearchsearch.Builder) {
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

// redacted replaces a non-empty API key when a [Config] is formatted.
const redacted = "[REDACTED]"

// plainConfig has the fields of [Config] but none of its methods, so
// formatting it cannot call [Config.String] again.
type plainConfig Config

// redact returns a copy of config whose APIKey is replaced by a placeholder
// when it is set.
func (config Config) redact() plainConfig {
	if config.APIKey != "" {
		config.APIKey = redacted
	}

	return plainConfig(config)
}

// String formats config with APIKey replaced by "[REDACTED]" when set, so %v
// and %+v never print the secret.
//
// Example:
//
//	fmt.Println((meilisearchsearch.Config{APIKey: "test-key"}).String())
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// The returned diagnostic text is not constructor input or serialized settings.
//
// Example:
//
//	fmt.Println((meilisearchsearch.Config{APIKey: "test-key"}).GoString())
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config.
// The API key remains available as a field but is never emitted by this method.
//
// Example:
//
//	config := meilisearchsearch.Config{APIKey: "test-key"}
//	fmt.Println(config.LogValue().String())
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
