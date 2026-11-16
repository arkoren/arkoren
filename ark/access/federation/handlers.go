package federation

import (
	"net/http"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/access/internal/web"
)

// ListHandler returns the handler of GET /connections: 200 with the logged-in
// user's connections as a JSON array of {"id", "issuer", "created_at",
// "last_used_at"}. It needs a browser session.
//
// Example:
//
//	web.Get("/connections", links.ListHandler())
func (links *Links) ListHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Session(r)
		if err != nil {
			return err
		}
		listed, err := links.List(r.Context(), identity.Subject())
		if err != nil {
			return err
		}

		return response.JSON(w, http.StatusOK, listed)
	}
}

// DeleteHandler returns the handler of DELETE /connections/{id}: 204 when the
// connection was removed. It needs a browser session and a fresh login
// ([auth.Guard.Fresh]). Another user's connection is [ErrNotFound] (404), and
// the user's last way to log in is [ErrLastLink] (409).
//
// Example:
//
//	web.Delete("/connections/{id}", links.DeleteHandler(guard))
func (links *Links) DeleteHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		if err := links.Unlink(r.Context(), identity.Subject(), r.PathValue("id")); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// ConnectHandler returns the handler of POST /connections/{provider}: it records
// a connect intent in the logged-in user's session and answers 303 to the
// provider's login route, where the user signs in; the callback then links that
// identity to their account ([Links.Resolve]). targets maps each provider name to
// its login path ("google" to "/auth/google"); a name that is not in it is
// [ErrUnknownProvider] (404). The intent lasts [ConnectIntentTTL] and serves one
// callback.
//
// With [Builder.Verified] set, a user whose email is not verified gets 403.
//
// It needs a browser session and a fresh login, and it must be a POST: a
// cross-site GET to the login route alone can then never link an identity to a
// victim. Connecting a provider the user already has links nothing new.
//
// Example:
//
//	web.Post("/connections/{provider}", links.ConnectHandler(guard, map[string]string{
//		"google": "/auth/google",
//	}))
func (links *Links) ConnectHandler(guard *auth.Guard, targets map[string]string) router.Handler {
	fixed := make(map[string]string, len(targets))
	for name, target := range targets {
		if err := response.ValidateRedirectTarget(target); err != nil {
			panic("federation: connect target " + name + ": " + err.Error())
		}
		fixed[name] = target
	}

	return web.RequireJSON(func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		if err := web.Verified(r.Context(), links.verified, identity.Subject()); err != nil {
			return err
		}
		target, ok := fixed[r.PathValue("provider")]
		if !ok {
			return ErrUnknownProvider
		}
		current, ok := request.Session(r)
		if !ok {
			return auth.ErrUnauthenticated
		}
		err = current.Put(connectField, connectIntent{
			Subject:   identity.Subject(),
			ExpiresAt: time.Now().Add(ConnectIntentTTL).Unix(),
		})
		if err != nil {
			return err
		}

		return response.Redirect(w, http.StatusSeeOther, target)
	})
}
