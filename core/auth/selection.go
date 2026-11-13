package auth

import "arkoren.dev/core/http/session"

// SessionBuilderFunc configures the session bridge captured by NewSessionIdentity.
// Policies run once in order on a shared draft, even for an invalid loader;
// a nil policy panics. This alias also accepts ordinary function values.
//
// Example:
//
//	policy := auth.SessionBuilderFunc(func(authenticator *auth.SessionBuilder) {
//		authenticator.Handle(applicationSession)
//	})
//	identity := auth.NewSessionIdentity(loadUser, policy)
//	if err := identity.Err(); err != nil {
//		return err
//	}
//	r.Use(auth.Middleware(identity.Authenticator()))
type SessionBuilderFunc = func(*SessionBuilder)

// SessionBuilder selects the request session for NewSessionIdentity. Methods
// return the same draft. Callbacks run once in order, even with a nil loader;
// nil callback entries panic. Publication copies the completed selection.
//
// Example:
//
//	identity := auth.NewSessionIdentity(loadUser, func(authenticator *auth.SessionBuilder) {
//		authenticator.Handle(applicationSession)
//	})
//	r.Use(auth.Middleware(identity.Authenticator()))
type SessionBuilder struct {
	// handle selects the session bridge; zero selects the default.
	handle session.Handle
}

// Handle selects the request bridge shared with session middleware. Zero resets
// the default. It changes neither cookie nor storage policy.
//
// Example:
//
//	identity := auth.NewSessionIdentity(loadUser, func(authenticator *auth.SessionBuilder) {
//		authenticator.Handle(applicationSession)
//	})
//	_ = identity.Err()
func (authenticator *SessionBuilder) Handle(handle session.Handle) *SessionBuilder {
	authenticator.handle = handle

	return authenticator
}
