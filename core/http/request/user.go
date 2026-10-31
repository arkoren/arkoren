package request

import (
	"net/http"

	"arkoren.dev/core/http/auth"
)

// User returns the user of type U stored on the request by auth.Middleware.
// It returns [auth.ErrUnauthenticated] when there is none.
//
// Example:
//
//	user, err := request.User[User](r)
//	if err != nil {
//		return err
//	}
//	_ = user.ID
func User[U any](r *http.Request) (U, error) {
	user, ok := auth.FromContext[U](r.Context())

	if !ok {
		return user, auth.ErrUnauthenticated
	}

	return user, nil
}
