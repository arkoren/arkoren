package apitoken

import (
	"net/http"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/access/internal/web"
)

// ExchangeWindow is how recent the login must be for [Issuer.ExchangeHandler]
// to trade it for a device token: five minutes. A silent re-login of the same
// user does not extend it.
//
// Example:
//
//	if err := guard.RecentLogin(r, apitoken.ExchangeWindow); err != nil {
//		return err
//	}
const ExchangeWindow = 5 * time.Minute

// secretBody is the response of the handlers that issue a token: the token and
// its secret text, the only time the latter is shown.
type secretBody struct {
	Token  Token  `json:"token"`
	Secret string `json:"secret"`
}

// ListHandler returns the handler of GET /tokens: 200 with the user's tokens,
// newest first, as a JSON array. It needs a browser session: a token cannot
// list tokens.
//
// Example:
//
//	web.Get("/tokens", issuer.ListHandler())
func (issuer *Issuer) ListHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Session(r)
		if err != nil {
			return err
		}
		tokens, err := issuer.Tokens(r.Context(), identity.Subject())
		if err != nil {
			return err
		}

		return response.JSON(w, http.StatusOK, tokens)
	}
}

// CreateHandler returns the handler of POST /tokens. The body is
// {"name": "...", "scopes": [...], "expires_at": "2026-12-31T00:00:00Z"} (the
// expiry is optional). It answers 201 with {"token": {...}, "secret": "ark_..."};
// the secret is shown once. It needs a browser session and a fresh login
// ([auth.Guard.Fresh]), so a hijacked old session cannot mint a long-lived
// credential. An invalid request is [ErrInvalidRequest] (422).
//
// Example:
//
//	web.Post("/tokens", issuer.CreateHandler(guard))
func (issuer *Issuer) CreateHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		body, err := web.Decode[Request](r)
		if err != nil {
			return err
		}
		token, secret, err := issuer.Issue(r.Context(), identity.Subject(), body)
		if err != nil {
			return err
		}

		return response.JSON(w, http.StatusCreated, secretBody{Token: token, Secret: secret})
	}
}

// DeleteHandler returns the handler of DELETE /tokens/{id}: 204 when the token
// of the logged-in user was revoked, [ErrNotFound] (404) when it does not exist
// or belongs to someone else. It needs a browser session.
//
// Example:
//
//	web.Delete("/tokens/{id}", issuer.DeleteHandler())
func (issuer *Issuer) DeleteHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Session(r)
		if err != nil {
			return err
		}
		affected, err := issuer.revoke(r.Context(), identity.Subject(), []string{r.PathValue("id")})
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotFound
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// ExchangeHandler returns the handler of POST /device-tokens. The body is
// {"device": "Pixel 9"}. It trades the browser-session login for a device token
// and ends that login: the device keeps the token and no cookie. It answers 201
// with {"token": {...}, "secret": "ark_..."}.
//
// It needs a browser session whose login is at most [ExchangeWindow] old
// ([auth.Guard.RecentLogin]); a silent re-login of the same user does not
// extend that, and a stale session answers [auth.ErrNoStepUpMethod] (403: log in
// again). A device therefore logs in through the ordinary routes (password, then
// the second factor if any) and exchanges right away, which reuses every login
// rule, throttle and attempt limit with no second password route.
//
// Example:
//
//	web.Post("/device-tokens", issuer.ExchangeHandler(guard))
func (issuer *Issuer) ExchangeHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Session(r)
		if err != nil {
			return err
		}
		if err := guard.RecentLogin(r, ExchangeWindow); err != nil {
			return err
		}
		body, err := web.Decode[struct {
			Device string `json:"device"`
		}](r)
		if err != nil {
			return err
		}
		token, secret, err := issuer.IssueDevice(r.Context(), identity.Subject(), body.Device)
		if err != nil {
			return err
		}
		if err := guard.Logout(r); err != nil {
			return err
		}

		return response.JSON(w, http.StatusCreated, secretBody{Token: token, Secret: secret})
	}
}

// DeleteCurrentHandler returns the handler of DELETE /device-tokens/current: it
// revokes the token the request presented and answers 204, which is how a
// device logs out. It accepts any token of this issuer, not a browser session
// (use the guard's logout for that). Mount it on a router whose middleware
// includes [Issuer.Authenticator].
//
// Example:
//
//	api.Delete("/device-tokens/current", issuer.DeleteCurrentHandler())
func (issuer *Issuer) DeleteCurrentHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := request.Identity(r)
		if err != nil {
			return err
		}
		id, ok := issuer.presentedID(r)
		if identity.Session() || !ok {
			return auth.ErrUnauthenticated
		}
		if err := issuer.Revoke(r.Context(), identity.Subject(), id); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}
