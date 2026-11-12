// Package identity deliberately contains a bad caller to test static binding.
package identity

import (
	"context"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/session"
)

// account is a user identified by an integer.
type account struct{}

// AuthID reports the integer identifier the loader below accepts.
func (account) AuthID() int {
	return 1
}

// wrongLogin attempts to log in a string instead of the identity's user type.
// Compilation must fail at the Login argument, without running this function.
func wrongLogin(current *session.Session) error {
	identity := auth.NewSessionIdentity(func(context.Context, int) (account, error) {
		return account{}, nil
	})

	return identity.Login(current, "wrong")
}
