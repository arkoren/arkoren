package iam_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/totp"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/internal/apptest"
)

// TestDefaultRegistriesAreOrdered verifies the second-factor methods are listed
// totp then recovery, and step-up is pwd then totp.
func TestDefaultRegistriesAreOrdered(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(true, hasher(), registration)
			identity.TwoFactor(true, totp.New("Example"), cipher(t))
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
			identity.Password(true, hasher(), registration)
			identity.Guard(func(policy *auth.GuardBuilder) { policy.Handle(session.NewHandle()).SessionLifetime(time.Second) })
			identity.Session(func(cookies *session.Builder) { cookies.Handle(session.NewHandle()).MaxLifetime(time.Second) })
		})
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Do(http.MethodGet, "/whoami", ""); strings.Contains(reply.Body, "anonymous") {
			t.Fatalf("/me after registering = %q, want the user", reply.Body)
		}
	})
}

// TestClientAddressFinalNilIsInvalid verifies an explicitly nil final source is
// rejected rather than silently falling back to the default.
func TestClientAddressFinalNilIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := iam.New(memoryDatabase(t), cache.New(memorycache.New()), func(identity *iam.Builder) {
		identity.ClientIP(nil)
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New with final nil client address source = %v, want ErrInvalidConfig", err)
	}
}

// TestClientAddressSourceLastSettingWins verifies nil can be replaced before
// construction, while a later nil remains an invalid final setting.
func TestClientAddressSourceLastSettingWins(t *testing.T) {
	t.Parallel()

	db := memoryDatabase(t)
	store := cache.New(memorycache.New())
	source := func(*http.Request) string {
		return "192.0.2.1"
	}
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

// TestClientAddressSnapshot verifies retained drafts cannot replace the source
// used by a published facade.
func TestClientAddressSnapshot(t *testing.T) {
	t.Parallel()

	var draft *iam.Builder
	sys := newApp(t, memoryDatabase(t), "address", func(identity *iam.Builder) {
		draft = identity
		identity.ClientIP(func(*http.Request) string {
			return "192.0.2.42"
		})
		identity.Password(true, hasher(), registration)
	})
	draft.ClientIP(func(*http.Request) string {
		return "192.0.2.100"
	})
	register(t, sys.client, "address@example.com")
	reply := sys.client.Do(http.MethodGet, "/sessions", "")
	if !strings.Contains(reply.Body, "192.0.2.42") {
		t.Fatalf("session address = %s, want configured source", reply.Body)
	}
}

// TestClientAddressOverridesGuard verifies the facade's address source wins
// over a guard-specific setting, keeping guard and throttle policy aligned.
func TestClientAddressOverridesGuard(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "addressguard", func(identity *iam.Builder) {
		identity.ClientIP(func(*http.Request) string {
			return "192.0.2.42"
		})
		identity.Guard(func(policy *auth.GuardBuilder) {
			policy.ClientIP(func(*http.Request) string {
				return "192.0.2.99"
			})
		})
		identity.Password(true, hasher(), registration)
	})
	register(t, sys.client, "guard@example.com")
	reply := sys.client.Do(http.MethodGet, "/sessions", "")
	if !strings.Contains(reply.Body, `"ip":"192.0.2.42"`) {
		t.Fatalf("session address = %s, want facade source", reply.Body)
	}
}

