package auth

import (
	"errors"
	"net/http"

	"arkoren.dev/core/http/session"
)

// Authenticator loads U from the stored K and returns an [Identity] whose
// subject is K formatted as a subject and whose [UserRecord] carries the
// subject and U in Value, so [UserFromContext] returns U. The identity has no
// factors. Missing sessions, absent identifiers and incompatible stored types
// yield ErrNoCredentials. Loader errors pass through unchanged. A stored K that
// does not format as a valid subject, such as an empty string, fails closed with
// [ErrInvalidSubject], never ErrNoCredentials. Invalid definitions instead return
// the configuration error before lookup, preventing middleware credential
// fallback.
//
// Example:
//
//	r.Use(auth.Middleware(identity.Authenticator()))
func (identity SessionIdentity[K, U]) Authenticator() Authenticator {
	return func(r *http.Request) (Identity, error) {
		current, err := identity.Session(r.Context())

		if errors.Is(err, session.ErrNoSession) {
			return Identity{}, ErrNoCredentials
		}

		if err != nil {
			return Identity{}, err
		}

		id, err := current.Get(session.NewField[K](sessionKey))

		if errors.Is(err, session.ErrKeyNotFound) || errors.Is(err, session.ErrInvalidValueType) {
			return Identity{}, ErrNoCredentials
		}

		if err != nil {
			return Identity{}, err
		}

		user, err := identity.load(r.Context(), id)

		if err != nil {
			return Identity{}, err
		}

		subject, err := formatSubject(id)

		if err != nil {
			return Identity{}, err
		}

		return NewIdentity(subject, func(principal *IdentityBuilder) {
			principal.Record(UserRecord{Subject: subject, Value: user})
		}), nil
	}
}
