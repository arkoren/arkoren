package restate

import (
	"log/slog"
	"net/http"
	"slices"
)

// Config is a redacted reporting record for host settings, not a constructor
// input. Configure [New] through [Builder]. String, GoString, and LogValue
// redact nonempty AuthKey and AdminAuthKey fields; URL fields are unchanged.
//
// Example:
//
//	record := Config{AuthKey: "secret"}
//	slog.Info("host settings", "config", record)
type Config struct {
	// Addr is the listen address of the service endpoint. The default is
	// ":9080".
	//
	// Example:
	//
	//	fmt.Println(Config{Addr: ":9080"}.Addr)
	Addr string

	// IngressURL is the base URL of the Restate ingress. The default is
	// "http://localhost:8080".
	//
	// Example:
	//
	//	fmt.Println(Config{IngressURL: "http://localhost:8080"}.IngressURL)
	IngressURL string

	// AdminURL is the base URL of the Restate admin API. The default is
	// "http://localhost:9070".
	//
	// Example:
	//
	//	fmt.Println(Config{AdminURL: "http://localhost:9070"}.AdminURL)
	AdminURL string

	// AdvertisedURL is the URL the Restate server uses to reach the endpoint.
	// The default is "http://localhost" followed by Addr.
	//
	// Example:
	//
	//	fmt.Println(Config{AdvertisedURL: "http://localhost:9080"}.AdvertisedURL)
	AdvertisedURL string

	// Register makes [Host.Serve] register the deployment with the admin API
	// once the endpoint is serving.
	//
	// Example:
	//
	//	fmt.Println(Config{Register: true}.Register)
	Register bool

	// RegisterForce sends force=true with the registration, which overwrites
	// a deployment that serves the same URL. Use it only when no invocation
	// of the old deployment is in flight.
	//
	// Example:
	//
	//	fmt.Println(Config{}.RegisterForce) // False: replacement is not forced.
	RegisterForce bool

	// RegisterBreaking sends breaking=true with the registration, which
	// accepts a deployment that removes services or handlers of an earlier
	// deployment under the same URL.
	//
	// Example:
	//
	//	fmt.Println(Config{}.RegisterBreaking) // False: breaking removal is not allowed.
	RegisterBreaking bool

	// ClientOnly makes the host a pure client of the Restate server:
	// [Host.Start] opens no listener, [Host.Serve] registers nothing, and
	// [Host.Bind] returns [ErrClientOnly]. A host with nothing bound behaves
	// the same way.
	//
	// Example:
	//
	//	fmt.Println(Config{ClientOnly: true}.ClientOnly)
	ClientOnly bool

	// IdentityKeys are the Restate request-identity public keys
	// ("publickeyv1_...") the endpoint accepts. The Restate server signs each
	// request to the endpoint, and the endpoint rejects requests that are
	// not signed by one of these keys. Take them from the Restate server
	// configuration (request identity). Empty leaves the endpoint
	// unauthenticated, which [Host.Start] refuses unless AllowInsecure is set.
	// This reporting record does not validate native key syntax or own its slice.
	//
	// Example:
	//
	//	fmt.Println(len(Config{IdentityKeys: []string{"publickeyv1_example"}}.IdentityKeys))
	IdentityKeys []string

	// AllowInsecure lets [Host.Start] open a service endpoint without
	// IdentityKeys. Use it only for local development and tests: anyone who
	// can reach the listener can invoke the bound handlers. The default is
	// false.
	//
	// Example:
	//
	//	fmt.Println(Config{}.AllowInsecure) // False: unsigned serving is not permitted.
	AllowInsecure bool

	// AuthKey is the bearer token sent to the ingress. Empty sends none.
	// Ordinary Config formatting redacts it; direct field access does not.
	//
	// Example:
	//
	//	slog.InfoContext(ctx, "settings", "config", Config{AuthKey: "example"})
	AuthKey string

	// AdminAuthKey is the bearer token sent to the admin API. Empty sends
	// none.
	// Ordinary Config formatting redacts it; direct field access does not.
	//
	// Example:
	//
	//	slog.InfoContext(ctx, "settings", "config", Config{AdminAuthKey: "example"})
	AdminAuthKey string

	// HTTPClient is used for ingress and admin calls. Nil uses a client with
	// default settings.
	// The native client remains shared; Host.Stop does not close its transport.
	//
	// Example:
	//
	//	record := Config{HTTPClient: http.DefaultClient}
	//	_ = record.HTTPClient
	HTTPClient *http.Client

	// Logger receives Host log lines. Nil uses [slog.Default].
	//
	// Example:
	//
	//	record := Config{Logger: slog.Default()}
	//	record.Logger.InfoContext(ctx, "configured host")
	Logger *slog.Logger
}

// Builder edits the construction settings of [New]. Callbacks share one
// fresh draft and run once in order; a nil callback panics. Retaining a draft
// does not permit changes to the published component. Dependencies and native
// SDK internals retain their existing ownership.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.Logger(slog.Default())
//	})
//	_ = component
type Builder struct {
	// config holds this draft's optional settings until publication.
	config Config
}

