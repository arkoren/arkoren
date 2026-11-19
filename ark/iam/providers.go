package iam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/iam/internal/web"
)

// Provider is a browser login with an external identity provider, such as the
// *oauthauth.Client of arkoren.dev/ark/oauth, which satisfies it structurally:
// this module never imports ark/oauth.
//
// Example:
//
//	identity.Provider(google) // an *oauthauth.Client
type Provider interface {
	// Name is the provider's name in mounted paths: "/auth/<name>" starts the
	// login and "/auth/<name>/callback" finishes it. Names must be unique.
	Name() string

	// LoginHandler starts the login by redirecting to the provider.
	LoginHandler() router.Handler

	// CallbackHandler finishes the login: it verifies the provider's answer,
	// maps it to a subject with resolve and begins the login on guard.
	CallbackHandler(guard *auth.Guard, resolve auth.Resolver) router.Handler
}

// NativeProvider is the extra capability of a [Provider] that also accepts an ID
// token from a mobile app's own provider SDK (Google and Apple in ark/oauth). A
// provider that implements it also gets "/auth/<name>/nonce" and
// "/auth/<name>/native".
//
// Example:
//
//	var _ identity.NativeProvider = (*oauthauth.Client)(nil)
type NativeProvider interface {
	// NonceHandler issues the nonce the app puts in its ID token request.
	NonceHandler() router.Handler

	// NativeHandler verifies the app's ID token, maps it to a subject with resolve
	// and begins the login on guard.
	NativeHandler(guard *auth.Guard, resolve auth.Resolver) router.Handler
}

// ProviderBuilder configures one [Provider] in a [Builder.Provider] call. Its
// settings belong to that provider alone: another provider of the same facade
// can have different ones. Callbacks run directly once, in order, on one fresh
// draft and methods return the same draft.
//
// Example:
//
//	identity.Provider(entra, func(login *iam.ProviderBuilder) {
//		login.SignUp(true)
//	})
type ProviderBuilder struct {
	signUp bool
	create func(context.Context, auth.External) (string, error)
	keep   []string
}

// ProviderBuilderFunc is the callback form of [ProviderBuilder].
//
// Example:
//
//	var enableSignUp identity.ProviderBuilderFunc = func(login *iam.ProviderBuilder) {
//		login.SignUp(true)
//	}
//	identity.Provider(entra, enableSignUp)
type ProviderBuilderFunc = func(*ProviderBuilder)

// SignUp lets a first logged-out login with this provider and an identity the
// application does not know create a user and connect the identity to it; the
// default is false. With false such a login fails with
// [federation.ErrNoAccount] (403, "federation:no-account") and creates nothing,
// so only people the application already has can sign in with this provider:
// they log in another way and connect it, or the application adds the user
// first. Logging in with an identity already connected, a logged-in user
// connecting the provider, and the takeover of an unverified built-in user by
// the owner of a provider-verified address never need it. With true, a first
// login must carry an email the provider proved, or it fails with
// [federation.ErrEmailUnverified] (403) and creates nothing. It needs users
// that can be created, which the built-in users always are; with [Builder.Users]
// that has no [Users.Create], nothing could create the user: [New] ignores
// SignUp, with one warning, and the provider logs in only known identities.
//
// Example:
//
//	login.SignUp(true)
func (login *ProviderBuilder) SignUp(enabled bool) *ProviderBuilder {
	login.signUp = enabled

	return login
}

// Create adds the application's rule for creating the user of an identity that
// signs up with this provider; it only runs when [ProviderBuilder.SignUp] is
// enabled. It runs before the facade's own creation and returns the new user's
// subject, and its errors pass through to the client. Returning "" and no
// error hands the identity on, so what it declines still gets the facade's
// creation (a user made through [Users.Create], verified when the provider
// proved the email). Calls add up in the order made until one returns a
// subject. The facade never links by email, so the function must not return an
// existing user. See [federation.Builder.Create].
//
// Example:
//
//	login.Create(func(ctx context.Context, external auth.External) (string, error) {
//		if !strings.HasSuffix(external.Email, "@acme.example") {
//			return "", errNotInvited
//		}
//		return "", nil
//	})
func (login *ProviderBuilder) Create(create func(ctx context.Context, external auth.External) (string, error)) *ProviderBuilder {
	login.create = web.First(login.create, create)

	return login
}

