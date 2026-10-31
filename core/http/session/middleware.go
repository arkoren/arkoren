package session

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"strings"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session/internal/persistence"
	"arkoren.dev/core/internal/hook"
	"arkoren.dev/core/service/cache"
)

// BuilderFunc configures a Builder.
// Example:
//
//	var policy session.BuilderFunc = func(cookies *session.Builder) {
//		cookies.TTL(time.Hour)
//	}
//	_ = session.Middleware(store, policy)
type BuilderFunc = func(*Builder)

// settings configures the session middleware behavior including cookie
// attributes, session lifetime, and the context key used to store the session
// in the request.
type settings struct {
	// Name is the cookie name sent to the client. Empty selects
	// "__Host-arkoren.session" when the cookie is Secure, has Path=/ and no
	// Domain, and "arkoren.session" otherwise. An explicit name is never
	// altered.
	Name string

	// Namespace names raw JSON session records. Lifetime policy is independent
	// of its TTL and always overrides it for writes and tombstones.
	Namespace cache.Namespace[jsontext.Value]

	// Path restricts the cookie to the given URL path prefix.
	Path string

	// Domain restricts the cookie to the given domain.
	Domain string

	// AllowInsecure explicitly permits cookies over HTTP. This is intended only
	// for local development; production deployments should leave it false.
	AllowInsecure bool

	// SameSite controls cross-site cookie behavior. Supported values are "lax",
	// "strict", "none", and "default".
	SameSite string

	// Partitioned enables the CHIPS partitioned cookie attribute.
	Partitioned bool

	// TTL is the idle lifetime from creation or renewal. MaxLifetime caps
	// the resulting expiry using the session's original creation time.
	TTL time.Duration

	// MaxLifetime is the absolute maximum duration a session may exist from its
	// initial creation, regardless of activity. A zero value uses the default
	// absolute lifetime. Default: 24 hours; negative disables the maximum.
	MaxLifetime time.Duration

	// ExpirationDelta is the remaining time threshold at which an active session
	// is automatically extended by a full TTL.
	ExpirationDelta time.Duration

	// Handle selects the request bridge, independently of cookie and storage
	// policy. Its zero value selects the default session.
	Handle Handle

	// ErrorHandler is an optional callback invoked when internal session
	// operations fail. When nil, errors are silently discarded.
	ErrorHandler func(error)

	// PersistenceTimeout bounds primary session persistence operations before
	// response headers. Best-effort lease release has a separate ten-second
	// cleanup budget. Zero defaults to five seconds.
	PersistenceTimeout time.Duration

	// Events receives [Expired], [Destroyed], and [PersistFailed]. Nil, the
	// default, publishes nothing.
	Events *event.Bus
}

// Builder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.TTL(time.Hour)
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// middlewareRuntime holds the request-time settings derived from [settings].
type middlewareRuntime struct {
	// Handle selects the request bridge populated by this middleware.
	Handle Handle

	// ErrorHandler is an optional callback invoked when internal session
	// operations fail. When nil, errors are silently discarded.
	ErrorHandler func(error)

	// PersistenceTimeout bounds primary persistence before response headers,
	// not the independent best-effort lease-release budget. Zero defaults to
	// five seconds.
	PersistenceTimeout time.Duration
}

const (
	// HostPrefix makes browsers accept the cookie only when it is Secure, has
	// Path=/ and no Domain (RFC 6265bis), so sibling subdomains cannot set it.
	// [Middleware] panics when the configuration cannot satisfy these rules.
	//
	// 	session.Middleware(store, func(cookies *session.Builder) {
	// 	cookies.Name(session.HostPrefix+"arkoren.session")
	// })
	HostPrefix = "__Host-"

	// SecurePrefix makes browsers accept the cookie only when it is Secure. It
	// still allows Domain, so sessions can be shared across subdomains.
	//
	// 	session.Middleware(store, func(cookies *session.Builder) {
	// 	cookies.Name(session.SecurePrefix+"arkoren.session")
	// }, func(sessionPolicy *session.Builder) {
	// 	sessionPolicy.Domain("example.com")
	// })
	SecurePrefix = "__Secure-"

	// DefaultCookie is the base session cookie name. When no name is set,
	// [Middleware] uses it with the [HostPrefix] prefix, or bare when the
	// configuration cannot satisfy that prefix (see [Builder.Name]).
	//
	// Example: name := session.HostPrefix + session.DefaultCookie
	DefaultCookie = "arkoren.session"

	// DefaultTTL is the default total session lifetime.
	// It is an idle lifetime; a positive maximum can cap the resulting expiry.
	//
	// Example: _ = session.Middleware(store, func(cookies *session.Builder) {
	// 	cookies.TTL(session.DefaultTTL)
	// })
	DefaultTTL = 2 * time.Hour

	// DefaultMaxLifetime is the default absolute maximum session age.
	// It remains anchored at original creation across regeneration and renewal.
	//
	// Example: _ = session.Middleware(store, func(cookies *session.Builder) {
	// 	cookies.MaxLifetime(session.DefaultMaxLifetime)
	// })
	DefaultMaxLifetime = 24 * time.Hour

	// DefaultExpirationDelta is the default remaining-time threshold that triggers
	// automatic session extension.
	//
	// Example: _ = session.Middleware(store, func(cookies *session.Builder) {
	// 	cookies.ExpirationDelta(session.DefaultExpirationDelta)
	// })
	DefaultExpirationDelta = 15 * time.Minute

	// defaultPersistenceTimeout bounds session storage before headers are
	// written.
	defaultPersistenceTimeout = 5 * time.Second
)

