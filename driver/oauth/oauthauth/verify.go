package oauthauth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"arkoren.dev/driver/oauth/oauthauth/internal/provider"
)

// Verify checks a JWT's issuer, audience, signature, expiry and nonempty final
// subject, then returns its [Identity]. It rejects a nonce claim or typ=ID claim
// as an ID-token heuristic, not a universal proof of access-token purpose. An
// unmarked JWT matching the accepted audience can pass. Applications must enforce
// provider-specific purpose policy where needed; do not rely on overlapping
// browser-client and API audiences to distinguish token purpose. OAuth2
// providers return [ErrUnsupported]. The accepted audiences must be set with
// [ClientBuilder.Audiences]; without them Verify returns [ErrNoAudiences], because the
// default audience of an ID token is the client ID and would make ID tokens
// pass as bearer tokens. Errors are plain; [auth.Bearer] turns them into a
// 401 challenge.
// The error returned by the native verifier remains in the chain. A failing
// lookup also retains context cancellation/deadline identity when it observes
// either condition. Native diagnostics can contain claims; do not log them
// indiscriminately. TokenRejected carries only a fixed opaque classification
// and no native cause.
//
// Example:
//
//	// client was built with ClientBuilder.Audiences("https://api.example.com").
//	r.Use(auth.Middleware(auth.Bearer(client.Verify)))
func (client *Client) Verify(ctx context.Context, token string) (Identity, error) {
	if !client.provider.isOIDC() {
		return Identity{}, ErrUnsupported
	}

	if len(client.config.Audiences) == 0 {
		return Identity{}, ErrNoAudiences
	}

	identity, err := client.verify(ctx, token, client.config.Audiences)

	if err != nil {
		client.config.Events.Emit(ctx, TokenRejected, TokenRejectedEvent{
			Err: errBearerRejected,
		})

		return Identity{}, err
	}

	// Reject common ID-token markers. Unmarked JWTs still require distinct
	// API audiences and any provider-specific application purpose policy.
	typ, _ := identity.Claims["typ"].(string)

	if _, nonce := identity.Claims["nonce"]; nonce || strings.EqualFold(typ, "ID") {
		err := errors.New("OIDC ID token used as bearer token")
		client.config.Events.Emit(ctx, TokenRejected, TokenRejectedEvent{
			Err: errBearerRejected,
		})

		return Identity{}, err
	}

	return identity, nil
}

// verify checks raw's signature, expiry, issuer and membership in audiences,
// then returns its provider-checked identity. Callers select the accepted
// audiences explicitly: the client ID for browser ID tokens, or configured API
// audiences for bearer tokens. Empty audiences accept no token. Verification
// and provider identity errors retain their causes for the caller to classify.
func (client *Client) verify(ctx context.Context, raw string, audiences []string) (Identity, error) {
	ready, err := client.started()

	if err != nil {
		return Identity{}, err
	}

	token, err := ready.verifier.Verify(ctx, raw)

	if err != nil {
		// The native verifier formats some failures without wrapping their
		// causes. Retain its error and restore the caller's context identity.
		return Identity{}, fmt.Errorf("verify OIDC token: %w", errors.Join(err, ctx.Err()))
	}

	if !slices.ContainsFunc(token.Audience, func(audience string) bool {
		return slices.Contains(audiences, audience)
	}) {
		return Identity{}, errors.New("OIDC token audience is not accepted")
	}

	claims := make(map[string]any)

	if err := token.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("decode OIDC claims: %w", err)
	}

	identity := Identity(provider.FromClaims(claims))

	if check := client.provider.identity; check != nil {
		identity, err = check(identity)

		if err != nil {
			return Identity{}, fmt.Errorf("check OIDC identity: %w", err)
		}
	}

	if identity.Subject == "" {
		return Identity{}, errors.New("OIDC identity lacks a stable subject")
	}

	return identity, nil
}
