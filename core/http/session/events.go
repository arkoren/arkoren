package session

import (
	"time"

	"arkoren.dev/core/event"
)

// ExpiredEvent is the payload of [Expired]. It carries no session ID or data.
// Its timestamps describe the rejected lifetime, not the visitor's identity.
//
// Example: payload := session.ExpiredEvent{CreatedAt: createdAt, ExpiresAt: expiresAt}
type ExpiredEvent struct {
	// CreatedAt is when the expired session was created.
	// It is the original anchor, including after earlier renewals.
	//
	// Example: payload := session.ExpiredEvent{CreatedAt: createdAt}
	CreatedAt time.Time

	// ExpiresAt is when the expired session was due to expire.
	// It is the stored or capped expiry, not the time the event is delivered.
	//
	// Example: payload := session.ExpiredEvent{ExpiresAt: expiresAt}
	ExpiresAt time.Time
}

// Expired is published when [Middleware] loads a session past its expiry or
// [Builder.MaxLifetime] and replaces it with a fresh one. It fires on the
// first request after the expiry, not at the expiry itself. Sessions the
// application ends with [Session.Destroy] publish [Destroyed] instead.
//
// Example:
//
//	stop, err := bus.Listen(session.Expired, func(_ context.Context, payload session.ExpiredEvent) error {
//		_ = payload.ExpiresAt
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer stop()
var Expired = event.NewTopic[ExpiredEvent]("session.expired")

// DestroyedEvent is the payload of [Destroyed]. It never carries the session
// ID or data, and it does not say who the session belonged to.
// Its path is registered route identity, not an expanded URL containing a
// visitor's path parameters. Do not put secrets in registered route literals.
//
// Example:
//
//	payload := session.DestroyedEvent{Method: "POST", Path: "/sessions/{id}/logout"}
//	_ = payload.Path
type DestroyedEvent struct {
	// Method is the request method.
	//
	// Example: payload := session.DestroyedEvent{Method: "POST"}
	Method string

	// Path is the matched route pattern with its method prefix removed.
	// Parameters are not expanded; no pattern means empty, never URL.Path.
	// A host prefix on a host-specific pattern is retained.
	//
	// Example: payload := session.DestroyedEvent{Path: "/sessions/{id}/logout"}
	Path string
}

// Destroyed is published when [Middleware] has retired a session the handler
// ended with [Session.Destroy]: after the old ID is tombstoned and before the
// cookie is expired on the response. A failed tombstone publishes
// [PersistFailed] instead, because the old ID may still be usable. Sessions
// that merely expire publish [Expired], and regeneration publishes nothing.
// The event is published with a context that is not canceled with the
// request, because persistence happens as the response header is written.
//
// Example:
//
//	stop, err := bus.Listen(session.Destroyed, func(_ context.Context, payload session.DestroyedEvent) error {
//		_ = payload.Path // Registered pattern, not expanded parameters.
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer stop()
var Destroyed = event.NewTopic[DestroyedEvent]("session.destroyed")

// Persistence operations reported by [PersistFailedEvent.Operation].
const (
	// PersistSave is writing the session record.
	// It includes the atomic guarded write for a loaded session.
	//
	// Example: failed := payload.Operation == session.PersistSave
	PersistSave = "save"

	// PersistVerify is checking that the session's original record is still
	// live before saving over it.
	//
	// Example: failed := payload.Operation == session.PersistVerify
	PersistVerify = "verify"

	// PersistRetire is writing the tombstone that retires a destroyed or
	// regenerated session's old ID.
	//
	// Example: failed := payload.Operation == session.PersistRetire
	PersistRetire = "retire"
)

// PersistFailedEvent is the payload of [PersistFailed]. It never carries the
// session ID or data, the cookie, or the storage error, whose text may name
// the backend or the session key.
// The route pattern omits expanded parameters and the query; applications must
// still avoid secrets in registered route literals.
//
// Example:
//
//	payload := session.PersistFailedEvent{Operation: session.PersistSave, Path: "/accounts/{id}/cart"}
//	_ = payload.Operation
type PersistFailedEvent struct {
	// Operation is the storage step that failed: [PersistSave],
	// [PersistVerify], or [PersistRetire].
	//
	// Example: writing := payload.Operation == session.PersistSave
	Operation string

	// Method is the request method.
	//
	// Example: payload := session.PersistFailedEvent{Method: "POST"}
	Method string

	// Path is the matched route pattern with its method prefix removed.
	// Parameters are not expanded; no pattern means empty, never URL.Path.
	// A host prefix on a host-specific pattern is retained.
	//
	// Example: payload := session.PersistFailedEvent{Path: "/accounts/{id}/cart"}
	Path string
}

// PersistFailed is published when [Middleware] cannot persist session
// changes after the handler ran, so the client keeps its previous session
// state. It fires in addition to the [Builder.ErrorHandler] callback, which
// still receives the error itself. The event is published with a context
// that is not canceled with the request, because persistence happens as the
// response header is written.
//
// Example:
//
//	stop, err := bus.Listen(session.PersistFailed, func(_ context.Context, payload session.PersistFailedEvent) error {
//		_ = payload.Operation // No raw storage error is attached.
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer stop()
var PersistFailed = event.NewTopic[PersistFailedEvent]("session.persist.failed")
