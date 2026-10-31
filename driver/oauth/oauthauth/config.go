package oauthauth

import (
	"slices"

	"arkoren.dev/core/event"
)

// ClientBuilderFunc edits one fresh client draft synchronously, once in order,
// before New validates inputs. Nil entries panic; expanded nil or empty slices
// omit edits. Publication copies owned settings, not service pointers or captures.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.Audiences("https://api.example.com")
//	}
//	policies := []oauth.ClientBuilderFunc{configure}
//	client, err := oauth.New(oauth.Google(), cipher, "web", policies...)
//	if err != nil {
//		return err
//	}
//	_ = client
type ClientBuilderFunc = func(*ClientBuilder)

// Config is a redacted representation of optional client settings, retained for
// safe formatting and structured logging. It is not a constructor input: use
// ClientBuilder with New. The required client identifier remains positional.
//
// Example:
//
//	config := oauth.Config{ClientSecret: "secret", PKCE: true}
//	text := config.String() // contains [REDACTED], not the secret
//	_ = text
type Config struct {
	// ClientSecret is the confidential client secret. Load it through a
	// secret provider rather than committing it to configuration.
	//
	// Example:
	//
	//	config := oauth.Config{ClientSecret: "synthetic-secret"}
	//	_ = config.String() // Redacts the secret; Config is diagnostic only.
	ClientSecret string

	// RedirectURL is the exact callback URL registered with the provider.
	// It is required by [Client.Login].
	//
	// Example:
	//
	//	config := oauth.Config{RedirectURL: "https://app.example.com/oauth/callback"}
	//	_ = config.String()
	RedirectURL string

	// Scopes are requested during login. Empty uses the provider defaults,
	// and "openid" for plain OIDC providers.
	//
	// Example:
	//
	//	config := oauth.Config{Scopes: []string{"openid", "email"}}
	//	_ = config.String()
	Scopes []string

	// Audiences are accepted JWT audiences for [Client.Verify], which fails
	// with [ErrNoAudiences] when empty. Login callbacks always accept the
	// client ID passed to [New] for ID tokens, regardless of this setting.
	//
	// Example:
	//
	//	config := oauth.Config{Audiences: []string{"https://api.example.com"}}
	//	_ = config.String()
	Audiences []string

	// Name prefixes the login cookie names. Empty uses "arkoren.oauth".
	// New rejects names that produce an invalid HTTP cookie name.
	//
	// Example:
	//
	//	config := oauth.Config{Name: "login"}
	//	_ = config.String()
	Name string

	// AllowInsecure drops the Secure attribute and the "__Host-" cookie
	// prefix so login works over plain HTTP. Never enable it in production.
	//
	// Example:
	//
	//	config := oauth.Config{AllowInsecure: false}
	//	_ = config.String()
	AllowInsecure bool

	// PKCE sends a PKCE challenge with the login. [New] enables it by default.
	//
	// Example:
	//	config := oauth.Config{PKCE: true}
	//	_ = config.String()
	PKCE bool

	// Events receives [LoginSucceeded], [LoginFailed], [LoggedOut], and
	// [TokenRejected]. Nil, the default, publishes nothing.
	// The bus stays shared; failure payloads contain bounded classifications,
	// not the causes retained by Callback and Verify's returned errors.
	//
	// Example:
	//
	//	bus := event.New()
	//	config := oauth.Config{Events: bus}
	//	_ = config.Events == bus
	Events *event.Bus
}

// ClientBuilder configures one OAuth client. New invokes callbacks directly
// once in order on one fresh draft before validation. Nil entries panic;
// expanded nil or empty slices omit edits. PKCE starts enabled. Retained drafts
// cannot change constructed settings; services and function captures stay shared.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.RedirectURL("https://app.example/callback")
//	}
//	client, err := oauth.New(oauth.Google(), cipher, "client-id", configure)
//	if err != nil {
//		return err
//	}
//	_ = client
type ClientBuilder struct {
	// config is the mutable root, detached before publication.
	config Config
}

// configureClient invokes policies before validation, preserving explicit false
// security settings, and copies owned scope and audience slices at publication.
func configureClient(configure ...ClientBuilderFunc) Config {
	client := &ClientBuilder{
		config: Config{
			Name: defaultName,
			PKCE: true,
		},
	}

	for _, configure := range configure {
		configure(client)
	}

	config := client.config
	config.Scopes = slices.Clone(config.Scopes)
	config.Audiences = slices.Clone(config.Audiences)

	return config
}

// ClientSecret replaces the confidential client secret and returns the same
// draft. Empty uses no secret unless a provider derives one, as Apple does.
// Load secrets from a secret provider rather than committing them to source.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.ClientSecret("secret")
//	}
//	_ = configure
func (client *ClientBuilder) ClientSecret(clientSecret string) *ClientBuilder {
	client.config.ClientSecret = clientSecret

	return client
}

// RedirectURL sets the exact registered callback URL and returns the same draft.
// Empty is accepted at construction but Login requires a callback URL.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.RedirectURL("https://app.example/callback")
//	}
//	_ = configure
func (client *ClientBuilder) RedirectURL(redirectURL string) *ClientBuilder {
	client.config.RedirectURL = redirectURL

	return client
}

// Scopes replaces requested login scopes and returns the same draft. Empty
// uses the provider defaults, or openid for plain OIDC. The input slice is
// copied here and again at publication; later edits cannot change the client.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.Scopes("openid", "email")
//	}
//	_ = configure
func (client *ClientBuilder) Scopes(scopes ...string) *ClientBuilder {
	client.config.Scopes = slices.Clone(scopes)

	return client
}

// Audiences replaces accepted access-token audiences and returns the same draft.
// Empty makes Verify return ErrNoAudiences. Use API audiences, not the client ID:
// login callbacks independently accept the client ID for ID tokens. The input
// slice is copied here and at publication.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.Audiences("api")
//	}
//	_ = configure
func (client *ClientBuilder) Audiences(audiences ...string) *ClientBuilder {
	client.config.Audiences = slices.Clone(audiences)

	return client
}

// Name replaces the login cookie prefix and returns the same draft. Empty
// normalizes to arkoren.oauth; secure clients additionally use the __Host- prefix.
// New rejects a final prefix that cannot form a valid HTTP cookie name.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.Name("login")
//	}
//	_ = configure
func (client *ClientBuilder) Name(name string) *ClientBuilder {
	client.config.Name = name

	return client
}

// AllowInsecure controls plain-HTTP development cookies and returns the same
// draft. False, the default, keeps Secure and __Host-. Never enable this in
// production: it permits login cookies to travel over unencrypted connections.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.AllowInsecure(false)
//	}
//	_ = configure
func (client *ClientBuilder) AllowInsecure(allow bool) *ClientBuilder {
	client.config.AllowInsecure = allow

	return client
}

// PKCE controls the login challenge and returns the same draft. It is enabled
// initially. Explicit false remains disabled; constructors do not restore it.
// Disable it only for a provider that cannot support PKCE.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.PKCE(true)
//	}
//	_ = configure
func (client *ClientBuilder) PKCE(enabled bool) *ClientBuilder {
	client.config.PKCE = enabled

	return client
}

// Events sets the shared bus receiving login, logout and token outcomes and
// returns the same draft. Nil, the default, disables publication. The bus and
// its listeners are not copied or invoked during configuration.
//
// Example:
//
//	configure := func(client *oauth.ClientBuilder) {
//		client.Events(nil)
//	}
//	_ = configure
func (client *ClientBuilder) Events(bus *event.Bus) *ClientBuilder {
	client.config.Events = bus

	return client
}
