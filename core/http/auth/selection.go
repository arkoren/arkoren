package auth

import (
	"time"

	"arkoren.dev/core/http/session"
)

// SessionBuilderFunc configures the session bridge, realm and challenge lifetime
// captured by NewSessionIdentity. Policies run once in order on a shared draft,
// even for an invalid loader; a nil policy panics. This alias also accepts
// ordinary function values.
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

// SessionBuilder selects the request session, realm and challenge lifetime for
// NewSessionIdentity. Methods
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

	// realm suffixes the session keys; empty selects the unsuffixed keys.
	realm string

	// challengeTTL is the requested pending-login lifetime; nonpositive selects
	// the default.
	challengeTTL time.Duration
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

// Realm names the slot this identity uses in the physical session by suffixing
// its session keys (arkoren:auth:user:admin). Empty, the default, keeps the
// unsuffixed keys, so existing sessions stay logged in. Names are literal and
// unvalidated. Use one realm per independent login, such as an admin area next
// to a customer area, so a login in one never signs in the other; their user
// types must differ because the request context is keyed by the user type.
// Changing a deployed realm logs out its existing sessions.
//
// Example:
//
//	admins := auth.NewSessionIdentity(loadAdmin, func(authenticator *auth.SessionBuilder) {
//		authenticator.Realm("admin")
//	})
//	_ = admins.Err()
func (authenticator *SessionBuilder) Realm(name string) *SessionBuilder {
	authenticator.realm = name

	return authenticator
}

// ChallengeTTL sets how long [SessionIdentity.Challenge] waits for the next
// factor before the pending login expires. Zero and negative values select the
// default, five minutes. The deadline is fixed when the challenge is created and
// is independent of session renewal.
//
// Example:
//
//	identity := auth.NewSessionIdentity(loadUser, func(authenticator *auth.SessionBuilder) {
//		authenticator.ChallengeTTL(10 * time.Minute)
//	})
//	_ = identity.Err()
func (authenticator *SessionBuilder) ChallengeTTL(ttl time.Duration) *SessionBuilder {
	authenticator.challengeTTL = ttl

	return authenticator
}
