package provider

import "errors"

// Google applies the preset's issuer, Workspace, and verified-email policy to
// an identity whose signature, expiry, and audience have already been checked.
func Google(identity Record, issuer, bare, domain string) (Record, error) {
	if identity.Issuer != issuer && identity.Issuer != bare {
		return Record{}, errors.New("Google issuer is not accepted")
	}

	if domain != "" {
		if hd, _ := identity.Claims["hd"].(string); hd != domain {
			return Record{}, errors.New("Google hosted domain is not accepted")
		}
	}

	if verified, _ := identity.Claims["email_verified"].(bool); !verified {
		identity.Email = ""
	}

	identity.Issuer = issuer

	return identity, nil
}
