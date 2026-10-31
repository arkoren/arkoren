// Package identity deliberately contains a bad caller to test static binding.
package identity

import (
	"context"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/session"
)

// wrongLogin attempts to store a string with an integer identity definition.
// Compilation must fail at the Login argument, without running this function.
func wrongLogin(current *session.Session) error {
	identity := auth.NewSessionIdentity(func(context.Context, int) (string, error) {
		return "user", nil
	})

	return identity.Login(current, "wrong")
}
