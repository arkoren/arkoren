// Package provider owns preset claim, signing, and user-info policy after native verification.
package provider

import (
	"strings"
)

// Record carries verified claim data across the private preset boundary. The
// public Identity remains a separately defined type in the owning package.
// Struct conversion preserves the existing map and slice ownership rules.
type Record struct {
	// Subject is the stable account identifier selected by the provider.
	Subject string
	// Issuer identifies the verified authority.
	Issuer string
	// Email is optional profile data filtered by provider verification evidence.
	Email string
	// Name is untrusted display text.
	Name string
	// Scopes retains the granted scopes without additional copying.
	Scopes []string
	// Claims retains the native verified claim map without recursive copying.
	Claims map[string]any
}

// FromClaims maps verified token claims to an [Record].
func FromClaims(claims map[string]any) Record {
	text := func(key string) string {
		value, _ := claims[key].(string)

		return value
	}

	return Record{
		Subject: text("sub"),
		Issuer:  text("iss"),
		Email:   text("email"),
		Name:    text("name"),
		Scopes:  strings.Fields(text("scope")),
		Claims:  claims,
	}
}