// Name is the cookie name sent to the client. Empty selects
// "__Host-arkoren.session" when the cookie is Secure, has Path=/ and no
// Domain, and "arkoren.session" otherwise. An explicit name is never
// altered.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.Name("session")
//	}))
func (sessionPolicy *Builder) Name(name string) *Builder {
	sessionPolicy.settings.Name = name

	return sessionPolicy
}

// Namespace selects the namespace of raw JSON session records. Its separator
// joins the session ID to the name; the default produces "arkoren:session:ID".
// A zero or invalid namespace causes Middleware to panic after all callbacks.
// Session idle and maximum lifetimes, not the namespace TTL, govern security:
// each record and tombstone write overrides TTL with its runtime lifetime.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.Namespace(cache.NewNamespace[jsontext.Value]("app.sessions"))
//	}))
func (sessionPolicy *Builder) Namespace(space cache.Namespace[jsontext.Value]) *Builder {
	sessionPolicy.settings.Namespace = space

	return sessionPolicy
}

// Path restricts the cookie to the given URL path prefix.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.Path("/")
//	}))
func (sessionPolicy *Builder) Path(path string) *Builder {
	sessionPolicy.settings.Path = path

	return sessionPolicy
}

// Domain restricts the cookie to the given domain.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.Domain("example.com")
//	}))
func (sessionPolicy *Builder) Domain(domain string) *Builder {
	sessionPolicy.settings.Domain = domain

	return sessionPolicy
}

// AllowInsecure explicitly permits cookies over HTTP. This is intended only
// for local development; production deployments should leave it false.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.AllowInsecure(false)
//	}))
func (sessionPolicy *Builder) AllowInsecure(allow bool) *Builder {
	sessionPolicy.settings.AllowInsecure = allow

	return sessionPolicy
}

// SameSite controls cross-site cookie behavior. Supported values are "lax",
// "strict", "none", and "default".
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.SameSite("lax")
//	}))
func (sessionPolicy *Builder) SameSite(sameSite string) *Builder {
	sessionPolicy.settings.SameSite = sameSite

	return sessionPolicy
}

// Partitioned enables the CHIPS partitioned cookie attribute.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.Partitioned(false)
//	}))
func (sessionPolicy *Builder) Partitioned(partitioned bool) *Builder {
	sessionPolicy.settings.Partitioned = partitioned

	return sessionPolicy
}

// TTL selects the idle lifetime from creation or renewal. A positive
// MaxLifetime caps the resulting expiry at the original absolute deadline.
// Zero selects the default two hours; a negative final TTL makes Middleware
// panic after all construction callbacks, rather than publishing invalid policy.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.TTL(time.Second)
//	}))
func (sessionPolicy *Builder) TTL(ttl time.Duration) *Builder {
	sessionPolicy.settings.TTL = ttl

	return sessionPolicy
}

// MaxLifetime caps initial expiry, renewal, and application-selected expiry at
// this duration after original creation. A zero value uses 24 hours; negative
// disables the cap. An expired absolute deadline discards later saves and
// cookies rather than granting a fresh lifetime to the same authenticated state.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.MaxLifetime(time.Second)
//	}))
func (sessionPolicy *Builder) MaxLifetime(lifetime time.Duration) *Builder {
	sessionPolicy.settings.MaxLifetime = lifetime

	return sessionPolicy
}

