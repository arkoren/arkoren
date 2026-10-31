package session

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"sync"
	"time"

	"arkoren.dev/foundation/problem"
)

// ErrNoSession is the problem returned when attempting to
// retrieve a session that does not exist in the request context.
//
// Example:
//
//	failure := session.ErrNoSession.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrNoSession = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/session:no-session")
	details.Title("Session not found")
	details.Detail("Unable to find the session in the request")
	details.Status(500)
})

// ErrKeyNotFound indicates that a session has no value for a key.
// An existing nil interface is a value, not a missing field.
//
// Example:
//
//	current := session.New(time.Now().Add(time.Hour), nil)
//	_, err := current.Get(session.NewField[string]("optional"))
//	_ = errors.Is(err, session.ErrKeyNotFound)
var ErrKeyNotFound = errors.New("session key not found")

// ErrInvalidValueType indicates that a session value cannot be decoded
// into its requested type.
// It does not expose the field name, value, or decoder diagnostic text.
//
// Example:
//
//	current := session.New(time.Now().Add(time.Hour), map[string]any{"count": "three"})
//	_, err := current.Get(session.NewField[int]("count"))
//	_ = errors.Is(err, session.ErrInvalidValueType)
var ErrInvalidValueType = errors.New("session invalid value type")

// Session represents a user session with data storage and lifecycle
// management capabilities. It provides methods to store, retrieve,
// and manage session data, as well as control session expiration
// and regeneration for security purposes. Access is protected by
// a mutex to ensure thread-safe operations.
// Construct with New or obtain one from middleware, rather than relying on a
// zero Session to supply an ID or writable map. The mutex does not recursively
// protect application-owned values retained in fields.
//
// Example:
//
//	current := session.New(time.Now().Add(time.Hour), nil)
//	field := session.NewField[int]("cart")
//	if err := current.Put(field, 42); err != nil {
//		return err
//	}
//	value, err := current.Get(field)
//	if err != nil {
//		return err
//	}
//	_ = value
type Session struct {
	// originalID is the session ID assigned when the session was
	// first created. This value remains constant even if the
	// session is regenerated.
	originalID string

	// id is the current session identifier. This may differ from
	// originalID if the session has been regenerated.
	id string

	// createdAt records the absolute time the session was first created.
	createdAt time.Time

	// expiresAt is the time at which the session will expire.
	expiresAt time.Time

	// storage holds the session data as key-value pairs.
	storage map[string]any

	// mutex protects concurrent access to the session fields.
	mutex sync.Mutex

	// changed tracks whether the session data has been modified.
	changed bool

	// destroyed marks a session whose record and cookie must be removed.
	destroyed bool

	// loaded marks a session read from the store rather than created by this
	// request. Its record can have been retired by another request since,
	// so [Middleware] re-checks it before saving. Set once by newFrom.
	loaded bool
}

// sessionIDLength is the number of random bytes used to generate
// a session ID. 32 bytes provides 256 bits of entropy.
const sessionIDLength = 32

// generateSessionID generates a cryptographically random session
// ID using crypto/rand and base64url encoding (43 characters).
func generateSessionID() string {
	value := make([]byte, sessionIDLength)

	// crypto/rand.Read always fills value or terminates the process when the
	// operating system cannot provide cryptographically secure randomness.
	_, _ = rand.Read(value)

	return base64.RawURLEncoding.EncodeToString(value)
}

// newFrom reconstructs a session loaded from storage. Unlike [New], it
// keeps id and does not mark the session as changed.
func newFrom(id string, createdAt time.Time, expiresAt time.Time, storage map[string]any) *Session {
	return &Session{
		originalID: id,
		id:         id,
		createdAt:  createdAt,
		expiresAt:  expiresAt,
		storage:    storage,
		changed:    false,
		loaded:     true,
	}
}

// All returns a shallow copy of the outer session map for inspection or
// persistence. Unmodified persisted fields appear as jsontext.Value; runtime
// values retain their exact Go types. Values, including raw JSON slices, remain
// borrowed: do not mutate them concurrently with the session. Use Get for typed
// decoding of persisted fields. Copying this map into New does not retain private
// persisted provenance; it becomes ordinary runtime input.
//
// Example:
//
//	current := session.New(time.Now().Add(time.Hour), map[string]any{"theme": "dark"})
//	snapshot := current.All()
//	delete(snapshot, "theme") // The session's outer map is unchanged.
//	value, err := current.Get(session.NewField[string]("theme"))
//	if err != nil {
//		return err
//	}
//	_ = value // "dark"
func (session *Session) All() map[string]any {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	snapshot := make(map[string]any, len(session.storage))

	for name, value := range session.storage {
		if raw, ok := value.(persistedValue); ok {
			value = jsontext.Value(raw)
		}

		snapshot[name] = value
	}

	return snapshot
}

