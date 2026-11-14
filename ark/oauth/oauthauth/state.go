package oauthauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

const (
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
	name := client.cookieName(state)

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
