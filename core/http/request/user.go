package request

import (
	"net/http"

	"arkoren.dev/core/auth"
)

// Identity returns the [auth.Identity] stored on the request by
// [auth.Middleware]. It returns [auth.ErrUnauthenticated] when the request is
// anonymous. It needs no knowledge of the application's user type.
//
// Example:
//
//	identity, err := request.Identity(r)
//	if err != nil {
//		return err
//	}
//	_ = identity.Subject()
func Identity(r *http.Request) (auth.Identity, error) {
	identity, ok := auth.FromContext(r.Context())

	if !ok {
		return identity, auth.ErrUnauthenticated
	}

	return identity, nil
}

// User returns the application user of type U that the authenticator attached
// to the request's identity, see [auth.UserFromContext]. It returns
// [auth.ErrUnauthenticated] when the request is anonymous, carries no user
// value, or carries one of another type, so a mismatch fails closed.
//
// Example:
//
//	user, err := request.User[User](r)
//	if err != nil {
//		return err
//	}
//	_ = user.ID
func User[U any](r *http.Request) (U, error) {
	user, ok := auth.UserFromContext[U](r.Context())

	if !ok {
		return user, auth.ErrUnauthenticated
	}

	return user, nil
}
