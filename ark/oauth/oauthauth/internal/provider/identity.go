// Package provider owns preset claim, signing, and user-info policy after native verification.
package provider

import "arkoren.dev/core/auth"

// FromClaims maps verified token claims to an [auth.External]. Claims keeps the
// native verified claim map without recursive copying. EmailVerified is true
// only when the token carries email_verified as the boolean true or the string
// "true" (Apple sends either); a missing or malformed claim leaves it false, so
// an unverified address is never reported as verified.
func FromClaims(claims map[string]any) auth.External {
	text := func(key string) string {
		value, _ := claims[key].(string)

		return value
	}

	verified, _ := claims["email_verified"].(bool)

	return auth.External{
		Issuer:        text("iss"),
		Subject:       text("sub"),
		Email:         text("email"),
		EmailVerified: verified || text("email_verified") == "true",
		Claims:        claims,
	}
}
