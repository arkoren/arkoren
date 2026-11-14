package provider

import (
	"errors"
	"strings"

	"arkoren.dev/core/auth"
)

// Microsoft applies tenant issuer, account-class, immutable-subject, and email
// verification policy only after the native token verifier has succeeded.
func Microsoft(identity auth.External, authority, tenant string) (auth.External, error) {
	tid, _ := identity.Claims["tid"].(string)
	oid, _ := identity.Claims["oid"].(string)

	switch {
	case tid == "" || oid == "":
		return auth.External{}, errors.New("Microsoft token lacks tid or oid")
	case identity.Issuer != authority+"/"+tid+"/v2.0":
		return auth.External{}, errors.New("Microsoft issuer does not match tid")
	case !microsoftAcceptsTenant(tenant, tid):
		return auth.External{}, errors.New("Microsoft tenant is not accepted")
	}

	identity.Subject = oid

	// Microsoft sends no email_verified claim: xms_edov is the only evidence.
	verified, _ := identity.Claims["xms_edov"].(bool)
	identity.EmailVerified = verified && identity.Email != ""

	if !identity.EmailVerified {
		identity.Email = ""
	}

	return identity, nil
}

// microsoftPersonalTenant is Microsoft's reserved tenant ID for personal
// account sign-ins. Organizational guest sign-ins carry the organization ID
// in tid instead, even when their home identity provider is a personal account.
const microsoftPersonalTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

// microsoftAcceptsTenant applies the configured authority's tenant policy to
// a verified, nonempty tid whose issuer has already been matched. Common accepts
// both account classes; the other aliases distinguish Microsoft's reserved
// personal tenant from organizational tenants. Explicit IDs keep their existing
// case-insensitive match, including the reserved personal-account ID itself.
func microsoftAcceptsTenant(tenant, tid string) bool {
	switch tenant {
	case "common":
		return true
	case "organizations":
		return !strings.EqualFold(tid, microsoftPersonalTenant)
	case "consumers":
		return strings.EqualFold(tid, microsoftPersonalTenant)
	default:
		return strings.EqualFold(tid, tenant)
	}
}
