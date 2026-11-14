package oauthauth

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"arkoren.dev/ark/oauth/oauthauth/internal/provider"
	"arkoren.dev/foundation/key"
)

// appleIssuer is the Apple ID issuer and the audience of its client secrets.
const appleIssuer = "https://appleid.apple.com"

// Apple returns a Sign in with Apple provider. teamID and keyID identify the
// developer account and the key; identity is a key.Signing loaded from Apple's
// downloaded ".p8" file with key.LoadSigning or key.ParseSigning. The client ID is the Services
// ID, and no client secret is configured: the provider signs a short-lived
// ES256 JWT for every code exchange. [New] rejects an invalid key.
//
// Apple sends the callback as a cross-site POST form, so the login cookie
// uses SameSite=None. Register the callback outside csrf.Middleware, or pass
// csrf.Builder.Protection with an http.CrossOriginProtection that calls
// AddInsecureBypassPattern("POST /auth/apple/callback"); the state and the
// login cookie are the CSRF defense of this route.
//
// Apple sends the user's name only on the first authorization, in the form
// field "user"; it is added as Claims["name"] of the [auth.External] on that login only,
// so store it then. The email may be a private relay address. PKCE is off.
//
// Apple supports native login ([Client.NativeHandler]) for the app's bundle ID.
// Its ID token carries the nonce exactly as the app sent it to Apple, so the app
// sends the lowercase hexadecimal SHA-256 of the nonce from [Client.NonceHandler].
// The client name is "apple".
//
// Example:
//
//	identity, err := key.LoadSigning("AuthKey_ABC123DEFG.p8")
//	if err != nil {
//		return err
//	}
//
//	client, err := oauth.New(oauth.Apple("TEAM123456", "ABC123DEFG", identity), cipher,
//		"com.example.web",
//		func(client *oauth.ClientBuilder) {
//			client.RedirectURL("https://app.example.com/auth/apple/callback")
//		},
//	)
func Apple(teamID, keyID string, identity key.Signing) Provider {
	privateKey, keyErr := identity.PrivateKey(), identity.Err()

	definition := OIDC(appleIssuer)
	definition.name = "apple"
	definition.scopes = []string{"openid", "name", "email"}
	definition.formPost = true
	definition.noPKCE = true
	definition.formIdentity = provider.AppleName
	definition.nativeNonce = func(nonce string) string {
		digest := sha256.Sum256([]byte(nonce))

		return hex.EncodeToString(digest[:])
	}

	definition.clientSecret = func(clientID, _ string) (string, error) {
		if keyErr != nil {
			return "", keyErr
		}

		return provider.AppleSecret(privateKey, teamID, keyID, clientID, appleIssuer, time.Now())
	}

	return definition
}
