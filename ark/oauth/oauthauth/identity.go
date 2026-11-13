package oauthauth

// Identity represents an authenticated subject.
// OIDC verification requires a nonempty final Subject after provider-specific
// mapping. Claims and optional profile data remain provider-controlled; they are
// not safe diagnostic payloads or a basis for account linking by email.
//
// Example:
//
//	identity, err := client.Verify(ctx, token)
//	if err != nil {
//		return err
//	}
//	issuer, subject := identity.Issuer, identity.Subject // Persist as a pair.
//	_, _ = issuer, subject
type Identity struct {
	// Subject is the provider's stable identifier for the authenticated subject.
	// Use it together with Issuer; it is not globally unique on its own.
	//
	// Example:
	//
	//	identity := oauth.Identity{Issuer: "https://id.example.com", Subject: "user-42"}
	//	_, _ = identity.Issuer, identity.Subject // Composite account key.
	Subject string `json:"subject"`

	// Issuer identifies the provider that authenticated the subject.
	// Include it in persistent external-identity keys, not only the subject.
	//
	// Example:
	//
	//	identity := oauth.Identity{Issuer: "https://id.example.com", Subject: "user-42"}
	//	_, _ = identity.Issuer, identity.Subject
	Issuer string `json:"issuer"`
	// Email is optional profile data supplied by the provider. Google, GitHub
	// and Microsoft filter it using email_verified, verified-primary and xms_edov
	// evidence respectively, leaving it empty without that evidence. Other
	// providers, including plain OIDC, can pass it through unchecked. Never link
	// accounts by email.
	//
	// Example:
	//
	//	identity := oauth.Identity{Email: "reader@example.com"}
	//	_ = identity.Email // Optional profile data, not an account key.
	Email string `json:"email"`

	// Name is the optional name claim supplied by the provider.
	// Treat it as untrusted display text, not an authorization attribute.
	//
	// Example:
	//
	//	identity := oauth.Identity{Name: "Example Reader"}
	//	_ = identity.Name // Escape when rendering in HTML.
	Name string `json:"name"`

	// Scopes are the scopes granted to the subject: the token response scope
	// for browser logins, the scope claim for bearer tokens. When the response
	// omits scope, callback uses a private copy of the requested scopes.
	//
	// Example:
	//
	//	identity := auth.NewIdentity(subject, func(principal *auth.IdentityBuilder) {
	//		principal.Scopes(external.Scopes...) // external is the verified oauth.Identity.
	//	})
	//	r.With(gate.Require(gate.HasScopes("posts:write"))).Post("/posts", create)
	//	_ = identity
	Scopes []string `json:"scopes,omitzero"`

	// Claims holds provider-controlled token claims.
	// It is not recursively sanitized or copied; do not log arbitrary claims.
	//
	// Example:
	//
	//	identity := oauth.Identity{Claims: map[string]any{"hd": "example.com"}}
	//	domain, _ := identity.Claims["hd"].(string)
	//	_ = domain // Apply provider-specific policy, not generic email trust.
	Claims map[string]any `json:"claims"`
}
