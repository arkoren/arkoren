package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/auth/authtest"
)

// newRequest returns a GET request without credentials.
func newRequest(*testing.T) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/", nil)
}

// authorizationRequest returns a factory for a request carrying authorization as its
// Authorization header.
func authorizationRequest(authorization string) func(*testing.T) *http.Request {
	return func(*testing.T) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", authorization)

		return r
	}
}

// verifyToken accepts the token "good" as user "u".
func verifyToken(_ context.Context, token string) (user, error) {
	if token != "good" {
		return user{}, errors.New("unknown token")
	}

	return user{"u"}, nil
}

// TestBearerConformance runs the authenticator suite against Bearer.
func TestBearerConformance(t *testing.T) {
	t.Parallel()

	authtest.Run(t, auth.Bearer(verifyToken), newRequest, authorizationRequest("Bearer good"),
		authtest.WithInvalid[user](authorizationRequest("Bearer forged")),
		authtest.WithUser(func(t *testing.T, got user) {
			if got.Name != "u" {
				t.Fatalf("user = %+v, want u", got)
			}
		}))
}
