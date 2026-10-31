// Package session provides cache-backed HTTP session state and middleware
// for Arkoren applications.
//
// Use [Middleware] to load sessions from cookies, attach them to requests, and
// persist records in any cache driver, such as arkoren.dev/driver/redis/rediscache.
// Construction uses [Builder] callbacks, run directly once in order on a fresh
// draft. Nil entries panic; expanded nil/empty slices supply none. Cookie and
// persistence settings detach at publication; cache and event services stay
// shared. A retained draft cannot change published middleware. Configure a
// shared [Handle] with [Builder.Handle], auth.SessionBuilder.Handle, and
// request.WithSessionHandle; a later zero restores default lookup. Handles are
// comparable opaque selectors, not cookie or storage isolation. Use NewContextFor
// and FromContextFor for explicit bridges; raw context keys do not populate them.
//
// Declare immutable [Field] values with [NewField] to bind literal storage
// names to Go types. Empty and zero fields are invalid; operations reject them
// before mutation. In-memory reads require matching Go types. Persisted values
// remain ordinary JSON, decoded into the requested type without an intermediate
// floating-point representation. Independent declarations with the same name
// still share storage; field types are not persisted as a schema or type tag.
// Existing nil interface fields round-trip as nil for interface definitions;
// missing fields still return ErrKeyNotFound, and mismatched concrete types
// still return ErrInvalidValueType without a JSON conversion.
// jsontext.Value supplied through New or Put is ordinary runtime data, not
// proof of persisted provenance. Only the private persistence decoder enables
// cross-type JSON reads. The outer constructor copy remains shallow; raw slices
// and other mutable values are borrowed, including values exposed through All.
//
//	theme := session.NewField[string]("theme")
//	if err := current.Put(theme, "dark"); err != nil {
//		return err
//	}
//	value, err := current.Get(theme)
//	if err != nil {
//		return err
//	}
//	_ = value
//
// Call [Session.Regenerate] after login or privilege changes to prevent session
// fixation. Regeneration preserves session data; use [Session.Destroy] to log
// out and discard it. Before the final response headers, [Middleware] persists
// these changes and retires the old ID with a tombstone, not physical deletion.
// Persistence is skipped for 5xx responses. Observe [Builder.ErrorHandler] or
// [PersistFailed]: a failed retirement write can leave the old ID valid.
// Loaded-session saves atomically match their original record before writing
// either the same ID or a regenerated ID. A missing, changed, or retired guard
// discards the save and cookie, even if the coordination lease expired. Existing
// keys, JSON, and bounded tombstone retention are unchanged; no retirement ledger
// is required. Drain old, unguarded writers during upgrades. Retirement does not
// revoke different IDs committed earlier or undo application handler effects.
// The absolute maximum caps initial and renewed expiry and is checked before
// persistence. Exact expiry is expired, never a zero-TTL persistent record.
// Detached best-effort lease cleanup retains trace values but has its own
// ten-second deadline; backend context cooperation remains necessary.
// Negative idle TTL or PersistenceTimeout is rejected during middleware
// construction after callbacks. Zero still restores defaults; negative maximum
// lifetime and expiration-delta settings retain their disabled meanings.
//
// # Cookie prefixes
//
// Browsers enforce the [HostPrefix] and [SecurePrefix] name prefixes. When no
// name is set, [Middleware] uses "__Host-arkoren.session" if the cookie is
// Secure, has Path=/ and no Domain, and "arkoren.session" otherwise (insecure
// development, a domain, or another path). An explicit name is never altered;
// put the prefix in it yourself. [Middleware] panics at construction when the
// attributes contradict a prefix in the name.
//
// [HostPrefix] means sibling subdomains cannot overwrite the cookie. To pick the
// name yourself:
//
//	session.Middleware(store, func(cookies *session.Builder) {
//		cookies.Name(session.HostPrefix + "arkoren.session")
//	})
//
// Upgrading from a version whose default was "arkoren.session" logs existing
// sessions out once, because browsers keep sending the old cookie name.
//
// To share sessions across subdomains, use [SecurePrefix] with a domain:
//
//	session.Middleware(store,
//		func(cookies *session.Builder) {
//			cookies.Name(session.SecurePrefix + "arkoren.session")
//		},
//
//		func(sessionPolicy *session.Builder) {
//			sessionPolicy.Domain("example.com")
//		})
//
// Prefixed cookies need HTTPS; the default name drops the prefix when
// Builder.AllowInsecure is set, so local development needs no special casing:
//
//	session.Middleware(store, func(sessionPolicy *session.Builder) {
//		sessionPolicy.AllowInsecure(app.Environment() == application.Development)
//	})
//
// # Events
//
// With [Builder.Events], [Middleware] publishes [Expired] ("session.expired")
// when it replaces an expired session, with its creation and expiry times
// and never its ID or data. It publishes [Destroyed] ("session.destroyed")
// with the request method and path once a session ended with
// [Session.Destroy] has been retired in the store. It publishes [PersistFailed]
// ("session.persist.failed") when saving, verifying, or retiring a session
// record fails, with the failed operation, request method, and path, and
// never the error text; [Builder.ErrorHandler] still receives the error.
// Destroyed and PersistFailed carry registered route patterns, never expanded
// URL paths; their Path is empty when no request pattern is available. Route
// literals are application-owned and must not contain secrets.
package session
