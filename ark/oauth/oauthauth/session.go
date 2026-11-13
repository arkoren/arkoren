package oauthauth

import (
	"errors"
	"net/http"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
)

// Logout ends the local login in the request's session, including a pending
// second-factor login, publishes [LoggedOut] and redirects to "/". It does not
// log out at the provider. It calls login.Logout, which removes the login and
// its login record and regenerates the session ID; other session data stays.
//
// A request without a session returns auth.ErrUnauthenticated and publishes no
// event, without falling back to a default session. Other login.Logout errors
// are returned unchanged and publish no event. A nil login is a configuration
// error returned per request before any side effect. Logging out a request
// that has a session but no logged-in user still succeeds and redirects.
//
// Example:
//
//	r.Post("/logout", client.Logout(login))
func (client *Client) Logout(login *auth.Login) router.Handler {
	return func(writer http.ResponseWriter, httpRequest *http.Request) error {
		if login == nil {
			return errors.New("oauth: nil login")
		}

		err := login.Logout(httpRequest)

		if errors.Is(err, session.ErrNoSession) {
			return auth.ErrUnauthenticated
		}

		if err != nil {
			return err
		}

		client.config.Events.Signal(httpRequest.Context(), LoggedOut)

		return response.SafeRedirect(writer, http.StatusFound, "/")
	}
}
