package oauthauth

import (
	"arkoren.dev/driver/oauth/oauthauth/internal/provider"
)

// microsoftAuthority is the base URL of the Microsoft identity platform.
const microsoftAuthority = "https://login.microsoftonline.com"

// Microsoft returns the Microsoft identity platform (Entra ID) provider for
// tenant: an explicit tenant ID, "common" for both organizational and personal
// tenants, "organizations" for organizational tenants only, or "consumers" for
// the personal-account tenant 9188040d-6c67-4c5b-b112-36a304b66dad only. These
// restrictions apply to both browser callbacks and bearer verification, using
// the verified tid claim, not the home identity provider (idp): a personal
// account invited into an organization retains that organization's tenant
// context. Explicit tenant IDs are matched case-insensitively. Tenant names
// such as "contoso.onmicrosoft.com" are not supported for single-tenant use.
// It requests the scopes openid, profile and email.
//
// The issuer of every token is checked against its "tid" claim. Identity.Issuer
// is the issuer of the user's tenant and Identity.Subject is the immutable
// "oid" claim, so Issuer + Subject identify the user within its tenant. Tokens
// without "tid" or "oid" are rejected. The email claim is mutable and not
// verified by default: Identity.Email is set only when the token carries the
// optional claim "xms_edov" (email domain owner verified) as boolean true, so
// configure that optional claim in the app registration to receive emails.
// Without it Identity.Email is empty, which prevents the "nOAuth" account
// takeover through an unverified, attacker-controlled address. Match users on
// Issuer + Subject, not on email.
//
// Example:
//
//	client, err := oauth.New(oauth.Microsoft("common"), cipher, id, func(client *oauth.ClientBuilder) {
//		client.ClientSecret(secret)
//	})
//	if err != nil {
//		return err
//	}
//	_ = client
func Microsoft(tenant string) Provider {
	return microsoft(microsoftAuthority+"/"+tenant+"/v2.0", microsoftAuthority, tenant)
}

// microsoft builds the [Microsoft] provider discovered at issuer, whose
// tenant issuers start with authority. Tests may replace both URLs. Its identity
// policy runs after signature, expiry and audience verification, then validates
// the tenant issuer and chosen account class before exposing the oid subject.
func microsoft(issuer, authority, tenant string) Provider {
	return Provider{
		issuer: issuer,
		scopes: []string{"openid", "profile", "email"},
		identity: func(identity Identity) (Identity, error) {
			checked, err := provider.Microsoft(provider.Record(identity), authority, tenant)

			return Identity(checked), err
		},
	}
}
