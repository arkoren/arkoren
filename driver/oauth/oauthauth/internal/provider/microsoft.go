package provider

import (
	"errors"
	"strings"
)

// Microsoft applies tenant issuer, account-class, immutable-subject, and email
// verification policy only after the native token verifier has succeeded.
func Microsoft(identity Record, authority, tenant string) (Record, error) {
	tid, _ := identity.Claims["tid"].(string)
	oid, _ := identity.Claims["oid"].(string)

	switch {
	case tid == "" || oid == "":
		return Record{}, errors.New("Microsoft token lacks tid or oid")
	case identity.Issuer != authority+"/"+tid+"/v2.0":
		return Record{}, errors.New("Microsoft issuer does not match tid")
	case !microsoftAcceptsTenant(tenant, tid):
		return Record{}, errors.New("Microsoft tenant is not accepted")
	}

	identity.Subject = oid

	if verified, _ := identity.Claims["xms_edov"].(bool); !verified {
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
