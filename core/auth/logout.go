package auth

import (
	"errors"
	"net/http"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
)

// LogoutBuilder configures [Guard.LogoutHandler] before publication. Callbacks
// run directly once, synchronously in order on one fresh draft; nil entries
// panic and expanded nil or empty slices supply no edits. Methods return the
// same draft, and retaining it cannot change a published handler.
//
// Example:
//
//	r.Post("/logout", guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
//		logout.Redirect("/").Events(events)
//	}))
type LogoutBuilder struct {
	// settings holds the private construction values until publication.
	settings logoutSettings
}

// logoutSettings configures a [Guard.LogoutHandler] handler.
type logoutSettings struct {
	// Redirect is the target of the 303 answer. Empty answers 204.
	Redirect string

	// Events receives [LoggedOut]. The default, nil, publishes nothing.
	Events *event.Bus
}

// Redirect makes the handler answer 303 See Other with target as Location
// instead of 204 No Content, which suits a server-rendered application. The
// target must be a safe relative path (see [response.ValidateRedirectTarget]):
// [Guard.LogoutHandler] checks it once at construction, and an unsafe target makes the
// handler return [response.ErrUnsafeRedirect] on every request before any
// session change. The empty string, the default, answers 204. This setting
// replaces the previous value on the construction draft.
//
// Example:
//
//	r.Post("/logout", guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
//		logout.Redirect("/")
//	}))
func (logout *LogoutBuilder) Redirect(target string) *LogoutBuilder {
	logout.settings.Redirect = target

	return logout
}

// Events sets the bus that receives [LoggedOut]. Nil, the default, publishes
// nothing. The bus is a shared service, not copied. This setting replaces the
// previous value on the construction draft.
//
// Example:
//
//	r.Post("/logout", guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
//		logout.Events(events)
//	}))
func (logout *LogoutBuilder) Events(events *event.Bus) *LogoutBuilder {
	logout.settings.Events = events

	return logout
}

// LogoutBuilderFunc configures a [LogoutBuilder].
//
// Example:
//
//	var policy auth.LogoutBuilderFunc = func(logout *auth.LogoutBuilder) {
//		logout.Redirect("/")
//	}
//	r.Post("/logout", guard.LogoutHandler(policy))
type LogoutBuilderFunc = func(*LogoutBuilder)

// LoggedOutEvent reports that a logged-in user logged out. It carries only the
// subject, never the session ID, login record or other personal data.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.LoggedOut, func(ctx context.Context, out auth.LoggedOutEvent) error {
//		logger.InfoContext(ctx, "logged out", "subject", out.Subject)
//		return nil
//	})
type LoggedOutEvent struct {
	// Subject is the subject of the identity that logged out.
	Subject string
}

// LoggedOut ("auth.user.logged-out") is published by [Guard.LogoutHandler], built with
// [LogoutBuilder.Events], after the logout of a logged-in user succeeded. Logging
// out an anonymous session, or one whose login was already revoked, publishes
// nothing. It reports the end of one session login, not of the user's other
// devices (see [Guard.Revoke]). Emit logs listener errors instead of returning
// them, so a listener cannot change the response.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.LoggedOut, onLoggedOut)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var LoggedOut = event.NewTopic[LoggedOutEvent]("auth.user.logged-out")

// LogoutHandler returns the handler of the logout route, the single owner of
// that route. It reads the identity that [Middleware] stored, calls
// [Guard.Logout] (which ends the logged-in session, its login record and any
// pending login, and regenerates the session ID), publishes [LoggedOut] when
// the request had an identity, and answers 204 No Content, or 303 See Other to
// [LogoutBuilder.Redirect].
//
// Logging out a session without a logged-in user succeeds and publishes
// nothing. A request without a session returns [session.ErrNoSession] (500, a
// wiring mistake). A nil guard or an unsafe redirect target is a configuration
// error returned on every request, before any session change. Other
// [Guard.Logout] errors are returned unchanged and publish nothing. The
// callbacks run once, when LogoutHandler is called, so build the handler at
// startup rather than per request.
//
// Register it for POST only, behind CSRF protection: a GET logout lets any page
// log users out. Token clients log out on their own route, since a bearer token
// is a different credential.
//
// Example:
//
//	web.Post("/logout", guard.LogoutHandler())
//
//	web.Post("/sign-out", guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
//		logout.Redirect("/")
//	}))
func (guard *Guard) LogoutHandler(configure ...LogoutBuilderFunc) router.Handler {
	logout := &LogoutBuilder{}

	for _, configure := range configure {
		configure(logout)
	}

	settings := logout.settings

	var configErr error

	if guard == nil {
		configErr = errors.New("auth: nil guard")
	} else if settings.Redirect != "" {
		configErr = response.ValidateRedirectTarget(settings.Redirect)
	}

	return func(w http.ResponseWriter, r *http.Request) error {
		if configErr != nil {
			return configErr
		}

		identity, loggedIn := FromContext(r.Context())

		if err := guard.Logout(r); err != nil {
			return err
		}

		if loggedIn {
			settings.Events.Emit(r.Context(), LoggedOut, LoggedOutEvent{Subject: identity.Subject()})
		}

		if settings.Redirect == "" {
			return response.Status(w, http.StatusNoContent)
		}

		return response.Redirect(w, http.StatusSeeOther, settings.Redirect)
	}
}
