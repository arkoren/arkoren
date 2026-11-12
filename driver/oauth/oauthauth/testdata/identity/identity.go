// Package identity deliberately contains a bad mapper to test static binding.
package identity

import (
	"context"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/driver/oauth/oauthauth"
)

// account is a user identified by an integer.
type account struct{}

// AuthID reports the integer identifier the loader below accepts.
func (account) AuthID() int {
	return 1
}

// wrongMapper attempts to map to a string instead of the definition's user.
// Compilation must fail without constructing or starting an OAuth client.
func wrongMapper(client *oauthauth.Client) router.Handler {
	identity := auth.NewSessionIdentity(func(context.Context, int) (account, error) {
		return account{}, nil
	})

	return client.Callback(identity, func(context.Context, oauthauth.Identity) (string, error) {
		return "wrong", nil
	})
}
