package request

import (
	"net/http"

	"arkoren.dev/core/http/session"
)

// SessionOption changes the context selection for one [Session] or [MustSession]
// call. Options run in order; a nil entry panics. An expanded nil slice supplies
// no options. Use [WithSessionHandle] rather than middleware construction
// settings when several stores share one request.
//
// Example:
//
//	current, ok := request.Session(r, request.WithSessionHandle(applicationSession))
//	if !ok {
//		return session.ErrNoSession
//	}
//	_ = current
type SessionOption func(*sessionConfig)

// WithSessionHandle selects the bridge used by session.Builder.Handle. Later
// options replace earlier selections; zero restores default lookup. Missing
// selected sessions never fall back to another handle or the default.
//
// Example:
//
//	current := request.MustSession(r, request.WithSessionHandle(applicationSession))
//	if err := current.Put(session.NewField[string]("theme"), "dark"); err != nil {
//		return err
//	}
func WithSessionHandle(handle session.Handle) SessionOption {
	return func(config *sessionConfig) {
		config.handle = handle
	}
}

// Session returns the session stored on the request by session.Middleware.
// The boolean reports whether a session was found. Options only select a
// handle; zero selects default. A stored nil pointer is absent. No session is
// created, no fallback is attempted, and the request is not mutated.
//
// Example:
//
//	current, ok := request.Session(r)
//	if !ok {
//		return session.ErrNoSession
//	}
//	if err := current.Put(session.NewField[string]("theme"), "dark"); err != nil {
//		return err
//	}
func Session(r *http.Request, opts ...SessionOption) (*session.Session, bool) {
	var config sessionConfig

	for _, option := range opts {
		option(&config)
	}

	return session.FromContextFor(r.Context(), config.handle)
}

// MustSession is like [Session] but panics with [session.ErrNoSession] when
// no session is found. It applies the per-call options once through Session.
//
// Example:
//
//	current := request.MustSession(r)
//	if err := current.Put(session.NewField[string]("theme"), "dark"); err != nil {
//		return err
//	}
func MustSession(r *http.Request, opts ...SessionOption) *session.Session {
	if value, ok := Session(r, opts...); ok {
		return value
	}

	panic(session.ErrNoSession)
}
