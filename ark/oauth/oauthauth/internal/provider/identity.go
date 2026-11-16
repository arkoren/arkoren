// Package provider owns preset claim, signing, and user-info policy after native verification.
package provider

import "arkoren.dev/core/auth"

// FromClaims maps verified token claims to an [auth.External]. Claims keeps the
// native verified claim map without recursive copying. EmailVerified is always
// false: the email_verified claim means different things to different issuers,
// so each provider applies its own policy afterwards ([Apple], [OIDC], or the
// preset's identity check). The email itself is kept for those policies to
// judge.
func FromClaims(claims map[string]any) auth.External {
	text := func(key string) string {
		value, _ := claims[key].(string)

		return value
	}

	return auth.External{
		Issuer:  text("iss"),
		Subject: text("sub"),
		Email:   text("email"),
		Claims:  claims,
	}
}

// OIDC maps the claims of a generic OIDC ID token. The email is reported, with
// EmailVerified true, only when trustEmail is set and the token carries
// email_verified as the boolean true; in every other case the email is blanked,
// because an address the provider did not prove must not reach account linking.
func OIDC(claims map[string]any, trustEmail bool) auth.External {
	identity := FromClaims(claims)
	verified, _ := claims["email_verified"].(bool)
	identity.EmailVerified = trustEmail && verified

	if !identity.EmailVerified {
		identity.Email = ""
	}

	return identity
}

// Apple maps the claims of an Apple ID token. Apple sends email_verified as
// either the boolean true or the string "true", and both count as verified; a
// missing or malformed claim leaves EmailVerified false. The email is kept
// either way, because Apple may send a private relay address.
func Apple(claims map[string]any) auth.External {
	identity := FromClaims(claims)
	verified, _ := claims["email_verified"].(bool)
	text, _ := claims["email_verified"].(string)
	identity.EmailVerified = verified || text == "true"

	return identity
}
