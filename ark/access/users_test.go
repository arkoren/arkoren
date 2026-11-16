package access_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
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
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Users(access.Users{Provider: provider, Lookup: users.lookup, Create: users.create, Rotate: users.rotate,
				Verified:   func(context.Context, string) (bool, error) { return true, nil },
				ProveEmail: func(context.Context, string) (bool, error) { return false, nil },
			})
			iam.Password(hasher(), registration)
		})
		if sys.iam.Directory() != nil {
			t.Fatal("the built-in directory was built next to Users")
		}
		for _, definition := range sys.iam.Migrations() {
			if strings.Contains(definition.Group(), ":account") {
				t.Fatalf("a users table is migrated: %s", definition.ID())
			}
		}
		if err := sys.iam.Verifier().Set(ctx, "u1", "correct horse"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		device := sys.client.NewDevice()
		if reply := device.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("login = %d %s", reply.Status, reply.Body)
		}
		if reply := device.Do(http.MethodGet, "/me", ""); !strings.HasPrefix(reply.Body, "u1|1|") {
			t.Fatalf("/me = %q", reply.Body)
		}
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		sys.settle(t)
		token := sys.box.last(t, access.KindReset).Token
		sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if users.versions["u1"] != 1 {
			t.Fatalf("the reset rotated %d times, want once", users.versions["u1"])
		}
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Body != "anonymous" {
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
	if _, err := newFacade(t, func(iam *access.Builder) { iam.Users(access.Users{}) }); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("Users without a provider error = %v", err)
	}
	_, err := newFacade(t, func(iam *access.Builder) {
		iam.Users(access.Users{Provider: provider})
		iam.Password(hasher(), registration)
	})
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("Password without Lookup and Rotate error = %v", err)
	}
	iam, err := newFacade(t, func(iam *access.Builder) { iam.Users(access.Users{Provider: provider}) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !panics(func() { iam.DeleteAccountHandler() }) || !panics(func() { iam.RequireVerified() }) {
		t.Error("the directory-only handlers did not panic with the application's own users")
	}
}
