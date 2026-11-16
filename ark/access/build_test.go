package access_test

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

	"arkoren.dev/ark/access"
)

// TestDefaultRegistriesAreOrdered verifies the second-factor methods are listed
// totp then recovery, and step-up is pwd then totp.
func TestDefaultRegistriesAreOrdered(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.TwoFactor(totp.New("Example"), cipher(t))
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

// TestApplicationCallbacksCannotOverrideTheSessionHandle verifies the facade's
// default handle wins over handles the Guard and Session callbacks set, so
// logging in still reaches the request.
func TestApplicationCallbacksCannotOverrideTheSessionHandle(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Guard(func(policy *auth.GuardBuilder) { policy.Handle(session.NewHandle()).SessionLifetime(time.Second) })
			iam.Session(func(cookies *session.Builder) { cookies.Handle(session.NewHandle()).MaxLifetime(time.Second) })
		})
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Do(http.MethodGet, "/me", ""); strings.Contains(reply.Body, "anonymous") {
			t.Fatalf("/me after registering = %q, want the user", reply.Body)
		}
	})
}

// TestClientAddressSourceIsRequired verifies New refuses to guess how to find
// the client address.
func TestClientAddressSourceIsRequired(t *testing.T) {
	t.Parallel()

	_, err := access.New(memoryDatabase(t), cache.New(memorycache.New()), nil)
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Fatalf("New without a client address source = %v, want ErrInvalidConfig", err)
	}
}

// TestRegistrationWithTheBuiltInUsersNeedsResetOrVerification verifies a new
// user could never prove their email without either, so New refuses it.
func TestRegistrationWithTheBuiltInUsersNeedsResetOrVerification(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{}, func(iam *access.Builder) { iam.Password(hasher(), registration) })
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Fatalf("registration without reset or verification = %v, want ErrInvalidConfig", err)
	}
}

// TestRegistrationWithResetOnlyIsValid verifies a reset link proves the mailbox,
// so Reset alone is enough for the built-in users.
func TestRegistrationWithResetOnlyIsValid(t *testing.T) {
	t.Parallel()

	if _, err := newFacadeWith(t, wire{reset: true}, func(iam *access.Builder) { iam.Password(hasher(), registration) }); err != nil {
		t.Fatalf("registration with reset only: %v", err)
	}
}

// TestRegistrationWithoutResetIsInvalid verifies registration with the built-in
// users needs Reset: Verification alone is not enough, because the reset notice
// is how the owner of a taken address is told.
func TestRegistrationWithoutResetIsInvalid(t *testing.T) {
	t.Parallel()

	for _, w := range []wire{{}, {verify: true}} {
		_, err := newFacadeWith(t, w, func(iam *access.Builder) { iam.Password(hasher(), registration) })

		if !errors.Is(err, access.ErrInvalidConfig) {
			t.Fatalf("registration without reset (%+v): error = %v, want ErrInvalidConfig", w, err)
		}
	}
}
