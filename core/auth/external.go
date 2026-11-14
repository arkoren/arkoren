package auth

import "context"

// External is an identity asserted by another system, for example the verified
// claims of an OpenID Connect ID token. It is the shared vocabulary between the
// module that verifies the assertion (such as ark/oauth) and the module that maps
// it to a local user: neither imports the other, and core contains no provider
// interface. An External is a value the verifier already checked; the resolver
// must still decide what it is allowed to do, in particular whether to trust
// Email (see EmailVerified).
//
// Example:
//
//	external := auth.External{
//		Issuer:        "https://accounts.example.com",
//		Subject:       "1089",
//		Email:         "ada@example.com",
//		EmailVerified: true,
//	}
type External struct {
	// Issuer identifies the system that made the assertion, such as the OIDC
	// issuer URL. Together with Subject it names the external account.
	Issuer string

	// Subject is the issuer's stable identifier of the account. It is unique
	// only within Issuer and is not a local subject (see [ValidateSubject]).
	Subject string

	// Email is the address the issuer reports, or "" when it reports none. Match
	// accounts by it only when EmailVerified is true.
	Email string

	// EmailVerified reports whether the issuer vouches that the user controls
	// Email.
	EmailVerified bool

	// Claims holds the remaining verified claims. The resolver owns it after the
	// call; verifiers must not share the map with later requests.
	Claims map[string]any
}

// Resolver maps an [External] identity to the local subject of a user, creating
// or linking the user when the application's policy says so. It is the only code
// that decides which local account an outside assertion may log into, so it
// owns account-takeover decisions such as linking by email. It returns an error
// wrapping [ErrUnauthenticated] to refuse the login; any other error is an
// operational failure. The returned subject must satisfy [ValidateSubject].
//
// Example:
//
//	var resolve auth.Resolver = func(ctx context.Context, external auth.External) (string, error) {
//		subject, err := links.Find(ctx, external.Issuer, external.Subject)
//		if err != nil {
//			return "", err
//		}
//		return subject, nil
//	}
type Resolver func(ctx context.Context, external External) (subject string, err error)
