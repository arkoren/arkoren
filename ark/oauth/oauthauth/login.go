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

// LoginHandler starts an Authorization Code login. It redirects to the provider
// and remembers the optional local return_to query target in a sealed cookie.
// It needs no session.
//
// With the query parameter fresh=1 on an OIDC provider, the authorization
// request also carries prompt=login and max_age=0, which asks the provider to
// authenticate the user again instead of approving silently, and the request is
// recorded in the sealed login state. It is a best-effort hint: a provider may
// ignore it. The callback reads the ID token's auth_time claim, which can only
// make the federated factor older than this server's clock, never fresher (see
// [Client.CallbackHandler]); whether the factor satisfies
// [auth.Guard.RequireFresh] depends on the user's step-up methods. OAuth2
// providers have no such request and ignore the parameter.
//
// Example:
//
//	r.Get("/auth/google", client.LoginHandler(), oauthauth.LoginOperation) // supports ?return_to=/local/path&fresh=1
func (client *Client) LoginHandler() router.Handler {
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

		fresh := client.provider.isOIDC() && httpRequest.URL.Query().Get("fresh") == "1"

		state, err := client.newState(returnTo, fresh)

		if err != nil {
			return err
		}

		client.prune(writer, httpRequest)

		if err := client.save(writer, state); err != nil {
			return err
		}

		options := client.authOptions(state)

		if fresh {
			options = append(options, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
		}

		return response.Redirect(writer, http.StatusFound, ready.oauth.AuthCodeURL(state.State, options...))
	}
}

// newState creates the random values of one login. fresh records that the
// login asked the provider to authenticate the user again.
func (client *Client) newState(returnTo string, fresh bool) (loginState, error) {
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
		Fresh:    fresh,
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
