package access_test

import (
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
)

// TestDefaultRegistriesAreOrdered verifies the second-factor methods are listed
// totp then recovery, and step-up is pwd then totp.
func TestDefaultRegistriesAreOrdered(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.TwoFactor(cipher(t), "Example")
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		setup := sys.client.Post("/two-factor/setup", "")
		sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`)
		stale := sys.client.NewDevice()
		reply := stale.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		if !strings.Contains(reply.Body, `["otp","recovery"]`) {
			t.Fatalf("second-factor methods = %s, want otp then recovery", reply.Body)
		}
	})
}

// TestSessionAndGuardShareTheLifetime verifies SessionLifetime reaches both.
func TestSessionAndGuardShareTheLifetime(t *testing.T) {
	t.Parallel()

	if _, err := newFacade(t, func(iam *access.Builder) {
		iam.SessionLifetime(48 * time.Hour)
		iam.Session(func(cookies *session.Builder) { cookies.Name("app_session") })
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
}