// ExpirationDelta is the remaining-time threshold for idle renewal by TTL,
// still capped by MaxLifetime. Zero selects the default fifteen minutes;
// negative disables near-expiry renewal, including the minimum time.Duration.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.ExpirationDelta(time.Second)
//	}))
func (sessionPolicy *Builder) ExpirationDelta(delta time.Duration) *Builder {
	sessionPolicy.settings.ExpirationDelta = delta

	return sessionPolicy
}

// Handle selects the request bridge populated by this middleware. Zero selects
// the default read by FromContext. This does not change cookie names, storage
// namespaces, or lifetime; configure these separately for independent stores.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.Handle(applicationSession)
//	}))
func (sessionPolicy *Builder) Handle(handle Handle) *Builder {
	sessionPolicy.settings.Handle = handle

	return sessionPolicy
}

// Events receives [Expired], [Destroyed], and [PersistFailed]. Nil, the
// default, publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.Events(event.New())
//	}))
func (sessionPolicy *Builder) Events(bus *event.Bus) *Builder {
	sessionPolicy.settings.Events = bus

	return sessionPolicy
}

// ErrorHandler is an optional callback invoked when internal session
// operations fail. When nil, errors are silently discarded.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(cookies *session.Builder) {
//		cookies.ErrorHandler(nil)
//	}))
func (sessionPolicy *Builder) ErrorHandler(handler func(error)) *Builder {
	sessionPolicy.settings.ErrorHandler = handler

	return sessionPolicy
}

// PersistenceTimeout bounds primary persistence before response headers are
// written. Zero defaults to five seconds. Lease release ignores request
// cancellation but has a separate ten-second best-effort cleanup deadline;
// release failure falls back to lease expiry. These bounds require the cache
// driver to honor its context and do not bound application error/event callbacks.
// A negative final timeout makes Middleware panic after construction callbacks;
// use a positive duration rather than an immediately canceled persistence policy.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.PersistenceTimeout(time.Second)
//	}))
func (sessionPolicy *Builder) PersistenceTimeout(timeout time.Duration) *Builder {
	sessionPolicy.settings.PersistenceTimeout = timeout

	return sessionPolicy
}

// expectedSessionIDLength is the expected length of a valid session ID.
const expectedSessionIDLength = 43

// sameSiteModes maps supported configuration values to HTTP SameSite modes.
var sameSiteModes = map[string]http.SameSite{
	"":        http.SameSiteLaxMode,
	"default": http.SameSiteDefaultMode,
	"lax":     http.SameSiteLaxMode,
	"none":    http.SameSiteNoneMode,
	"strict":  http.SameSiteStrictMode,
}

// validSessionID reports whether id is exactly 43 base64url characters.
func validSessionID(id string) bool {
	if len(id) != expectedSessionIDLength {
		return false
	}

	for _, character := range []byte(id) {
		valid := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '_'

		if !valid {
			return false
		}
	}

	return true
}

// cookieValue returns the named cookie value or an empty string.
func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)

	if err != nil {
		return ""
	}

	return cookie.Value
}

// sameSiteMode converts a configuration value to an HTTP SameSite mode.
func sameSiteMode(value string) http.SameSite {
	mode, ok := sameSiteModes[value]

	if !ok {
		panic("session middleware: invalid SameSite value")
	}

	return mode
}

// currentSession loads an existing session from the cookie-provided ID or creates
// a fresh one when no valid session is found.
func currentSession(r *http.Request, driver sessionStore, config settings) (*Session, error) {
	id := cookieValue(r, config.Name)

	if id == "" || !validSessionID(id) {
		return newSession(config)
	}

	// An unknown ID starts a fresh session; a storage failure is an error, so
	// an outage does not silently log every user out.
	session, found, err := driver.get(r.Context(), id)

	if err != nil {
		return nil, err
	}

	if !found {
		return newSession(config)
	}

	return usableSession(r, driver, config, id, session)
}

// newSession creates a fresh session using the configured lifetime. It
// starts unchanged, so it is stored and gets a cookie only once the handler
// writes to it; clients that never use the session create no records.
func newSession(config settings) (*Session, error) {
	session := New(time.Now().Add(config.TTL), map[string]any{})
	session.Extend(boundedSessionExpiry(config, session.CreatedAt(), session.ExpiresAt()))
	session.MarkAsUnchanged()

	return session, nil
}

