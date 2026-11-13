package auth_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/authtest"
)

// account is the application user of the typed driver tests.
type account struct {
	ID        int64
	Version   string
	Claims    map[string]any
	Suspended bool
}

// TestTypedUserDriver runs the shared conformance suite against an integer
// keyed TypedUserDriver.
func TestTypedUserDriver(t *testing.T) {
	t.Parallel()

	authtest.RunUserDriver(t, func(t *testing.T, users []authtest.User) auth.UserDriver {
		accounts := map[int64]*account{}

		for _, user := range users {
			id, err := strconv.ParseInt(user.Subject, 10, 64)

			if err != nil {
				t.Fatal(err)
			}

			accounts[id] = &account{ID: id, Version: user.Version, Claims: user.Claims, Suspended: user.Disabled}
		}

		driver, err := auth.NewUserDriver(
			func(_ context.Context, id int64) (*account, error) {
				if found, ok := accounts[id]; ok {
					return found, nil
				}

				return nil, auth.ErrUserNotFound
			},
			func(user *account) int64 { return user.ID },
			func(driver *auth.UserDriverBuilder[*account]) {
				driver.Version(func(user *account) string { return user.Version })
				driver.Claims(func(user *account) map[string]any { return user.Claims })
				driver.Disabled(func(user *account) bool { return user.Suspended })
			},
		)

		if err != nil {
			t.Fatal(err)
		}

		return driver
	})
}

// TestTypedUserDriverNilPointerIsNotFound verifies a loader returning a nil
// pointer and a nil error reports a missing user.
func TestTypedUserDriverNilPointerIsNotFound(t *testing.T) {
	t.Parallel()

	driver, err := auth.NewUserDriver(
		func(context.Context, int64) (*account, error) { return nil, nil },
		func(user *account) int64 { return user.ID },
	)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Find(t.Context(), "1"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestTypedUserDriverLoaderErrorPassesThrough verifies loader failures other
// than not found are returned unchanged and never look like deletion.
func TestTypedUserDriverLoaderErrorPassesThrough(t *testing.T) {
	t.Parallel()

	down := errors.New("db down")
	driver, err := auth.NewUserDriver(
		func(context.Context, int64) (account, error) { return account{}, down },
		func(user account) int64 { return user.ID },
	)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Find(t.Context(), "1"); !errors.Is(err, down) || errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestNewUserDriverRejectsNilFunctions verifies nil loader and id fail setup.
func TestNewUserDriverRejectsNilFunctions(t *testing.T) {
	t.Parallel()

	if _, err := auth.NewUserDriver[int64, account](nil, func(user account) int64 { return user.ID }); err == nil {
		t.Error("nil loader accepted")
	}

	if _, err := auth.NewUserDriver(func(context.Context, int64) (account, error) { return account{}, nil }, nil); err == nil {
		t.Error("nil id accepted")
	}
}