// KeepClaims names claims of this provider's verified identity that its
// connection stores, to prefill a profile form from [federation.Links.List]
// later (Google sends "name" and "picture", GitHub "name" and "login"). Nothing
// is kept by default and calls add up. The claims are unverified suggestions of
// the provider: use them to prefill, never for authorization, identity or to
// match users. A login overwrites the claims it carries and keeps the rest, so
// Apple's name, sent only on the first login, stays; a claim the provider stops
// sending stays too. Only strings, booleans, numbers and lists of strings are
// kept, up to 8 KiB per connection. An empty name or a protocol claim (iss,
// sub, aud, exp, iat, nbf, nonce, at_hash, auth_time, scope, azp, acr, amr)
// makes [New] fail with [ErrInvalidConfig]. See
// [federation.ResolverBuilder.KeepClaims] for the storage rules; GET
// /connections shows the kept claims.
//
// Example:
//
//	login.KeepClaims("name", "picture")
func (login *ProviderBuilder) KeepClaims(names ...string) *ProviderBuilder {
	login.keep = append(login.keep, names...)

	return login
}

// providerSetup is one [Builder.Provider] call: the provider and the settings
// its callbacks produced.
type providerSetup struct {
	provider Provider
	settings ProviderBuilder
}

// mountedProvider is a provider with the resolver that carries its sign-up
// settings, which [IAM.registerProviders] hands to its handlers.
type mountedProvider struct {
	provider Provider
	resolve  auth.Resolver
}

// validateProviders rejects a nil provider, an empty name, a name with anything but
// letters, digits, hyphens and underscores (it must be one safe path segment of
// "/auth/<name>": the connect handler redirects there and the router would
// reject or reinterpret a pattern with braces, slashes or dots), a name used
// twice, since each name is a mounted path.
func validateProviders(setups []providerSetup) error {
	names := map[string]bool{}
	for _, setup := range setups {
		provider := setup.provider
		if provider == nil || provider.Name() == "" {
			return errors.New("a provider is nil or has an empty name")
		}
		if strings.IndexFunc(provider.Name(), func(char rune) bool {
			return !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '-' && char != '_'
		}) >= 0 {
			return fmt.Errorf("provider name %q: use only letters, digits, hyphens and underscores", provider.Name())
		}
		if names[provider.Name()] {
			return fmt.Errorf("duplicate provider name %q: configure each provider in one Provider call", provider.Name())
		}
		names[provider.Name()] = true
	}

	return nil
}

// registerProviders registers the login and callback routes of every provider,
// and the nonce and native routes of those that implement [NativeProvider].
func (identity *IAM) registerProviders(r *router.Router) {
	for _, mounted := range identity.providers {
		provider := mounted.provider
		base := "/auth/" + provider.Name()
		r.Get(base, provider.LoginHandler())
		r.Methods([]string{http.MethodGet, http.MethodPost}, base+"/callback", provider.CallbackHandler(identity.guard, mounted.resolve))
		if native, ok := provider.(NativeProvider); ok {
			r.Get(base+"/nonce", native.NonceHandler())
			r.Post(base+"/native", native.NativeHandler(identity.guard, mounted.resolve))
		}
	}
}

// connectTargets maps each provider name to the path that starts its login,
// where the connect handler redirects.
func (identity *IAM) connectTargets() map[string]string {
	targets := make(map[string]string, len(identity.providers))
	for _, mounted := range identity.providers {
		targets[mounted.provider.Name()] = "/auth/" + mounted.provider.Name()
	}

	return targets
}
