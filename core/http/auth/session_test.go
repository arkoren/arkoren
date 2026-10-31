package auth_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"testing"
	"time"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/auth/authtest"
	"arkoren.dev/core/http/session"
)

// TestSessionConformance runs the authenticator suite against Session.
func TestSessionConformance(t *testing.T) {
	t.Parallel()

	anonymous := func(*testing.T) *http.Request {
		return sessionRequest(session.New(time.Now().Add(time.Hour), map[string]any{}))
	}

	loggedIn := func(*testing.T) *http.Request {
		sess := session.New(time.Now().Add(time.Hour), map[string]any{})

		if err := auth.NewSessionIdentity(loadByID).Login(sess, 1); err != nil {
			t.Fatal(err)
		}

		return sessionRequest(sess)
	}

	authtest.Run(t, auth.NewSessionIdentity(loadByID).Authenticator(), anonymous, loggedIn)
}

// TestSessionWrongLoginTypeFailsClosed verifies stale in-memory data cannot be
// coerced through JSON, even when its numeric representation is compatible.
func TestSessionWrongLoginTypeFailsClosed(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := current.Put(session.NewField[int64]("arkoren:auth:user"), int64(7)); err != nil {
		t.Fatal(err)
	}

	called := false
	identity := auth.NewSessionIdentity(func(context.Context, int) (string, error) {
		called = true

		return "unexpected", nil
	})

	_, err := identity.Authenticator()(sessionRequest(current))

	if !errors.Is(err, auth.ErrNoCredentials) || called {
		t.Fatalf("mismatched login: error = %v, loader called = %v", err, called)
	}
}

// TestSessionPersistedIdentifierMismatchFailsClosed verifies raw persisted
// JSON with a wrong identifier shape never reaches the user loader.
func TestSessionPersistedIdentifierMismatchFailsClosed(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), map[string]any{
		"arkoren:auth:user": jsontext.Value(`"private-user"`),
	})
	identity := auth.NewSessionIdentity(func(context.Context, int) (string, error) {
		t.Fatal("loader called for invalid persisted identifier")

		return "", nil
	})

	_, err := identity.Authenticator()(sessionRequest(current))

	if !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("persisted mismatch = %v", err)
	}
}
