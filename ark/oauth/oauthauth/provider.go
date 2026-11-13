package oauthauth

import (
	"context"
	"net/http"
)

// Endpoint holds the authorization and token URLs of an OAuth2 provider that
// has no OIDC discovery.
//
// Example:
//
//	endpoint := oauth.Endpoint{
//		AuthURL:  "https://github.com/login/oauth/authorize",
//		TokenURL: "https://github.com/login/oauth/access_token",
//	}
//	provider := oauth.OAuth2(endpoint, userInfo) // Your UserInfoFunc owns identity checks.
//	_ = provider
type Endpoint struct {
	// AuthURL is where the browser is sent to authorize.
	// The application supplies this trusted URL; it is not discovered for OAuth2.
	//
	// Example:
	//
	//	endpoint := oauth.Endpoint{
	//		AuthURL:  "https://id.example.com/authorize",
	//		TokenURL: "https://id.example.com/token",
	//	}
	//	provider := oauth.OAuth2(endpoint, userInfo)
	//	_ = provider
	AuthURL string

	// TokenURL is where the authorization code is exchanged.
	// Keep this trusted server-to-server endpoint independent of request input.
	//
	// Example:
	//
	//	endpoint := oauth.Endpoint{
	//		AuthURL:  "https://id.example.com/authorize",
	//		TokenURL: "https://id.example.com/token",
	//	}
	//	provider := oauth.OAuth2(endpoint, userInfo)
	//	_ = provider
	TokenURL string
}

// UserInfoFunc loads the [Identity] of a user from an OAuth2 provider. The
// client is authenticated with the user's access token.
// Use ctx on each request and close response bodies. The function owns stable
// subject and issuer validation and any email-verification policy: the generic
// OAuth2 adapter does not impose a provider-specific schema. Native/client
// dependencies remain shared; returned errors retain causes and can be sensitive.
//
// Example:
//
//	var userInfo oauth.UserInfoFunc = func(ctx context.Context, client *http.Client) (oauth.Identity, error) {
//		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/user", nil)
//		if err != nil {
//			return oauth.Identity{}, err
//		}
//		response, err := client.Do(request)
//		if err != nil {
//			return oauth.Identity{}, err
//		}
//		defer func() {
//			// Cleanup cannot undo the authoritative response classification.
//			_ = response.Body.Close()
//		}()
//		if response.StatusCode != http.StatusOK {
//			return oauth.Identity{}, errors.New("user info unavailable")
//		}
//
//		var user struct {
//			ID string `json:"id"`
//		}
//		if err := json.UnmarshalRead(response.Body, &user); err != nil {
//			return oauth.Identity{}, err
//		}
//		if user.ID == "" {
//			return oauth.Identity{}, errors.New("missing user subject")
//		}
//
//		return oauth.Identity{Issuer: "https://api.example.com", Subject: user.ID}, nil
//	}
type UserInfoFunc = func(ctx context.Context, client *http.Client) (Identity, error)

// Provider describes how to log in with one identity provider. Build it with
// [OIDC] or [OAuth2]; presets in this package fill the optional fields.
// The zero Provider is invalid.
//
// Example:
//
//	provider := oauth.OIDC("https://id.example.com/realms/app")
type Provider struct {
	// issuer is the OIDC issuer URL used for discovery. Empty for OAuth2.
	issuer string

	// endpoint holds the OAuth2 URLs. Unused for OIDC.
	endpoint Endpoint

	// userInfo loads the identity after the code exchange. OAuth2 only.
	userInfo UserInfoFunc

	// scopes are requested when the client configures none.
	scopes []string

	// authParams are added to the authorization URL, for example
	// "prompt" or "access_type".
	authParams map[string]string

	// formPost asks the provider to send the callback as a POST form
	// (response_mode=form_post). Cookies then travel cross-site, so the
	// login cookie uses SameSite=None instead of Lax.
	formPost bool

	// noPKCE turns PKCE off for providers that reject it, whatever the
	// client configuration says.
	noPKCE bool

	// identity replaces the issuer comparison of ID tokens for providers
	// whose issuer varies, such as per-tenant or multiple issuers. It
	// receives the verified identity and returns the identity to use, or
	// an error that rejects it. It may rewrite Issuer, Subject and Email.
	// OIDC only.
	identity func(Identity) (Identity, error)

	// formIdentity completes the identity from the form_post callback
	// request, for data the provider sends only there. It never sees the
	// request of a GET callback.
	formIdentity func(*http.Request, Identity) Identity

	// clientSecret derives the secret sent at the token endpoint from the
	// client ID and the configured secret, for providers that need a
	// signed JWT instead of a static secret. Nil sends the configured
	// secret as is.
	clientSecret func(clientID, secret string) (string, error)
}

// OIDC returns a provider that discovers its endpoints from issuerURL, and
// verifies ID tokens and bearer access tokens.
//
// Example:
//
//	client, err := oauth.New(oauth.OIDC("https://accounts.example.com"), cipher, "web")
func OIDC(issuerURL string) Provider {
	return Provider{
		issuer: issuerURL,
	}
}

// OAuth2 returns a provider without OIDC. userInfo loads the identity after
// the login; bearer verification is unsupported.
//
// Example:
//
//	provider := oauth.OAuth2(oauth.Endpoint{AuthURL: authURL, TokenURL: tokenURL}, userInfo)
func OAuth2(endpoint Endpoint, userInfo UserInfoFunc) Provider {
	return Provider{
		endpoint: endpoint,
		userInfo: userInfo,
	}
}

// isOIDC reports whether the provider uses OIDC discovery.
func (provider Provider) isOIDC() bool {
	return provider.issuer != ""
}