// buildSettings applies construction callbacks directly and publishes a detached
// settings root. An empty list preserves defaults; nil entries panic.
func buildSettings(configure ...BuilderFunc) Config {
	config := Config{
		Addr:       ":9080",
		IngressURL: "http://localhost:8080",
		AdminURL:   "http://localhost:9070",
	}
	host := &Builder{
		config: config,
	}

	for _, configure := range configure {
		configure(host)
	}

	config = host.config
	config.IdentityKeys = slices.Clone(config.IdentityKeys)

	return config
}

// Addr is the listen address of the service endpoint. The default is
// ":9080".
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.Addr(":9080")
//	})
//	_ = component
func (host *Builder) Addr(addr string) *Builder {
	host.config.Addr = addr

	return host
}

// IngressURL is the base URL of the Restate ingress. The default is
// "http://localhost:8080".
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.IngressURL("http://localhost:8080")
//	})
//	_ = component
func (host *Builder) IngressURL(url string) *Builder {
	host.config.IngressURL = url

	return host
}

// AdminURL is the base URL of the Restate admin API. The default is
// "http://localhost:9070".
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.AdminURL("http://localhost:9070")
//	})
//	_ = component
func (host *Builder) AdminURL(url string) *Builder {
	host.config.AdminURL = url

	return host
}

// AdvertisedURL is the URL the Restate server uses to reach the endpoint.
// The default is "http://localhost" followed by Addr.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.AdvertisedURL("http://localhost:9080")
//	})
//	_ = component
func (host *Builder) AdvertisedURL(url string) *Builder {
	host.config.AdvertisedURL = url

	return host
}

// Register makes [Host.Serve] register the deployment with the admin API
// once the endpoint is serving.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.Register(true)
//	})
//	_ = component
func (host *Builder) Register(register bool) *Builder {
	host.config.Register = register

	return host
}

// RegisterForce sends force=true with the registration, which overwrites
// a deployment that serves the same URL. Use it only when no invocation
// of the old deployment is in flight.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.RegisterForce(true)
//	})
//	_ = component
func (host *Builder) RegisterForce(force bool) *Builder {
	host.config.RegisterForce = force

	return host
}

// RegisterBreaking sends breaking=true with the registration, which
// accepts a deployment that removes services or handlers of an earlier
// deployment under the same URL.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.RegisterBreaking(true)
//	})
//	_ = component
func (host *Builder) RegisterBreaking(breaking bool) *Builder {
	host.config.RegisterBreaking = breaking

	return host
}

// ClientOnly makes the host a pure client of the Restate server:
// [Host.Start] opens no listener, [Host.Serve] registers nothing, and
// [Host.Bind] returns [ErrClientOnly]. A host with nothing bound behaves
// the same way.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.ClientOnly(true)
//	})
//	_ = component
func (host *Builder) ClientOnly(clientOnly bool) *Builder {
	host.config.ClientOnly = clientOnly

	return host
}

// AdminAuthKey is the bearer token sent to the admin API. Empty sends
// none.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.AdminAuthKey("example")
//	})
//	_ = component
func (host *Builder) AdminAuthKey(key string) *Builder {
	host.config.AdminAuthKey = key

	return host
}

// AuthKey is the bearer token sent to the ingress. Empty sends none.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.AuthKey("example")
//	})
//	_ = component
func (host *Builder) AuthKey(key string) *Builder {
	host.config.AuthKey = key

	return host
}

// HTTPClient is used for ingress and admin calls. Nil uses a client with
// default settings.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.HTTPClient(http.DefaultClient)
//	})
//	_ = component
func (host *Builder) HTTPClient(client *http.Client) *Builder {
	host.config.HTTPClient = client

	return host
}

// Logger receives Host log lines. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.Logger(slog.Default())
//	})
//	_ = component
func (host *Builder) Logger(logger *slog.Logger) *Builder {
	host.config.Logger = logger

	return host
}

// IdentityKeys are the Restate request-identity public keys
// ("publickeyv1_...") the endpoint accepts. The Restate server signs each
// request to the endpoint, and the endpoint rejects requests that are
// not signed by one of these keys. Take them from the Restate server
// configuration (request identity). Empty leaves the endpoint
// unauthenticated, which [Host.Start] refuses unless AllowInsecure is set.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.IdentityKeys("publickeyv1_example")
//	})
//	_ = component
func (host *Builder) IdentityKeys(keys ...string) *Builder {
	host.config.IdentityKeys = slices.Clone(keys)

	return host
}

// AllowInsecure lets [Host.Start] open a service endpoint without
// IdentityKeys. Use it only for local development and tests: anyone who
// can reach the listener can invoke the bound handlers. The default is
// false.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.AllowInsecure(true)
//	})
//	_ = component
func (host *Builder) AllowInsecure(allow bool) *Builder {
	host.config.AllowInsecure = allow

	return host
}

// BuilderFunc is an ordinary callback alias for host construction. New invokes
// callbacks directly once in order; nil entries panic and an empty slice omits
// edits. Retaining a draft does not retain mutation access to the host.
//
// Example:
//
//	policies := []BuilderFunc{func(host *Builder) {
//		host.ClientOnly(true)
//	}}
//	_ = New(policies...)
type BuilderFunc = func(*Builder)