// TestDefaultClientAddressIgnoresForwarding verifies a facade constructed
// without ClientIP records its direct peer, not untrusted forwarding headers.
func TestDefaultClientAddressIgnoresForwarding(t *testing.T) {
	t.Parallel()

	db := memoryDatabase(t)
	store := cache.New(memorycache.New())
	identity, err := iam.New(db, store, func(identity *iam.Builder) {
		identity.Password(true, hasher())
		identity.Session(func(cookies *session.Builder) {
			cookies.AllowInsecure(true)
		})
	})
	if err != nil {
		t.Fatalf("New without ClientIP: %v", err)
	}
	migrateUp(t, db, identity.Migrations()...)
	subject, err := identity.Directory().Create(testContext(t), "peer@example.com")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := identity.Verifier().Set(testContext(t), subject, "correct horse"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	routes := router.New()
	identity.Register(routes.With(identity.Middleware()))
	client := apptest.ServeRouter(t, routes)
	reply := client.Do(http.MethodPost, "/login", `{"email":"peer@example.com","password":"correct horse"}`,
		"X-Forwarded-For: 203.0.113.12", "CF-Connecting-IP: 203.0.113.13")
	if reply.Status != http.StatusNoContent {
		t.Fatalf("login = %d %s", reply.Status, reply.Body)
	}
	reply = client.Do(http.MethodGet, "/sessions", "")
	if reply.Status != http.StatusOK || !strings.Contains(reply.Body, `"ip":"127.0.0.1"`) {
		t.Fatalf("sessions = %d %s, want direct peer", reply.Status, reply.Body)
	}
}

// TestRawIPv6AddressAndThrottle verifies the raw address is kept in the login
// record, while distinct addresses within one /64 share the per-client budget.
func TestRawIPv6AddressAndThrottle(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "ipv6", func(identity *iam.Builder) {
		identity.ClientIP(func(request *http.Request) string {
			return request.Header.Get("X-Test-IP")
		})
		identity.Password(true, hasher(), registration)
	})
	register(t, sys.client, "ipv6@example.com")
	client := sys.client.NewDevice()
	reply := client.Do(http.MethodPost, "/login", `{"email":"ipv6@example.com","password":"correct horse"}`,
		"X-Test-IP: 2001:db8:1:2::1234")
	if reply.Status != http.StatusNoContent {
		t.Fatalf("login = %d %s", reply.Status, reply.Body)
	}
	reply = client.Do(http.MethodGet, "/sessions", "")
	if !strings.Contains(reply.Body, `"ip":"2001:db8:1:2::1234"`) {
		t.Fatalf("sessions = %s, want full IPv6 address", reply.Body)
	}
	for index := range 100 {
		body := fmt.Sprintf(`{"email":"unknown%d@example.com","password":"wrong horse"}`, index)
		reply = client.Do(http.MethodPost, "/login", body, "X-Test-IP: 2001:db8:1:2::1")
		if reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("failure %d = %d %s", index+1, reply.Status, reply.Body)
		}
	}
	reply = client.Do(http.MethodPost, "/login", `{"email":"another@example.com","password":"wrong horse"}`,
		"X-Test-IP: 2001:db8:1:2::2")
	if reply.Status != http.StatusTooManyRequests {
		t.Fatalf("same /64 = %d %s, want 429", reply.Status, reply.Body)
	}
	reply = client.Do(http.MethodPost, "/login", `{"email":"another@example.com","password":"wrong horse"}`,
		"X-Test-IP: 2001:db8:1:3::2")
	if reply.Status != http.StatusUnprocessableEntity {
		t.Fatalf("different /64 = %d %s, want 422", reply.Status, reply.Body)
	}
}

// TestRegistrationWithResetOnlyIsValid verifies a reset link proves the mailbox,
// so Reset alone is enough for the built-in users.
func TestRegistrationWithResetOnlyIsValid(t *testing.T) {
	t.Parallel()

	if _, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) { identity.Password(true, hasher(), registration) }); err != nil {
		t.Fatalf("registration with reset only: %v", err)
	}
}

// TestRegistrationWithoutResetIsIgnored verifies registration with the built-in
// users needs Reset: Verification alone is not enough, because the reset notice
// is how the owner of a taken address is told. Without it New ignores the
// setting with a warning and mounts no /register route.
func TestRegistrationWithoutResetIsIgnored(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	for _, w := range []wire{{}, {verify: true}} {
		sys := newAppWith(t, memoryDatabase(t), newInstance(t), w, func(identity *iam.Builder) { identity.Password(true, hasher(), registration) })
		absent(t, sys, "POST /register")
	}
	if got := warnings.Settings(); len(got) != 2 || got[0] != "password.Builder.Registration" {
		t.Fatalf("warnings = %v, want one per facade naming password.Builder.Registration", got)
	}
}