// usableSession returns session when it remains valid or replaces it otherwise.
func usableSession(
	r *http.Request,
	driver sessionStore,
	config settings,
	id string,
	session *Session,
) (*Session, error) {
	if sessionExpired(config, session) {
		// Expired sessions are rejected regardless of whether best-effort deletion succeeds.
		_ = driver.Delete(r.Context(), id)

		config.Events.Emit(r.Context(), Expired, ExpiredEvent{
			CreatedAt: session.CreatedAt(),
			ExpiresAt: session.ExpiresAt(),
		})

		return newSession(config)
	}

	session.MarkAsUnchanged()

	return session, nil
}

// sessionExpired reports whether session cannot be reused.
func sessionExpired(config settings, session *Session) bool {
	if session.HasExpired() {
		return true
	}

	return config.MaxLifetime > 0 && time.Since(session.CreatedAt()) >= config.MaxLifetime
}

// Middleware returns session middleware configured with the given driver and
// configuration.
//
// Changes are saved just before the response status is written, and never
// for 5xx responses, so a failing handler does not persist partial state.
// Concurrent requests of one client normally save their own copy in lease
// order (last-write-wins), but a retired session cannot be revived: destroying a
// session, or replacing its ID by regeneration (logout, login), overwrites
// the old record with a tombstone that lives for [Builder.MaxLifetime]. A
// tombstoned ID loads as a fresh session, and a request that loaded the
// session before it was retired re-reads its record before saving and drops
// the save, without a cookie, when the record is gone or tombstoned.
//
// A loaded session's save atomically compares the exact re-read original
// record and writes the current ID using cache.Driver.CompareAndPut. This
// also guards regenerated IDs: a tombstone or missing original rejects the
// write without issuing a cookie. A changed guard drops the save as well.
// Per-ID ten-second cache leases reduce conflicts but do not provide the
// correctness guarantee; expiry cannot let a pending save bypass retirement.
// Only changed loaded sessions and retirement take a lease. Acquisition or
// storage failures within [Builder.PersistenceTimeout] are persistence errors.
// Retiring an old ID does not revoke a different ID committed before retirement.
// Upgrade all writers and drain older requests before relying on this fencing;
// unguarded writes or restoring an old live record bypass it. Keys and JSON
// representations are unchanged.
// Initial and renewed expiry never exceed the original absolute deadline;
// a handler that reaches it cannot publish another session record or cookie.
// Lease cleanup can use an additional ten seconds independently of persistence
// cancellation, provided the backend honors its cleanup context.
// Changes are not saved on hijacked connections such as WebSocket upgrades.
//
// Sessions are stored as JSON records in the configured namespace, joined with
// the session ID, so any cache driver works as the session store. A missing
// record starts a fresh session; any other cache error fails the request.
// Construction panics for negative TTL or PersistenceTimeout after callbacks
// and default resolution. Negative MaxLifetime and ExpirationDelta retain their
// documented disabled meanings; zero values still select defaults.
//
// Example:
//
//	routes.Use(session.Middleware(store, func(cookies *session.Builder) {
//		cookies.Namespace(cache.NewNamespace[jsontext.Value]("app.sessions"))
//	}))
func Middleware(store *cache.Cache, configure ...BuilderFunc) router.Middleware {
	config := newSettings(configure...)

	if err := config.Namespace.Err(); err != nil {
		panic("session: invalid namespace: " + err.Error())
	}

	validateDurations(config)
	validatePrefix(config)

	runtime := middlewareRuntime{
		Handle:             config.Handle,
		ErrorHandler:       config.ErrorHandler,
		PersistenceTimeout: config.PersistenceTimeout,
	}
	sameSite := sameSiteMode(config.SameSite)

	return sessionHandler(sessionStore{
		Store: persistence.Store{Driver: store.Driver(), Locks: store, Space: config.Namespace},
	}, config, runtime, sameSite)
}

// validateDurations rejects final policies that cannot create a live session or
// allow any persistence work. Zero values were already defaulted; negative
// maximum age and renewal windows intentionally remain valid disabled settings.
func validateDurations(config settings) {
	if config.TTL < 0 {
		panic("session: TTL must be positive")
	}

	if config.PersistenceTimeout < 0 {
		panic("session: PersistenceTimeout must be positive")
	}
}

