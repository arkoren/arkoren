package oauthauth

import (
	"context"
	"net/http"

	"arkoren.dev/driver/oauth/oauthauth/internal/provider"
)

// GitHub returns a GitHub OAuth2 provider with the scopes "read:user" and
// "user:email". The subject is the numeric user ID, which never changes,
// unlike the login. The email is the primary verified address, or empty when
// there is none. GitHub has no OIDC, so [Client.Verify] is unsupported.
//
// Example:
//
//	client, err := oauth.New(oauth.GitHub(), cipher,
//		os.Getenv("GITHUB_CLIENT_ID"),
//		func(client *oauth.ClientBuilder) {
//			client.ClientSecret(os.Getenv("GITHUB_CLIENT_SECRET"))
//			client.RedirectURL("https://app.example.com/oauth/callback")
//		},
//	)
func GitHub() Provider {
	return github("https://api.github.com")
}

// github builds the GitHub provider that calls the API at apiURL.
func github(apiURL string) Provider {
	definition := OAuth2(Endpoint{
		AuthURL:  "https://github.com/login/oauth/authorize",
		TokenURL: "https://github.com/login/oauth/access_token",
	}, func(ctx context.Context, client *http.Client) (Identity, error) {
		record, err := provider.GitHubUser(ctx, client, apiURL)

		return Identity(record), err
	})
	definition.scopes = []string{"read:user", "user:email"}

	return definition
}
