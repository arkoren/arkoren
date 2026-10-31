package provider

import (
	"crypto/ecdsa"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// appleSecretTTL is the lifetime of a generated client secret. Apple allows
// up to six months; one is generated per code exchange.
const appleSecretTTL = 5 * time.Minute

// AppleSecret signs the client secret JWT valid from now.
func AppleSecret(key *ecdsa.PrivateKey, teamID, keyID, clientID, issuer string, now time.Time) (string, error) {
	signer, err := jose.NewSigner(
		jose.SigningKey{
			Algorithm: jose.ES256,
			Key: jose.JSONWebKey{
				Key:   key,
				KeyID: keyID,
			},
		},
		(&jose.SignerOptions{}).WithType("JWT"),
	)

	if err != nil {
		return "", fmt.Errorf("create Apple signer: %w", err)
	}

	secret, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   teamID,
		Subject:  clientID,
		Audience: jwt.Audience{issuer},
		IssuedAt: jwt.NewNumericDate(now),
		Expiry:   jwt.NewNumericDate(now.Add(appleSecretTTL)),
	}).Serialize()

	if err != nil {
		return "", fmt.Errorf("sign Apple client secret: %w", err)
	}

	return secret, nil
}

// AppleName adds the name from the "user" form field of the first
// authorization. The field is not signed, so only the name is taken from it,
// never the email.
func AppleName(request *http.Request, identity Record) Record {
	var user struct {
		// Name is the unsigned first-authorization display-name payload.
		Name struct {
			// FirstName is display data only, never a verified identity claim.
			FirstName string `json:"firstName"`
			// LastName completes the display name supplied by the browser.
			LastName string `json:"lastName"`
		} `json:"name"`
	}

	if err := json.Unmarshal([]byte(request.PostFormValue("user")), &user); err != nil {
		return identity
	}

	if name := strings.TrimSpace(user.Name.FirstName + " " + user.Name.LastName); name != "" {
		identity.Name = name
	}

	return identity
}