// ID returns the current session identifier. This may differ
// from the original session ID if the session has been regenerated.
// It reveals the bearer identifier; do not log it indiscriminately.
//
// Example: id := current.ID() // Use only where the session identifier is required.
func (session *Session) ID() string {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return session.id
}

// OriginalID returns the session identifier that was assigned
// when the session was initially created.
// Regenerate preserves it so middleware can retire the original record.
//
// Example: original := current.OriginalID()
func (session *Session) OriginalID() string {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return session.originalID
}

// Get retrieves a field's value, returning ErrInvalidField for invalid
// declarations and ErrKeyNotFound for missing members. In-memory values must
// already have type T, including jsontext.Value supplied through New or Put.
// Only fields actually decoded from a stored record permit typed JSON decoding.
// Replacing a loaded field with Put restores runtime type checking. Decode and
// type failures return ErrInvalidValueType without exposing names or values.
// An existing nil interface returns nil for interface fields, not ErrKeyNotFound;
// it cannot be read as a concrete type without the ordinary matching Go value.
//
// Example:
//
//	userID, err := current.Get(session.NewField[int]("user_id"))
//	if err != nil {
//		return err
//	}
//	_ = userID
func (session *Session) Get[T any](field Field[T]) (res T, err error) {
	if err := field.Err(); err != nil {
		return res, err
	}

	session.mutex.Lock()
	defer session.mutex.Unlock()

	raw, ok := session.storage[field.Name()]

	if !ok {
		return res, ErrKeyNotFound
	}

	encoded, ok := raw.(persistedValue)

	if !ok {
		return runtimeValue[T](raw)
	}

	if err := json.Unmarshal(jsontext.Value(encoded), &res); err == nil {
		return res, nil
	}

	return res, ErrInvalidValueType
}

// Put stores a value under the field's literal name and marks the session as
// changed. Invalid fields return ErrInvalidField without changing any state.
// Values are retained, not copied; persistence still uses ordinary JSON.
//
// WARNING: When storing authentication-related state, callers MUST
// call [Session.Regenerate] immediately after to prevent session
// fixation attacks.
//
// Example:
//
//	if err := current.Put(session.NewField[int]("user_id"), 42); err != nil {
//		return err
//	}
//	current.Regenerate()
func (session *Session) Put[T any](field Field[T], value T) error {
	if err := field.Err(); err != nil {
		return err
	}

	session.mutex.Lock()
	defer session.mutex.Unlock()

	session.storage[field.Name()] = value
	session.changed = true

	return nil
}

// Delete removes a field and marks the session as changed, even if absent.
// Invalid fields return ErrInvalidField without changing any state. The field
// type selects the declaration but deletion does not inspect the stored value.
//
// Example:
//
//	if err := current.Delete(session.NewField[string]("theme")); err != nil {
//		return err
//	}
func (session *Session) Delete[T any](field Field[T]) error {
	if err := field.Err(); err != nil {
		return err
	}

	session.mutex.Lock()
	defer session.mutex.Unlock()

	delete(session.storage, field.Name())

	session.changed = true

	return nil
}

// Extend requests a new expiration time and marks the session changed. It
// performs no persistence itself. Middleware caps this requested expiry at the
// original creation time plus its positive MaxLifetime before saving; callers
// cannot use Extend to renew the absolute lifetime or revive a retired ID.
//
// Example:
//
//	current.Extend(time.Now().Add(time.Hour))
//	_ = current.ExpiresAt() // Requested expiry, before middleware applies its cap.
func (session *Session) Extend(expiresAt time.Time) {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	session.expiresAt = expiresAt
	session.changed = true
}