// validatePrefix panics when the cookie attributes violate the rules of a
// [HostPrefix] or [SecurePrefix] name.
func validatePrefix(config settings) {
	host := strings.HasPrefix(config.Name, HostPrefix)

	if !host && !strings.HasPrefix(config.Name, SecurePrefix) {
		return
	}

	if config.AllowInsecure {
		panic("session: " + config.Name + " requires a Secure cookie; set AllowInsecure(false)")
	}

	if host && (config.Domain != "" || config.Path != "/") {
		panic("session: " + HostPrefix + " names require Path=/ and no Domain")
	}
}

// sessionHandler creates middleware with normalized session settings.
func sessionHandler(
	driver sessionStore,
	config settings,
	runtime middlewareRuntime,
	sameSite http.SameSite,
) router.Middleware {
	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			session, err := currentSession(r, driver, config)

			if err != nil {
				return err
			}

			registry, ok := hook.FromContext(r.Context())

			if !ok {
				panic(hook.ErrNoHooks)
			}

			registry.BeforeWriteHeader(persistSession(w, r, session, driver, config, runtime, sameSite))

			ctx := NewContextFor(r.Context(), runtime.Handle, session)

			return next(w, r.WithContext(ctx))
		}
	}
}

// renewSession chooses the configured idle renewal, caps it at the original
// absolute deadline, then applies only a real expiry change. A capped no-op must
// not mark an otherwise unused new session dirty or cause unnecessary cookies.
func renewSession(config settings, session *Session) {
	expired := session.HasExpired()
	expiresAt := session.ExpiresAt()

	if expired {
		session.Regenerate()
	}

	if expired || session.ExpiresSoon(config.ExpirationDelta) {
		expiresAt = time.Now().Add(config.TTL)
	}

	expiresAt = boundedSessionExpiry(config, session.CreatedAt(), expiresAt)

	if !expiresAt.Equal(session.ExpiresAt()) {
		session.Extend(expiresAt)
	}
}

// boundedSessionExpiry caps an initial, renewed, or application-selected expiry
// at the original absolute deadline. A disabled maximum leaves expiry alone.
// The calculation has no side effects, so a capped no-op does not become a
// mutation. A past result is deliberately retained for expiry rejection.
func boundedSessionExpiry(config settings, createdAt, expiresAt time.Time) time.Time {
	if config.MaxLifetime <= 0 {
		return expiresAt
	}

	deadline := createdAt.Add(config.MaxLifetime)

	if expiresAt.After(deadline) {
		return deadline
	}

	return expiresAt
}

// reportPersistenceError publishes [PersistFailed] and calls the optional
// error callback for a non-nil err; a nil err does nothing. The event gets
// ctx's values without its deadline, which a timed-out save has already
// exhausted.
func reportPersistenceError(
	ctx context.Context,
	r *http.Request,
	config settings,
	runtime middlewareRuntime,
	operation string,
	err error,
) {
	if err == nil {
		return
	}

	config.Events.Emit(context.WithoutCancel(ctx), PersistFailed, PersistFailedEvent{
		Operation: operation,
		Method:    r.Method,
		Path:      sessionEventPath(r),
	})

	if runtime.ErrorHandler != nil {
		runtime.ErrorHandler(err)
	}
}

// sessionEventPath returns a registered pattern without its method prefix,
// never an expanded URL path. Direct Handler use without a matched pattern
// returns empty, rather than disclosing untrusted path parameters or queries.
func sessionEventPath(r *http.Request) string {
	_, path, found := strings.Cut(r.Pattern, " ")

	if found {
		return path
	}

	return r.Pattern
}

// setSessionCookie writes the cookie representing session.
func setSessionCookie(
	w http.ResponseWriter,
	config settings,
	sameSite http.SameSite,
	session *Session,
	ttl time.Duration,
) {
	http.SetCookie(w, &http.Cookie{
		Name:        config.Name,
		Value:       session.ID(),
		Path:        config.Path,
		Domain:      config.Domain,
		Expires:     session.ExpiresAt(),
		MaxAge:      int(ttl.Seconds()),
		Secure:      !config.AllowInsecure,
		HttpOnly:    true,
		SameSite:    sameSite,
		Partitioned: config.Partitioned,
	})
}

// expireSessionCookie tells the client to drop its session cookie.
func expireSessionCookie(w http.ResponseWriter, config settings, sameSite http.SameSite) {
	http.SetCookie(w, &http.Cookie{
		Name:        config.Name,
		Path:        config.Path,
		Domain:      config.Domain,
		MaxAge:      -1,
		Secure:      !config.AllowInsecure,
		HttpOnly:    true,
		SameSite:    sameSite,
		Partitioned: config.Partitioned,
	})
}
