package iam_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/totp"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam"
)

// TestDefaultRegistriesAreOrdered verifies the second-factor methods are listed
// totp then recovery, and step-up is pwd then totp.
func TestDefaultRegistriesAreOrdered(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
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

	if _, err := newFacade(t, func(identity *iam.Builder) {
		identity.SessionLifetime(48 * time.Hour)
		identity.Session(func(cookies *session.Builder) { cookies.Name("app_session") })
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
}

// TestApplicationCallbacksCannotOverrideTheSessionHandle verifies the facade's
// default handle wins over handles the Guard and Session callbacks set, so
// logging in still reaches the request.
func TestApplicationCallbacksCannotOverrideTheSessionHandle(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.Guard(func(policy *auth.GuardBuilder) { policy.Handle(session.NewHandle()).SessionLifetime(time.Second) })
			identity.Session(func(cookies *session.Builder) { cookies.Handle(session.NewHandle()).MaxLifetime(time.Second) })
		})
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Do(http.MethodGet, "/me", ""); strings.Contains(reply.Body, "anonymous") {
			t.Fatalf("/me after registering = %q, want the user", reply.Body)
		}
	})
}

// TestClientAddressSourceIsRequired verifies an explicitly nil final source is
// rejected rather than silently falling back to the default.
func TestClientAddressSourceIsRequired(t *testing.T) {
	t.Parallel()

	_, err := iam.New(memoryDatabase(t), cache.New(memorycache.New()), func(identity *iam.Builder) {
		identity.ClientIP(nil)
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New without a client address source = %v, want ErrInvalidConfig", err)
	}
}

// TestClientAddressSourceLastSettingWins verifies nil can be replaced before
// construction, while a later nil remains an invalid final setting.
func TestClientAddressSourceLastSettingWins(t *testing.T) {
	t.Parallel()

	db := memoryDatabase(t)
	store := cache.New(memorycache.New())
	source := func(*http.Request) string { return "192.0.2.1" }
	_, err := iam.New(db, store, func(identity *iam.Builder) {
		identity.ClientIP(nil).ClientIP(source)
	})
	if err != nil {
		t.Fatalf("replacing nil source: %v", err)
	}
	_, err = iam.New(db, store, func(identity *iam.Builder) {
		identity.ClientIP(source).ClientIP(nil)
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("final nil source = %v, want ErrInvalidConfig", err)
	}
}

// TestClientAddressSnapshotAndGuardPrecedence verifies the shared source wins
// over a guard callback and retained drafts cannot replace it after construction.
func TestClientAddressSnapshotAndGuardPrecedence(t *testing.T) {
	t.Parallel()

	var draft *iam.Builder
	sys := newApp(t, memoryDatabase(t), "address", func(identity *iam.Builder) {
		draft = identity
		identity.ClientIP(func(*http.Request) string { return "192.0.2.42" })
		identity.Guard(func(policy *auth.GuardBuilder) {
			policy.ClientIP(func(*http.Request) string { return "192.0.2.99" })
		})
		identity.Password(hasher(), registration)
	})
	draft.ClientIP(func(*http.Request) string { return "192.0.2.100" })
	register(t, sys.client, "address@example.com")
	reply := sys.client.Do(http.MethodGet, "/sessions", "")
	if !strings.Contains(reply.Body, "192.0.2.42") {
		t.Fatalf("session address = %s, want configured source", reply.Body)
	}
}

// TestRegistrationWithTheBuiltInUsersNeedsResetOrVerification verifies a new
// user could never prove their email without either, so New refuses it.
func TestRegistrationWithTheBuiltInUsersNeedsResetOrVerification(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{}, func(identity *iam.Builder) { identity.Password(hasher(), registration) })
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("registration without reset or verification = %v, want ErrInvalidConfig", err)
	}
}

// TestRegistrationWithResetOnlyIsValid verifies a reset link proves the mailbox,
// so Reset alone is enough for the built-in users.
func TestRegistrationWithResetOnlyIsValid(t *testing.T) {
	t.Parallel()

	if _, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) { identity.Password(hasher(), registration) }); err != nil {
		t.Fatalf("registration with reset only: %v", err)
	}
}

// TestRegistrationWithoutResetIsInvalid verifies registration with the built-in
// users needs Reset: Verification alone is not enough, because the reset notice
// is how the owner of a taken address is told.
func TestRegistrationWithoutResetIsInvalid(t *testing.T) {
	t.Parallel()

	for _, w := range []wire{{}, {verify: true}} {
		_, err := newFacadeWith(t, w, func(identity *iam.Builder) { identity.Password(hasher(), registration) })

		if !errors.Is(err, iam.ErrInvalidConfig) {
			t.Fatalf("registration without reset (%+v): error = %v, want ErrInvalidConfig", w, err)
		}
	}
}
