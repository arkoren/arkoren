package auth

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/http/session"
)

// sessionKey is the session key holding the user's identifier.
const sessionKey = "arkoren:auth:user"

// ErrInvalidSessionIdentity reports a zero definition, a nil loader or an
// identifier type K that cannot be converted to a subject (see
// [ErrInvalidSubject]; the latter error is wrapped too). It is a
// configuration error, not a credential rejection, so middleware fails closed.
//
// Example:
//
//	if err := identity.Err(); errors.Is(err, auth.ErrInvalidSessionIdentity) {
//		return err
//	}
var ErrInvalidSessionIdentity = errors.New("auth: invalid session identity")

// SessionIdentity binds the login identifier K, user loader U and session bridge
// in one immutable definition. It is a temporary bridge to the non-generic
// [Identity]: [SessionIdentity.Authenticator] builds an identity whose subject
// is K converted by the [NewUserDriver] rules and whose record holds the loaded
// U, so K must be a string, an integer or a text type. Zero is invalid. Copies share functions and their
// captures, not mutable construction drafts. All definitions use the existing
// arkoren:auth:user slot: a physical session supports one identity, and another
// definition using the same K can still mix realms. Compatible persisted JSON
// may decode across types; incompatible in-memory types fail closed.
//
// Example:
//
//	identity := auth.NewSessionIdentity(loadUser)
//	r.Use(auth.Middleware(identity.Authenticator()))
type SessionIdentity[K, U any] struct {
	// load resolves a correctly typed stored identifier; nil marks invalidity.
	load func(context.Context, K) (U, error)

	// handle selects exactly one context bridge, without default fallback.
	handle session.Handle

	// err is the first construction error, such as an unsupported K.
	err error
}

// NewSessionIdentity publishes an immutable identity definition without calling
// load. A nil loader is invalid, but construction callbacks still run once in
// order. Nil callback entries panic. Retained drafts cannot change the result;
// function captures remain caller-owned. Check Err during application setup.
//
// Example:
//
//	identity := auth.NewSessionIdentity(loadUser)
//	if err := identity.Err(); err != nil {
//		return err
//	}
//	r.Use(auth.Middleware(identity.Authenticator()))
func NewSessionIdentity[K, U any](
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
		err:    checkSubjectType[K](),
	}
}

// Err validates the definition without looking up a session or invoking its
// loader. Zero and nil-loader definitions return ErrInvalidSessionIdentity, and
// so does an identifier type K that cannot be a subject, with the cause
// ([ErrInvalidSubject]) wrapped.
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

	if identity.err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSessionIdentity, identity.err)
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
