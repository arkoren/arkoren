package auth

import (
	"errors"
	"net/http"

	"arkoren.dev/core/http/session"
)

// Authenticator loads U from the stored K. Missing sessions, absent identifiers
// and incompatible stored types yield ErrNoCredentials. Loader errors pass
// through unchanged. Invalid definitions instead return the configuration error
// before lookup, preventing middleware credential fallback.
//
// Example:
//
//	r.Use(auth.Middleware(identity.Authenticator()))
func (identity SessionIdentity[K, U]) Authenticator() Authenticator[U] {
	return func(r *http.Request) (user U, err error) {
		current, err := identity.Session(r.Context())

		if errors.Is(err, session.ErrNoSession) {
			return user, ErrNoCredentials
		}

		if err != nil {
			return user, err
		}

		id, err := current.Get(session.NewField[K](sessionKey))

		if errors.Is(err, session.ErrKeyNotFound) || errors.Is(err, session.ErrInvalidValueType) {
			return user, ErrNoCredentials
		}

		if err != nil {
			return user, err
		}

		return identity.load(r.Context(), id)
	}
}
