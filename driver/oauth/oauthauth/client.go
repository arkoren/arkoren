package oauthauth

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/crypto"
)

// defaultName is the login cookie name used when [Config.Name] is empty.
const defaultName = "arkoren.oauth"

// Client logs users in through one provider and verifies its bearer tokens.
// New publishes immutable settings; Start performs OIDC discovery before use. The
// cipher and event bus remain shared services owned by the caller. Client has no
// Stop method and does not stop the cipher. Retain its pointer: atomic state must
// not be copied, and pointer formatting redacts owned credentials.
//
// Example:
//
//	client, err := oauth.New(oauth.Google(), cipher, "web", func(client *oauth.ClientBuilder) {
//		client.RedirectURL("https://app.example.com/oauth/callback")
//	})
//	if err != nil {
//		return err
//	}
//	// Register discovery; New itself performs no network I/O.
//	compose.Startup(client.Start)
type Client struct {
	// provider describes the identity provider.
	provider Provider

	// clientID is the registered client identifier passed to [New].
	clientID string

	// config is the validated optional configuration, fixed at construction.
	config Config

	// cipher seals the login cookies.
	cipher *crypto.Encrypter

	// prefix starts the name of every login cookie of this client.
	prefix string

	// ready holds the endpoints and verifier. It is nil until [Client.Start]
	// succeeds for OIDC providers and is replaced atomically.
	ready atomic.Pointer[endpoints]
}

// endpoints is the provider state produced by [New] or [Client.Start].
type endpoints struct {
	// oauth is the OAuth2 configuration of the client.
	oauth oauth2.Config

	// verifier verifies OIDC tokens. It is nil for OAuth2 providers.
	verifier *oidc.IDTokenVerifier
}

// New validates the configuration, including a provider's client secret
// derivation such as the Apple key, and creates a client. It performs no I/O:
// OIDC discovery happens in [Client.Start].
//
// provider is the identity provider. cipher seals the login cookies; use a
// long-lived key shared by all instances. clientID is the registered client
// identifier, also the ID-token audience used by login callbacks. Configure
// access-token audiences separately with ClientBuilder.Audiences. Callbacks
// run directly once in order on a fresh draft before validation; nil entries
// panic. Expanded nil or empty slices omit edits.
//
// New returns an error when cipher is nil, clientID is empty ("OAuth client ID
// cannot be empty"), the provider is incomplete, or the provider cannot derive
// its client secret, or the configured name produces an invalid HTTP cookie name.
//
// Example:
//
//	client, err := oauth.New(
//		oauth.OIDC("https://id.example.com/realms/app"),
//		cipher,
//		"web",
//		func(client *oauth.ClientBuilder) {
//			client.RedirectURL("https://app.example.com/oauth/callback")
//		},
//	)
//	if err != nil {
//		return err
//	}
//	_ = client
func New(
	provider Provider,
	cipher *crypto.Encrypter,
	clientID string,
	configure ...ClientBuilderFunc,
) (*Client, error) {
	config := configureClient(configure...)

	if err := validateClientInputs(provider, cipher, clientID); err != nil {
		return nil, err
	}

	if provider.clientSecret != nil {
		if _, err := provider.clientSecret(clientID, config.ClientSecret); err != nil {
			return nil, fmt.Errorf("OAuth client secret: %w", err)
		}
	}

	if len(config.Scopes) == 0 {
		config.Scopes = slices.Clone(provider.scopes)
	}

	if provider.isOIDC() && len(config.Scopes) == 0 {
		config.Scopes = []string{oidc.ScopeOpenID}
	}

	config.Name = cmp.Or(config.Name, defaultName)

	client := &Client{
		provider: provider,
		clientID: clientID,
		config:   config,
		cipher:   cipher,
		prefix:   config.Name + ".",
	}

	if !config.AllowInsecure {
		client.prefix = session.HostPrefix + client.prefix
	}

	if err := (&http.Cookie{Name: client.prefix + "state"}).Valid(); err != nil {
		return nil, fmt.Errorf("OAuth login cookie name: %w", err)
	}

	if !provider.isOIDC() {
		client.ready.Store(client.endpoints(oauth2.Endpoint{
			AuthURL:  provider.endpoint.AuthURL,
			TokenURL: provider.endpoint.TokenURL,
		}, nil))
	}

	return client, nil
}

// validateClientInputs checks required values in their established error order.
// It does no I/O and does not derive provider secrets; New does that only after
// these ordinary argument checks succeed and all policies have already run.
func validateClientInputs(provider Provider, cipher *crypto.Encrypter, clientID string) error {
	if cipher == nil {
		return errors.New("OAuth cipher cannot be nil")
	}

	if clientID == "" {
		return errors.New("OAuth client ID cannot be empty")
	}

	endpoints := provider.endpoint.AuthURL != "" && provider.endpoint.TokenURL != ""
	oauthProvider := endpoints && provider.userInfo != nil

	if !provider.isOIDC() && !oauthProvider {
		return errors.New("OAuth provider needs an issuer URL, or endpoints and a user info function")
	}

	return nil
}

// endpoints builds the provider state for endpoint and verifier.
func (client *Client) endpoints(endpoint oauth2.Endpoint, verifier *oidc.IDTokenVerifier) *endpoints {
	return &endpoints{
		oauth: oauth2.Config{
			ClientID:     client.clientID,
			ClientSecret: client.config.ClientSecret,
			RedirectURL:  client.config.RedirectURL,
			Endpoint:     endpoint,
			Scopes:       slices.Clone(client.config.Scopes),
		},
		verifier: verifier,
	}
}

// Start discovers an OIDC provider and prepares token verification. It
// does nothing for OAuth2 providers. It must succeed before the client
// serves requests; until then handlers fail with [ErrNotStarted]. Calling
// Start again refreshes the discovered data.
// Discovery errors retain native causes and, when discovery fails after the
// context ends, context.Canceled or context.DeadlineExceeded for errors.Is.
// This method acquires no separately closeable resource; the caller still owns
// the shared cipher and any HTTP client supplied through the native context.
//
// Example:
//
//	return client.Start(ctx)
func (client *Client) Start(ctx context.Context) error {
	if !client.provider.isOIDC() {
		return nil
	}

	if client.provider.identity != nil {
		ctx = oidc.InsecureIssuerURLContext(ctx, client.provider.issuer)
	}

	discovered, err := oidc.NewProvider(ctx, client.provider.issuer)

	if err != nil {
		return fmt.Errorf("discover OIDC provider: %w", errors.Join(err, ctx.Err()))
	}

	verifier := discovered.Verifier(&oidc.Config{
		SkipClientIDCheck: true,
		SkipIssuerCheck:   client.provider.identity != nil,
	})
	client.ready.Store(client.endpoints(discovered.Endpoint(), verifier))

	return nil
}

// started returns the provider state, or [ErrNotStarted] before
// [Client.Start] has succeeded.
func (client *Client) started() (*endpoints, error) {
	ready := client.ready.Load()

	if ready == nil {
		return nil, ErrNotStarted
	}

	return ready, nil
}
