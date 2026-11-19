package iam_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/account"
)

// ownUsers is the application's own user store, as the facade sees it.
type ownUsers struct {
	mu       sync.Mutex
	driver   *memoryauth.Driver
	subjects map[string]string
	versions map[string]int
}

// newOwnUsers returns a store with one user, "ada@example.com".
func newOwnUsers(t *testing.T) *ownUsers {
	t.Helper()

	users := &ownUsers{driver: memoryauth.New(), subjects: map[string]string{}, versions: map[string]int{}}
	if _, err := users.create(context.Background(), "ada@example.com", true); err != nil {
		t.Fatalf("create: %v", err)
	}

	return users
}

// create adds a user.
func (users *ownUsers) create(_ context.Context, identifier string, _ bool) (string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	subject := "u" + strconv.Itoa(len(users.subjects)+1)
	users.subjects[identifier] = subject

	return subject, users.driver.Put(auth.UserRecord{Subject: subject, Version: "0"})
}

// lookup finds a user by identifier.
func (users *ownUsers) lookup(_ context.Context, identifier string) (string, string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	subject, ok := users.subjects[identifier]
	if !ok {
		return "", "", fmt.Errorf("own users: %w", auth.ErrUserNotFound)
	}

	return subject, identifier, nil
}

// rotate bumps a user's version.
func (users *ownUsers) rotate(_ context.Context, subject string) error {
	users.mu.Lock()
	defer users.mu.Unlock()

	users.versions[subject]++

	return users.driver.Put(auth.UserRecord{Subject: subject, Version: strconv.Itoa(users.versions[subject])})
}

// TestOwnUsersPasswordLoginAndReset verifies the facade works over the
// application's own users: no users table, login, and a reset that rotates.
func TestOwnUsersPasswordLoginAndReset(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		users := newOwnUsers(t)
		provider, err := auth.NewUserProvider(users.driver)
		if err != nil {
			t.Fatalf("NewUserProvider: %v", err)
		}
		sys := newAppWith(t, db, instance, wire{reset: true}, func(identity *iam.Builder) {
			identity.Users(iam.Users{Provider: provider, Lookup: users.lookup, Create: users.create, Rotate: users.rotate,
				Verified:   func(context.Context, string) (bool, error) { return true, nil },
				ProveEmail: func(context.Context, string) (bool, error) { return false, nil },
			})
			identity.Password(hasher(), registration)
		})
		if sys.identity.Directory() != nil {
			t.Fatal("the built-in directory was built next to Users")
		}
		for _, definition := range sys.identity.Migrations() {
			if strings.Contains(definition.Group(), ":account") {
				t.Fatalf("a users table is migrated: %s", definition.ID())
			}
		}
		if err := sys.identity.Verifier().Set(ctx, "u1", "correct horse"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		device := sys.client.NewDevice()
		if reply := device.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("login = %d %s", reply.Status, reply.Body)
		}
		if reply := device.Do(http.MethodGet, "/whoami", ""); !strings.HasPrefix(reply.Body, "u1|1|") {
			t.Fatalf("/me = %q", reply.Body)
		}
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		token := sys.box.lastReset(t).Signed
		sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if users.versions["u1"] != 1 {
			t.Fatalf("the reset rotated %d times, want once", users.versions["u1"])
		}
		if reply := device.Do(http.MethodGet, "/whoami", ""); reply.Body != "anonymous" {
			t.Fatalf("the old login survived the rotation: %q", reply.Body)
		}
		for _, route := range [][2]string{{"POST", "/email/verify"}, {"POST", "/email/verify/resend"}, {"DELETE", "/account"}} {
			if sys.router.Has(route[0], route[1]) {
				t.Errorf("%s %s is mounted without the built-in users", route[0], route[1])
			}
		}
	})
}

// TestOwnUsersValidation verifies the Users requirements.
func TestOwnUsersValidation(t *testing.T) {
	t.Parallel()

	users := newOwnUsers(t)
	provider, _ := auth.NewUserProvider(users.driver)
	if _, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) { identity.Users(iam.Users{}) }); !errors.Is(err, iam.ErrInvalidConfig) {
		t.Errorf("Users without a provider error = %v", err)
	}
	_, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) {
		identity.Users(iam.Users{Provider: provider})
		identity.Password(hasher(), registration)
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Errorf("Password without Lookup and Rotate error = %v", err)
	}
	identity, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) { identity.Users(iam.Users{Provider: provider}) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !panics(func() { identity.DeleteAccountHandler() }) || !panics(func() { identity.RequireVerified() }) {
		t.Error("the directory-only handlers did not panic with the application's own users")
	}
}

// TestRequireVerifiedUsesTheApplicationsVerified verifies RequireVerified works
// with the application's own users: it asks Users.Verified for each request,
// admits a verified user, answers 403 to an unverified one and 401 to nobody.
func TestRequireVerifiedUsesTheApplicationsVerified(t *testing.T) {
	t.Parallel()

	users := newOwnUsers(t)
	provider, _ := auth.NewUserProvider(users.driver)
	identity, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) {
		identity.Users(iam.Users{
			Provider:   provider,
			Verified:   func(_ context.Context, subject string) (bool, error) { return subject == "u1", nil },
			ProveEmail: func(context.Context, string) (bool, error) { return false, nil },
		})
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	guarded := identity.RequireVerified()(func(http.ResponseWriter, *http.Request) error { return nil })
	serve := func(subject string) error {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		if subject != "" {
			request = request.WithContext(auth.NewContext(request.Context(), auth.NewIdentity(subject)))
		}

		return guarded(httptest.NewRecorder(), request)
	}
	if err := serve("u1"); err != nil {
		t.Errorf("verified user = %v, want admitted", err)
	}
	if err := serve("u2"); !errors.Is(err, account.ErrEmailUnverified) {
		t.Errorf("unverified user = %v, want ErrEmailUnverified", err)
	}
	if err := serve(""); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("anonymous = %v, want ErrUnauthenticated", err)
	}
}
