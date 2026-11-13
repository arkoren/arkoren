package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"arkoren.dev/foundation/problem"
)

// Bearer returns an [Authenticator] for "Authorization: Bearer <token>"
// requests. Without an Authorization header it returns [ErrNoCredentials].
// A malformed header, or an error from verify, becomes [ErrUnauthenticated]
// with a `WWW-Authenticate: Bearer error="invalid_token"` challenge and the
// cause attached. If verify returns [ErrNoCredentials] it is passed on.
//
// Example:
//
//	r.Use(auth.Middleware(auth.Bearer(func(ctx context.Context, token string) (auth.Identity, error) {
//		key, err := keys.Lookup(ctx, token)
//		if err != nil {
//			return auth.Identity{}, err
//		}
//
//		return auth.NewIdentity(key.OwnerSubject), nil
//	})))
func Bearer(verify func(ctx context.Context, token string) (Identity, error)) Authenticator {
	invalid := ErrUnauthenticated.Derive(func(details *problem.Builder) {
		details.Header("WWW-Authenticate", `Bearer error="invalid_token"`)
	})

	return func(r *http.Request) (identity Identity, err error) {
		values := r.Header.Values("Authorization")

		if len(values) == 0 {
			return identity, ErrNoCredentials
		}

		scheme, token, found := strings.Cut(values[0], " ")

		if len(values) != 1 || !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.Contains(token, " ") {
			return identity, invalid
		}

		identity, err = verify(r.Context(), token)

		if errors.Is(err, ErrNoCredentials) {
			return identity, err
		}

		if err != nil {
			return identity, invalid.Derive(func(details *problem.Builder) {
				details.Error(err)
			})
		}

		return identity, nil
	}
}
