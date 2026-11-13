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
// log out at the provider. It calls guard.Logout, which removes the login and
// its login record and regenerates the session ID; other session data stays.
//
// A request without a session returns auth.ErrUnauthenticated and publishes no
// event, without falling back to a default session. Other guard.Logout errors
// are returned unchanged and publish no event. A nil guard is a configuration
// error returned per request before any side effect. Logging out a request
// that has a session but no logged-in user still succeeds and redirects.
//
// Deprecated: use guard.LogoutHandler, which owns the logout route in core. It
// answers 204 or a 303 redirect set with [auth.LogoutBuilder.Redirect] and
// publishes [auth.LoggedOut] with the subject. Logout keeps its documented 401,
// 302 and [LoggedOut] behavior until it is removed in a later minor release.
//
// Example:
//
//	r.Post("/logout", guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
//		logout.Redirect("/")
//	}))
func (client *Client) Logout(guard *auth.Guard) router.Handler {
	return func(writer http.ResponseWriter, httpRequest *http.Request) error {
		if guard == nil {
			return errors.New("oauth: nil guard")
		}

		err := guard.Logout(httpRequest)

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
