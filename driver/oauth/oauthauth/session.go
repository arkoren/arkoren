package oauthauth

import (
	"context"
	"errors"
	"net/http"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
)

// loginSession resolves exactly the identity's selected bridge once, mapping
// only missing sessions to the browser handlers' existing unauthorized error.
// Invalid definitions remain ordinary configuration errors.
func loginSession[K, U any](ctx context.Context, identity auth.SessionIdentity[K, U]) (*session.Session, error) {
	current, err := identity.Session(ctx)

	if errors.Is(err, session.ErrNoSession) {
		return nil, auth.ErrUnauthenticated
	}

	return current, err
}

// Logout ends the local login in exactly identity's selected session and
// redirects to "/"; it does not log out at the provider. Invalid definitions
// fail before lookup or mutation, and absent sessions return
// auth.ErrUnauthenticated without falling back to default.
//
// Example:
//
//	r.Post("/logout", client.Logout(identity))
func (client *Client) Logout[K, U any](identity auth.SessionIdentity[K, U]) router.Handler {
	return func(writer http.ResponseWriter, httpRequest *http.Request) error {
		current, err := loginSession(httpRequest.Context(), identity)

		if err != nil {
			return err
		}

		if err := identity.Logout(current); err != nil {
			return err
		}

		client.config.Events.Signal(httpRequest.Context(), LoggedOut)

		return response.SafeRedirect(writer, http.StatusFound, "/")
	}
}
