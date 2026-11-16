package provider_test

import (
	"testing"

	"arkoren.dev/ark/oauth/oauthauth/internal/provider"
)

// TestAppleAcceptsBooleanAndStringVerified verifies Apple's email_verified is
// accepted as the boolean true and as the string "true".
func TestAppleAcceptsBooleanAndStringVerified(t *testing.T) {
	t.Parallel()

	for _, verified := range []any{true, "true"} {
		identity := provider.Apple(map[string]any{"email": "a@privaterelay.appleid.com", "email_verified": verified})

		if !identity.EmailVerified || identity.Email == "" {
			t.Errorf("email_verified %v: identity = %+v, want verified", verified, identity)
		}
	}
}

// TestAppleRejectsOtherVerifiedValues verifies a false, "false" or missing
// email_verified leaves the email unverified.
func TestAppleRejectsOtherVerifiedValues(t *testing.T) {
	t.Parallel()

	for _, verified := range []any{false, "false", "yes", nil} {
		identity := provider.Apple(map[string]any{"email": "a@example.com", "email_verified": verified})

		if identity.EmailVerified {
			t.Errorf("email_verified %v: reported verified", verified)
		}
	}
}
