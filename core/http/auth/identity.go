package auth

import (
	"context"
	"errors"

	"arkoren.dev/core/http/session"
)

// sessionKey is the session key holding the user's identifier.
const sessionKey = "arkoren:auth:user"

// ErrInvalidSessionIdentity reports a zero definition or nil loader. It is a
// configuration error, not a credential rejection, so middleware fails closed.
//
// Example:
//
//	if err := identity.Err(); errors.Is(err, auth.ErrInvalidSessionIdentity) {
//		return err
//	}
var ErrInvalidSessionIdentity = errors.New("auth: invalid session identity")

// Identifiable is implemented by user types that report their own login
// identifier. [SessionIdentity] needs both directions: the loader maps K to the
// user and AuthID maps the user back to K, so [SessionIdentity.Login] can take
// the user and [SessionIdentity.Subject] can derive a stable reference for
// packages that do not know the application's types. The compiler ties the K
// returned here to the K the loader accepts.
//
// AuthID must return the same value the loader accepts, must not change for the
// life of the user, and should be implemented on the type used as U (a pointer
// receiver requires a pointer U). It is called on a possibly zero user, so it
// must not depend on loaded state.
//
// Example:
//
//	type User struct{ ID int64 }
//
//	func (user User) AuthID() int64 { return user.ID }
type Identifiable[K any] interface {
	AuthID() K
}

// SessionIdentity binds the login identifier K, user loader U and session bridge
// in one immutable definition. U reports its identifier through [Identifiable].
// Zero is invalid. Copies share functions and their
// captures, not mutable construction drafts. All definitions use the existing
// arkoren:auth:user slot: a physical session supports one identity, and another
// definition using the same K can still mix realms. Compatible persisted JSON
// may decode across types; incompatible in-memory types fail closed.
//
// Example:
//
//	identity := auth.NewSessionIdentity(loadUser)
//	r.Use(auth.Middleware(identity.Authenticator()))
type SessionIdentity[K any, U Identifiable[K]] struct {
	// load resolves a correctly typed stored identifier; nil marks invalidity.
	load func(context.Context, K) (U, error)

	// handle selects exactly one context bridge, without default fallback.
	handle session.Handle
}

// NewSessionIdentity publishes an immutable identity definition without calling
// load. A nil loader is invalid, but construction callbacks still run once in
// order. Nil callback entries panic. Retained drafts cannot change the result;
// function captures remain caller-owned. Check Err during application setup.
// Both type arguments are inferred from load when U implements [Identifiable]
// for K. Err checks only the loader: an unsupported K (see
// [SessionIdentity.Subject]) still works for sessions and fails only where a
// subject is needed.
//
// Example:
//
//	identity := auth.NewSessionIdentity(loadUser)
//	if err := identity.Err(); err != nil {
//		return err
//	}
//	r.Use(auth.Middleware(identity.Authenticator()))
func NewSessionIdentity[K any, U Identifiable[K]](
	load func(ctx context.Context, id K) (U, error),
	configure ...SessionBuilderFunc,
) SessionIdentity[K, U] {
	authenticator := &SessionBuilder{}

	for _, configure := range configure {
		configure(authenticator)
	}

	return SessionIdentity[K, U]{
		load:   load,
		handle: authenticator.handle,
	}
}

// Err validates the definition without looking up a session or invoking its
// loader. Zero and nil-loader definitions return ErrInvalidSessionIdentity.
//
// Example:
//
//	if err := identity.Err(); err != nil {
//		return err
//	}
func (identity SessionIdentity[K, U]) Err() error {
	if identity.load == nil {
		return ErrInvalidSessionIdentity
	}

	return nil
}

// Session resolves exactly the selected request bridge once. It returns
// ErrInvalidSessionIdentity before lookup for invalid definitions and
// session.ErrNoSession for absent or nil sessions. It does not load a user,
// inspect an identifier, mutate state or fall back to the default session.
//
// Example:
//
//	current, err := identity.Session(r.Context())
//	if err != nil {
//		return err
//	}
//	_ = current
func (identity SessionIdentity[K, U]) Session(ctx context.Context) (*session.Session, error) {
	if err := identity.Err(); err != nil {
		return nil, err
	}

	current, ok := session.FromContextFor(ctx, identity.handle)

	if !ok {
		return nil, session.ErrNoSession
	}

	return current, nil
}
