// Package identity deliberately contains a bad mapper to test static binding.
package identity

import (
	"context"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/driver/oauth/oauthauth"
)

// wrongMapper attempts to map a string identifier into an integer definition.
// Compilation must fail without constructing or starting an OAuth client.
func wrongMapper(client *oauthauth.Client) router.Handler {
	identity := auth.NewSessionIdentity(func(context.Context, int) (string, error) {
		return "user", nil
	})

	return client.Callback(identity, func(context.Context, oauthauth.Identity) (string, error) {
		return "wrong", nil
	})
}
