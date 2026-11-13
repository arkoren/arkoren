package oauthauth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
)

// Login starts an Authorization Code login. It redirects to the provider and
// remembers the optional local return_to query target in a sealed cookie.
// It needs no session.
//
// Example:
//
//	r.Get("/login", client.Login()) // supports ?return_to=/local/path
func (client *Client) Login() router.Handler {
	return func(writer http.ResponseWriter, httpRequest *http.Request) error {
		if client.config.RedirectURL == "" {
			return errors.New("OAuth redirect URL cannot be empty")
		}

		ready, err := client.started()

		if err != nil {
			return err
		}

		returnTo := httpRequest.URL.Query().Get("return_to")

		if returnTo == "" {
			returnTo = "/"
		}

		if err := response.ValidateRedirectTarget(returnTo); err != nil {
			return invalidLoginRequest(err)
		}

		state, err := client.newState(returnTo)

		if err != nil {
			return err
		}

		client.prune(writer, httpRequest)

		if err := client.save(writer, state); err != nil {
			return err
		}

		return response.Redirect(writer, http.StatusFound, ready.oauth.AuthCodeURL(state.State, client.authOptions(state)...))
	}
}

// newState creates the random values of one login.
func (client *Client) newState(returnTo string) (loginState, error) {
	state, err := random()

	if err != nil {
		return loginState{}, err
	}

	nonce := ""

	if client.provider.isOIDC() {
		if nonce, err = random(); err != nil {
			return loginState{}, err
		}
	}

	verifier := ""

	if client.pkce() {
		verifier = oauth2.GenerateVerifier()
	}

	return loginState{
		Cookie:   client.cookieName(state),
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		ReturnTo: returnTo,
		Expires:  time.Now().Add(loginTTL),
	}, nil
}

// pkce reports whether logins use PKCE.
func (client *Client) pkce() bool {
	return client.config.PKCE && !client.provider.noPKCE
}

// authOptions returns the authorization URL parameters of a login.
func (client *Client) authOptions(state loginState) []oauth2.AuthCodeOption {
	var options []oauth2.AuthCodeOption

	for key, value := range client.provider.authParams {
		options = append(options, oauth2.SetAuthURLParam(key, value))
	}

	if state.Nonce != "" {
		options = append(options, oauth2.SetAuthURLParam("nonce", state.Nonce))
	}

	if state.Verifier != "" {
		options = append(options, oauth2.S256ChallengeOption(state.Verifier))
	}

	if client.provider.formPost {
		options = append(options, oauth2.SetAuthURLParam("response_mode", "form_post"))
	}

	return options
}

// random returns a cryptographically random URL-safe value.
func random() (string, error) {
	value := make([]byte, 32)

	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(value), nil
}
