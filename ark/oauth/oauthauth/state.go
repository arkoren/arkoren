package oauthauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/response"
)

const (
	// handoffTTL is how long the verified result of a form_post callback waits
	// for the same-site request that finishes the login. It only has to cover one
	// redirect, so it is much shorter than a login.
	handoffTTL = 2 * time.Minute

	// loginTTL is how long a started login stays valid.
	loginTTL = 10 * time.Minute

	// maxLogins caps the login cookies a browser holds at once.
	maxLogins = 5
)

// loginState is the sealed content of a login cookie. It binds one callback
// to the browser that started the login.
type loginState struct {
	// Cookie is the cookie name, checked on open so a value cannot be moved
	// to another cookie.
	Cookie string `json:"cookie"`

	// State is the value echoed by the provider in the callback.
	State string `json:"state"`

	// Nonce binds the ID token to this login. Empty for OAuth2 providers.
	Nonce string `json:"nonce"`

	// Verifier is the PKCE verifier. Empty when PKCE is off.
	Verifier string `json:"verifier"`

	// ReturnTo is the local path to redirect to after login.
	ReturnTo string `json:"return_to"`

	// Expires is when the login stops being accepted.
	Expires time.Time `json:"expires"`

	// Identity is set only in a hand-off cookie (see [Client.handOff]): the
	// identity the form_post callback already verified. Nil in a login cookie.
	Identity *auth.External `json:"identity,omitempty"`
}

// cookieName returns the cookie name of the login with the given state.
func (client *Client) cookieName(state string) string {
	digest := sha256.Sum256([]byte(state))

	return client.prefix + base64.RawURLEncoding.EncodeToString(digest[:])[:12]
}

// cookie returns a login cookie with the security attributes of the client.
// A negative maxAge deletes it.
func (client *Client) cookie(name, value string, maxAge int) *http.Cookie {
	sameSite := http.SameSiteLaxMode

	if client.provider.formPost {
		sameSite = http.SameSiteNoneMode
	}

	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   !client.config.AllowInsecure,
		SameSite: sameSite,
	}
}

// save seals state and sets it as a login cookie.
func (client *Client) save(writer http.ResponseWriter, state loginState) error {
	sealed, err := client.cipher.Encrypt(state)

	if err != nil {
		return err
	}

	value := base64.RawURLEncoding.EncodeToString(sealed)
	http.SetCookie(writer, client.cookie(state.Cookie, value, int(loginTTL.Seconds())))

	return nil
}

// open decrypts a login cookie and checks that it belongs to its name.
func (client *Client) open(cookie *http.Cookie) (loginState, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(cookie.Value)

	if err != nil {
		return loginState{}, err
	}

	state, err := client.cipher.Decrypt[loginState](sealed)

	if err != nil {
		return loginState{}, err
	}

	if state.Cookie != cookie.Name {
		return loginState{}, errors.New("login cookie name mismatch")
	}

	return state, nil
}

// drop deletes the named login cookie.
func (client *Client) drop(writer http.ResponseWriter, name string) {
	http.SetCookie(writer, client.cookie(name, "", -1))
}

// take returns the login started with state and sends a cookie deletion to the
// browser. It fails for missing, tampered, expired or state-mismatched cookies.
// Deletion does not invalidate a copied cookie: there is no used-state registry.
// The provider's one-time authorization code is the server-side replay boundary.
func (client *Client) take(writer http.ResponseWriter, request *http.Request, state string) (loginState, error) {
	return client.takeCookie(writer, request, client.cookieName(state), state)
}

// handoffName returns the name of the hand-off cookie of the login with the
// given state. It is derived from the state, so only a request that knows the
// state can ask for it.
func (client *Client) handoffName(state string) string {
	return client.cookieName(state) + "h"
}

// handOff ends the cross-site POST of a form_post callback. The browser sends
// that POST without its SameSite=Lax cookies, so the session, and with it a
// logged-in user who connects a provider, is invisible to it. handOff therefore
// only keeps the identity the POST verified, sealed in a short-lived hand-off
// cookie named after the state, and answers 303 See Other to the callback path
// with the state in the query. The browser follows it with a top-level GET,
// which carries the Lax session cookie, and [Client.CallbackHandler] finishes the
// login there with [Client.takeHandoff]. saved is the login the POST consumed;
// identity is what it verified. The cookie is Lax, not None: only that GET needs
// it, and a cross-site request has no use for it.
func (client *Client) handOff(writer http.ResponseWriter, saved loginState, identity auth.External) error {
	callback, err := url.Parse(client.config.RedirectURL)

	if err != nil {
		return err
	}

	name := client.handoffName(saved.State)

	sealed, err := client.cipher.Encrypt(loginState{
		Cookie:   name,
		State:    saved.State,
		ReturnTo: saved.ReturnTo,
		Expires:  time.Now().Add(handoffTTL),
		Identity: &identity,
	})

	if err != nil {
		return err
	}

	cookie := client.cookie(name, base64.RawURLEncoding.EncodeToString(sealed), int(handoffTTL.Seconds()))
	cookie.SameSite = http.SameSiteLaxMode
	http.SetCookie(writer, cookie)

	return response.SafeRedirect(writer, http.StatusSeeOther, callback.EscapedPath()+"?"+url.Values{"state": {saved.State}}.Encode())
}

// takeHandoff returns the login and verified identity that [Client.handOff]
// stored for state and sends a cookie deletion to the browser. It fails when the
// cookie is missing, tampered, expired, moved, for another state, or holds no
// identity. Like [Client.take] it cannot invalidate a copied cookie; the
// cookie is encrypted, HttpOnly and lives for [handoffTTL].
func (client *Client) takeHandoff(writer http.ResponseWriter, request *http.Request, state string) (loginState, error) {
	saved, err := client.takeCookie(writer, request, client.handoffName(state), state)

	if err != nil {
		return loginState{}, err
	}

	if saved.Identity == nil {
		return loginState{}, errors.New("hand-off cookie without identity")
	}

	return saved, nil
}

// takeCookie is the shared body of [Client.take] and [Client.takeHandoff]: it
// opens the sealed cookie called name, deletes it from the browser and checks it
// belongs to state.
func (client *Client) takeCookie(writer http.ResponseWriter, request *http.Request, name, state string) (loginState, error) {
	cookie, err := request.Cookie(name)

	if err != nil {
		return loginState{}, err
	}

	client.drop(writer, name)

	saved, err := client.open(cookie)

	if err != nil {
		return loginState{}, err
	}

	if !saved.Expires.After(time.Now()) || subtle.ConstantTimeCompare([]byte(saved.State), []byte(state)) != 1 {
		return loginState{}, errors.New("login expired or state mismatch")
	}

	return saved, nil
}

// prune deletes unreadable and expired login cookies, and the oldest ones
// beyond the cap, leaving room for one new login.
func (client *Client) prune(writer http.ResponseWriter, request *http.Request) {
	var live []loginState

	for _, cookie := range request.Cookies() {
		if !strings.HasPrefix(cookie.Name, client.prefix) {
			continue
		}

		state, err := client.open(cookie)

		if err != nil || !state.Expires.After(time.Now()) {
			client.drop(writer, cookie.Name)

			continue
		}

		live = append(live, state)
	}

	slices.SortFunc(live, func(a, b loginState) int {
		return a.Expires.Compare(b.Expires)
	})

	for _, old := range live[:max(0, len(live)-maxLogins+1)] {
		client.drop(writer, old.Cookie)
	}
}
