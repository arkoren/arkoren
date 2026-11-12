package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"arkoren.dev/core/http/session"
)

// Authenticator loads U from the stored K. Missing sessions, absent identifiers
// and incompatible stored types yield ErrNoCredentials. Loader errors pass
// through unchanged. Invalid definitions instead return the configuration error
// before lookup, preventing middleware credential fallback.
//
// It reads only the logged-in user's slot: a login waiting for a further factor
// (see [SessionIdentity.Challenge]) is anonymous. If the loaded user implements
// [Versioned], the version stored at login must match the user's current one,
// otherwise the result is ErrNoCredentials. Sessions without factors, created
// before they existed, stay logged in unless the user is [Versioned].
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

		id, err := current.Get(field[K](identity.realm, userKey))

		if errors.Is(err, session.ErrKeyNotFound) || errors.Is(err, session.ErrInvalidValueType) {
			return user, ErrNoCredentials
		}

		if err != nil {
			return user, err
		}

		user, err = identity.load(r.Context(), id)

		if err != nil {
			return user, err
		}

		credentials := version(user)

		if credentials == "" {
			return user, nil
		}

		saved, err := current.Get(field[storedState](identity.realm, stateKey))

		if errors.Is(err, session.ErrKeyNotFound) || errors.Is(err, session.ErrInvalidValueType) {
			return user, ErrNoCredentials
		}

		if err != nil {
			return user, err
		}

		if subtle.ConstantTimeCompare([]byte(saved.Version), []byte(credentials)) != 1 {
			return user, ErrNoCredentials
		}

		return user, nil
	}
}
