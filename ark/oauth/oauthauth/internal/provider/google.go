package provider

import (
	"errors"

	"arkoren.dev/core/auth"
)

// Google applies the preset's issuer, Workspace, and verified-email policy to
// an identity whose signature, expiry, and audience have already been checked.
func Google(identity auth.External, issuer, bare, domain string) (auth.External, error) {
	if identity.Issuer != issuer && identity.Issuer != bare {
		return auth.External{}, errors.New("Google issuer is not accepted")
	}

	if domain != "" {
		if hd, _ := identity.Claims["hd"].(string); hd != domain {
			return auth.External{}, errors.New("Google hosted domain is not accepted")
		}
	}

	// Google documents email_verified as a boolean; accept nothing else.
	verified, _ := identity.Claims["email_verified"].(bool)
	identity.EmailVerified = verified

	if !verified {
		identity.Email = ""
	}

	identity.Issuer = issuer

	return identity, nil
}