// Regenerate generates a new cryptographically random session ID. The original
// ID and session data are preserved, and the session is marked as changed.
// This method performs no storage I/O. With [Middleware], change session state
// before the first final response headers. Unless the response is 5xx, the
// middleware saves the new record, issues its cookie, then replaces the old
// record with a tombstone so stale requests cannot revive the retired ID.
// The old record is not physically deleted. A failed save leaves the old
// session intact; a failed tombstone write can leave the old ID valid. Observe
// [Builder.ErrorHandler] or [PersistFailed] for persistence failures.
//
// WARNING: Call Regenerate after login or privilege changes to prevent session
// fixation. It preserves authentication data and does not log the user out.
// Use [Session.Destroy] for logout instead.
//
// Example:
//
//	session.Regenerate()
func (session *Session) Regenerate() {
	id := generateSessionID()

	session.mutex.Lock()
	defer session.mutex.Unlock()

	session.id = id
	session.changed = true
}

// Clear removes all data from the session while maintaining the
// session itself. This operation marks the session as changed.
// It neither rotates nor retires the ID; use Destroy to end the session.
//
// Example:
//
//	current.Clear()
//	_ = current.HasChanged() // true
func (session *Session) Clear() {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	clear(session.storage)

	session.changed = true
}

// Destroy marks the session as ended; later changes are discarded. It performs
// no storage I/O itself. Call it before the first final response headers to log
// out. Unless the response is 5xx, [Middleware] replaces the original ID's
// record with a tombstone and expires the cookie instead of saving the session.
// The tombstone prevents stale requests from reviving the retired ID; the
// record is not physically deleted. If retirement fails, the cookie still
// expires but the old ID can remain valid. Observe [Builder.ErrorHandler] or
// [PersistFailed] for that failure.
//
// Example:
//
//	current.Destroy() // Before the final response headers.
//	_ = current.Destroyed() // Storage retirement still belongs to middleware.
func (session *Session) Destroy() {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	session.destroyed = true
}

// Destroyed reports whether [Session.Destroy] was called.
// It is a local lifecycle flag, not confirmation of durable retirement.
//
// Example: ended := current.Destroyed()
func (session *Session) Destroyed() bool {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return session.destroyed
}

// CreatedAt returns the absolute time the session was first created.
// Regeneration and renewal preserve this anchor for maximum-lifetime checks.
//
// Example: createdAt := current.CreatedAt()
func (session *Session) CreatedAt() time.Time {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return session.createdAt
}

// ExpiresAt returns the time at which the session will expire.
// Middleware may cap an application-selected expiry before saving it.
//
// Example: expiresAt := current.ExpiresAt()
func (session *Session) ExpiresAt() time.Time {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return session.expiresAt
}

// HasExpired reports whether the expiration instant has been reached. Equality
// counts as expired, matching cache expiry and preventing a zero remaining TTL
// from being mistaken for a persistent record by middleware.
//
// Example:
//
//	current := session.New(time.Unix(0, 0), nil)
//	_ = current.HasExpired() // true
func (session *Session) HasExpired() bool {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return !time.Now().Before(session.expiresAt)
}

// ExpiresSoon reports whether expiry is strictly after now and strictly before
// now+delta. Equality at either boundary returns false; HasExpired handles the
// expiry boundary. Nonpositive deltas disable the warning, including the minimum
// time.Duration, without overflowing through negation.
//
// Example:
//
//	current := session.New(time.Now().Add(time.Hour), nil)
//	_ = current.ExpiresSoon(-time.Second) // false: warnings disabled.
func (session *Session) ExpiresSoon(delta time.Duration) bool {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	now := time.Now()

	return now.Before(session.expiresAt) && now.Add(delta).After(session.expiresAt)
}

// HasChanged returns true if the session data has been modified
// since it was loaded.
// This tracks field/lifecycle operations, not mutations through borrowed values.
//
// Example: changed := current.HasChanged()
func (session *Session) HasChanged() bool {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return session.changed
}

// HasRegenerated returns true if the session has been regenerated
// (i.e., the session ID has changed).
// Regeneration does not reset original creation or prove storage succeeded.
//
// Example: rotated := current.HasRegenerated()
func (session *Session) HasRegenerated() bool {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return session.id != session.originalID
}

// MarkAsUnchanged resets the change tracking flag, preventing
// the session from being persisted on the current request.
// Middleware renewal can mark it changed again. This does not undo any data
// mutation and should not be used to claim an unsaved change is durable.
//
// Example:
//
//	current.MarkAsUnchanged()
//	_ = current.HasChanged() // false until another mutation or renewal.
func (session *Session) MarkAsUnchanged() {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	session.changed = false
}
