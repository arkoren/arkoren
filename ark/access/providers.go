package access

import (
	"errors"
	"fmt"
	"net/http"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
)

// Provider is a browser login with an external identity provider, such as the
// *oauthauth.Client of arkoren.dev/ark/oauth, which satisfies it structurally:
// this module never imports ark/oauth.
//
// Example:
//
//	iam.Provider(google, github) // *oauthauth.Client values
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
//	var _ access.NativeProvider = (*oauthauth.Client)(nil)
type NativeProvider interface {
	// NonceHandler issues the nonce the app puts in its ID token request.
	NonceHandler() router.Handler

	// NativeHandler verifies the app's ID token, maps it to a subject with resolve
	// and begins the login on guard.
	NativeHandler(guard *auth.Guard, resolve auth.Resolver) router.Handler
}

// validateProviders rejects a nil provider, an empty name and a name used twice,
// since each name is a mounted path.
func validateProviders(providers []Provider) error {
	names := map[string]bool{}
	for _, provider := range providers {
		if provider == nil || provider.Name() == "" {
			return errors.New("a provider is nil or has an empty name")
		}
		if names[provider.Name()] {
			return fmt.Errorf("duplicate provider name %q: set a distinct name on one of them", provider.Name())
		}
		names[provider.Name()] = true
	}

	return nil
}

// mountProviders registers the login and callback routes of every provider,
// and the nonce and native routes of those that implement [NativeProvider].
func (iam *Access) mountProviders(r *router.Router) {
	for _, provider := range iam.providers {
		base := "/auth/" + provider.Name()
		r.Get(base, provider.LoginHandler())
		r.Methods([]string{http.MethodGet, http.MethodPost}, base+"/callback", provider.CallbackHandler(iam.guard, iam.links.Resolve))
		if native, ok := provider.(NativeProvider); ok {
			r.Get(base+"/nonce", native.NonceHandler())
			r.Post(base+"/native", native.NativeHandler(iam.guard, iam.links.Resolve))
		}
	}
}

// connectTargets maps each provider name to the path that starts its login,
// where the connect handler redirects.
func (iam *Access) connectTargets() map[string]string {
	targets := make(map[string]string, len(iam.providers))
	for _, provider := range iam.providers {
		targets[provider.Name()] = "/auth/" + provider.Name()
	}

	return targets
}
