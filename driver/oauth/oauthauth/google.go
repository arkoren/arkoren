package oauthauth

import (
	"net/url"

	"arkoren.dev/driver/oauth/oauthauth/internal/provider"
)

// GoogleBuilderFunc edits one fresh provider draft synchronously, once in order.
// Nil entries panic; expanded nil or empty slices omit edits. Google detaches
// the completed hosted-domain restriction before returning its provider value.
//
// Example:
//
//	configure := func(provider *oauth.GoogleBuilder) {
//		provider.HostedDomain("example.com")
//	}
//	policies := []oauth.GoogleBuilderFunc{configure}
//	provider := oauth.Google(policies...)
//	_ = provider
type GoogleBuilderFunc = func(*GoogleBuilder)

// googleIssuer is the OIDC issuer of Google accounts.
const googleIssuer = "https://accounts.google.com"

// GoogleBuilder restricts the Google preset to a verified Workspace domain.
// Callbacks run directly once in order on one fresh draft; nil entries panic
// and expanded nil or empty slices omit edits. Published providers detach this
// root so a retained draft cannot change authorization or identity checks.
//
// Example:
//
//	provider := oauth.Google(func(provider *oauth.GoogleBuilder) {
//		provider.HostedDomain("example.com")
//	})
//	_ = provider
type GoogleBuilder struct {
	// hostedDomain restricts logins to one Google Workspace domain.
	hostedDomain string
}

// HostedDomain restricts logins to the Google Workspace domain. It sends
// the "hd" authorization parameter, which is only a hint to the account
// chooser, and rejects every identity whose verified "hd" claim differs.
// It replaces the restriction and returns the same draft; empty permits all
// domains. The constructed provider detaches this setting from retained drafts.
//
// Example:
//
//	provider := oauth.Google(func(provider *oauth.GoogleBuilder) {
//		provider.HostedDomain("example.com")
//	})
//	_ = provider
func (definition *GoogleBuilder) HostedDomain(domain string) *GoogleBuilder {
	definition.hostedDomain = domain

	return definition
}

// Google returns the Google Sign-In provider. It requests the scopes openid,
// email and profile, and reports the issuer "https://accounts.google.com"
// whether the token carries that form or "accounts.google.com".
//
// Identity.Email is set only when Google reports email_verified; otherwise
// it is empty. Match users on Issuer + Subject, not on email.
// Construction callbacks run directly once in order on one fresh GoogleBuilder;
// nil entries panic and expanded nil or empty slices omit edits. Publication
// detaches the completed restriction before capturing it in claim validation.
//
// Example:
//
//	client, err := oauth.New(oauth.Google(), cipher, id, func(client *oauth.ClientBuilder) {
//		client.ClientSecret(secret)
//	})
//	if err != nil {
//		return err
//	}
//	_ = client
func Google(configure ...GoogleBuilderFunc) Provider {
	return google(googleIssuer, configure...)
}

// google builds the Google preset with a testable issuer override. It invokes
// policies directly once in order on a fresh draft, then copies the completed
// root before capturing it in claim checks. Required claim functions remain
// deferred until identity validation; constructor policies are never replayed.
func google(issuer string, configure ...GoogleBuilderFunc) Provider {
	definition := &GoogleBuilder{}

	for _, configure := range configure {
		configure(definition)
	}

	config := *definition

	// Google also issues tokens with the scheme-less issuer.
	bare := issuer

	if parsed, err := url.Parse(issuer); err == nil {
		bare = parsed.Host
	}

	result := Provider{
		issuer: issuer,
		scopes: []string{"openid", "email", "profile"},
		identity: func(identity Identity) (Identity, error) {
			checked, err := provider.Google(provider.Record(identity), issuer, bare, config.hostedDomain)

			return Identity(checked), err
		},
	}

	if config.hostedDomain != "" {
		result.authParams = map[string]string{"hd": config.hostedDomain}
	}

	return result
}
