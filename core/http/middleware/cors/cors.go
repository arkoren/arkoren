package cors

import (
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/http/router"
)

// settings configures the Cross-Origin Resource Sharing
// (CORS) middleware behavior. Each field maps directly to a
// CORS response header.
type settings struct {
	// AllowedOrigins is the list of origins permitted to make
	// cross-origin requests. "*" allows any origin without credentials.
	// Other entries must be bare origins, not URL or subdomain patterns.
	AllowedOrigins []string

	// AllowedMethods is the list of HTTP methods allowed in
	// cross-origin requests. These are returned in the
	// Access-Control-Allow-Methods header for preflight requests.
	AllowedMethods []string

	// AllowedHeaders is the list of request headers allowed in
	// cross-origin requests. These are returned in the
	// Access-Control-Allow-Headers header for preflight requests.
	AllowedHeaders []string

	// ExposedHeaders is the list of response headers that
	// browsers are allowed to access. These are returned in
	// the Access-Control-Expose-Headers header.
	ExposedHeaders []string

	// AllowCredentials indicates whether the request can include
	// credentials such as cookies, HTTP authentication, or client
	// TLS certificates. When true, the wildcard "*" must not be
	// used in AllowedOrigins.
	AllowCredentials bool

	// MaxAge is how long browsers may cache preflight results. It is
	// rounded down to whole seconds in the header. Nonpositive values omit the
	// Access-Control-Max-Age header; [Middleware] defaults to 5 minutes.
	MaxAge time.Duration
}

// Builder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(cors.Middleware(func(policy *cors.Builder) {
//		policy.AllowedOrigins("https://app.example.com")
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// AllowedOrigins is the list of origins permitted to make
// cross-origin requests. "*" allows any origin without credentials; combining
// it with AllowCredentials(true) panics at publication. Other entries must be
// bare ASCII origins with a scheme, hostname, and optional port in [0, 65535].
// Userinfo, paths, query/fragment delimiters, and wildcard host patterns panic.
// Hosts and schemes are lowercased; international hosts must use ASCII punycode.
// A nil list restores defaults; a nonnil empty list grants no origins.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(cors.Middleware(func(policy *cors.Builder) {
//		policy.AllowedOrigins("https://app.example.com")
//	}))
func (policy *Builder) AllowedOrigins(origins ...string) *Builder {
	policy.settings.AllowedOrigins = slices.Clone(origins)

	return policy
}

// AllowedMethods is the list of HTTP methods allowed in
// cross-origin requests. These are returned in the
// Access-Control-Allow-Methods header for preflight requests.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(cors.Middleware(func(policy *cors.Builder) {
//		policy.AllowedMethods(http.MethodGet)
//	}))
func (policy *Builder) AllowedMethods(methods ...string) *Builder {
	policy.settings.AllowedMethods = slices.Clone(methods)

	return policy
}

// AllowedHeaders is the list of request headers allowed in
// cross-origin requests. These are returned in the
// Access-Control-Allow-Headers header for preflight requests.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(cors.Middleware(func(policy *cors.Builder) {
//		policy.AllowedHeaders("Content-Type")
//	}))
func (policy *Builder) AllowedHeaders(headers ...string) *Builder {
	policy.settings.AllowedHeaders = slices.Clone(headers)

	return policy
}

// ExposedHeaders is the list of response headers that
// browsers are allowed to access. These are returned in
// the Access-Control-Expose-Headers header.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(cors.Middleware(func(policy *cors.Builder) {
//		policy.ExposedHeaders("X-Trace-ID")
//	}))
func (policy *Builder) ExposedHeaders(headers ...string) *Builder {
	policy.settings.ExposedHeaders = slices.Clone(headers)

	return policy
}

// AllowCredentials indicates whether the request can include
// credentials such as cookies, HTTP authentication, or client
// TLS certificates. When true, the wildcard "*" must not be
// used in AllowedOrigins.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(cors.Middleware(func(policy *cors.Builder) {
//		policy.AllowCredentials(false)
//	}))
func (policy *Builder) AllowCredentials(allow bool) *Builder {
	policy.settings.AllowCredentials = allow

	return policy
}

// MaxAge is how long browsers may cache preflight results. It is
// rounded down to whole seconds in the header. Nonpositive values omit the
// Access-Control-Max-Age header; [Middleware] defaults to 5 minutes.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(cors.Middleware(func(policy *cors.Builder) {
//		policy.MaxAge(time.Second)
//	}))
func (policy *Builder) MaxAge(maxAge time.Duration) *Builder {
	policy.settings.MaxAge = maxAge

	return policy
}

// defaultConfig returns the default CORS configuration.
func defaultConfig() settings {
	return settings{
		AllowedOrigins: []string{
			"*",
		},
		AllowedMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodHead,
		},
		AllowedHeaders: []string{
			"Accept",
			"Content-Type",
		},
		MaxAge: 5 * time.Minute,
	}
}

// policy holds the CORS response values computed once from a config.
type policy struct {
	// origins is the normalized allow list.
	origins []string

	// anyOrigin reports whether responses use the "*" origin.
	anyOrigin bool

	// credentials reports whether credentials are allowed.
	credentials bool

	// methods, headers, exposed, and maxAge are the joined header values;
	// empty values are omitted.
	methods, headers, exposed, maxAge string
}

// Middleware creates a Cross-Origin Resource Sharing middleware with
// the completed builder settings. It sets CORS headers for allowed origins and
// answers their preflight OPTIONS requests with 204 No Content. Other requests
// continue to the next handler without CORS permission headers.
//
// It panics after all callbacks when an allowed origin is not "*" or a bare
// ASCII origin such as "https://example.com", or when credentials are combined
// with "*". Origins cannot contain userinfo, a path, query/fragment delimiters,
// wildcard host patterns, or a port outside [0, 65535].
//
// Without callbacks it allows common JSON API usage without credentials. Nil
// lists receive defaults after callbacks; explicit empty lists remain empty.
//
// Example:
//
//	routes.Use(cors.Middleware(func(policy *cors.Builder) {
//		policy.AllowedOrigins("https://app.example.com")
//		policy.AllowCredentials(true)
//	}))
func Middleware(configure ...BuilderFunc) router.Middleware {
	policy := newPolicy(newSettings(configure...))

	return func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, request *http.Request) error {
			return policy.handle(writer, request, next)
		}
	}
}

// handle applies precomputed browser permissions to one request. Disallowed
// origins still reach next without CORS headers; CORS is not authorization.
// Recognized allowed preflights commit 204 here. Other requests preserve next's
// response and return its error unchanged.
func (policy policy) handle(writer http.ResponseWriter, request *http.Request, next router.Handler) error {
	header := writer.Header()

	// Responses differ by Origin unless every origin gets "*", so caches must
	// key on it even for requests without an Origin header.
	if !policy.anyOrigin {
		header.Add("Vary", "Origin")
	}

	origin := request.Header.Get("Origin")

	if origin == "" || !policy.allows(origin) {
		return next(writer, request)
	}

	policy.setOrigin(header, origin)

	// preflight distinguishes browser CORS negotiation from ordinary OPTIONS.
	// Only recognized preflights finish here without calling the next handler.
	preflight := request.Method == http.MethodOptions && request.Header.Get("Access-Control-Request-Method") != ""

	if !preflight {
		setHeader(header, "Access-Control-Expose-Headers", policy.exposed)

		return next(writer, request)
	}

	header.Add("Vary", "Access-Control-Request-Method")
	header.Add("Vary", "Access-Control-Request-Headers")
	setHeader(header, "Access-Control-Allow-Methods", policy.methods)
	setHeader(header, "Access-Control-Allow-Headers", policy.headers)
	setHeader(header, "Access-Control-Max-Age", policy.maxAge)
	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// allows reports whether origin is on the allow list.
func (policy policy) allows(origin string) bool {
	return slices.ContainsFunc(policy.origins, func(candidate string) bool {
		return candidate == "*" || candidate == origin
	})
}

// setOrigin writes the allowed origin and credentials headers.
func (policy policy) setOrigin(header http.Header, origin string) {
	if policy.anyOrigin {
		header.Set("Access-Control-Allow-Origin", "*")
	} else {
		header.Set("Access-Control-Allow-Origin", origin)
	}

	if policy.credentials {
		header.Set("Access-Control-Allow-Credentials", "true")
	}
}

// setHeader writes value unless it is empty.
func setHeader(header http.Header, name, value string) {
	if value != "" {
		header.Set(name, value)
	}
}

// withDefaults fills only nil CORS lists from recommended defaults. Explicit
// empty lists remain empty so callers can intentionally disable those
// allowances.
func (config settings) withDefaults() settings {
	defaults := defaultConfig()
	config.AllowedOrigins = sliceOrDefault(config.AllowedOrigins, defaults.AllowedOrigins)
	config.AllowedMethods = sliceOrDefault(config.AllowedMethods, defaults.AllowedMethods)
	config.AllowedHeaders = sliceOrDefault(config.AllowedHeaders, defaults.AllowedHeaders)

	return config
}

// sliceOrDefault returns fallback only when value is nil.
func sliceOrDefault(value []string, fallback []string) []string {
	if value != nil {
		return value
	}

	return fallback
}
