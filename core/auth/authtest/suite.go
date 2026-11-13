package authtest

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
)

// defaultTimeout bounds the context of each subtest unless [WithTimeout]
// overrides it.
const defaultTimeout = 30 * time.Second

const (
	// foundSubject names the enabled fixture user.
	foundSubject = "1"

	// disabledSubject names the disabled fixture user.
	disabledSubject = "2"

	// missingSubject is a valid subject no fixture defines.
	missingSubject = "3"
)

// User is a fixture the suite asks a driver under test to contain.
//
// Example:
//
//	authtest.User{Subject: "1", Version: "v1", Claims: map[string]any{"role": "admin"}}
type User struct {
	// Subject identifies the user. Fixture subjects are decimal digits.
	Subject string

	// Version must come back as [auth.UserRecord.Version].
	Version string

	// Claims must come back as [auth.UserRecord.Claims].
	Claims map[string]any

	// Disabled users must make Find return [auth.ErrUserDisabled].
	Disabled bool
}

// settings holds the suite configuration built from [Option] values.
type settings struct {
	// timeout bounds the context handed to each subtest.
	timeout time.Duration
}

// Option configures [RunUserDriver]; create one with [WithTimeout].
//
// Example:
//
//	option := authtest.WithTimeout(time.Minute)
//	authtest.RunUserDriver(t, newDriver, option)
type Option func(*settings)

// WithTimeout bounds every subtest's context, for slow remote drivers. A zero
// or negative timeout keeps the default of 30 seconds.
//
// Example:
//
//	authtest.RunUserDriver(t, newDriver, authtest.WithTimeout(time.Minute))
func WithTimeout(timeout time.Duration) Option {
	return func(config *settings) {
		if timeout > 0 {
			config.timeout = timeout
		}
	}
}

// RunUserDriver executes the conformance suite against drivers returned by
// newDriver. newDriver is called once per subtest with the fixtures the driver
// must contain (one enabled user with a version and claims, one disabled user)
// and registers any cleanup on t. The suite reports every deviation with
// t.Errorf or t.Fatalf in a subtest named after the behavior.
//
// Example:
//
//	func TestMemoryDriver(t *testing.T) {
//		authtest.RunUserDriver(t, func(t *testing.T, users []authtest.User) auth.UserDriver {
//			driver := memoryauth.New()
//			for _, user := range users {
//				_ = driver.Put(auth.UserRecord{Subject: user.Subject, Version: user.Version, Claims: user.Claims})
//				if user.Disabled {
//					_ = driver.Disable(user.Subject)
//				}
//			}
//			return driver
//		})
//	}
func RunUserDriver(t *testing.T, newDriver func(t *testing.T, users []User) auth.UserDriver, opts ...Option) {
	t.Helper()

	config := settings{timeout: defaultTimeout}

	for _, opt := range opts {
		opt(&config)
	}

	claims := map[string]any{"role": "admin"}
	fixtures := []User{
		{Subject: foundSubject, Version: "v7", Claims: claims},
		{Subject: disabledSubject, Version: "v1", Disabled: true},
	}

	run := func(name string, test func(t *testing.T, driver auth.UserDriver)) {
		t.Run(name, func(t *testing.T) {
			driver := newDriver(t, fixtures)

			test(t, driver)
		})
	}

	find := func(t *testing.T, driver auth.UserDriver, subject string) (auth.UserRecord, error) {
		t.Helper()

		ctx, cancel := context.WithTimeout(t.Context(), config.timeout)
		defer cancel()

		return driver.Find(ctx, subject)
	}

	run("Found", func(t *testing.T, driver auth.UserDriver) {
		record, err := find(t, driver, foundSubject)

		if err != nil {
			t.Fatalf("Find(%q) = %v", foundSubject, err)
		}

		if record.Subject != foundSubject {
			t.Errorf("Subject = %q, want %q", record.Subject, foundSubject)
		}
	})

	run("VersionPassthrough", func(t *testing.T, driver auth.UserDriver) {
		record, err := find(t, driver, foundSubject)

		if err != nil || record.Version != "v7" {
			t.Fatalf("Find(%q) = version %q, %v, want v7", foundSubject, record.Version, err)
		}
	})

	run("ClaimsPassthrough", func(t *testing.T, driver auth.UserDriver) {
		record, err := find(t, driver, foundSubject)

		if err != nil || !maps.Equal(record.Claims, claims) {
			t.Fatalf("Find(%q) = claims %v, %v, want %v", foundSubject, record.Claims, err, claims)
		}
	})

	run("NotFound", func(t *testing.T, driver auth.UserDriver) {
		if _, err := find(t, driver, missingSubject); !errors.Is(err, auth.ErrUserNotFound) {
			t.Errorf("Find(%q) error = %v, want ErrUserNotFound", missingSubject, err)
		}
	})

	run("Disabled", func(t *testing.T, driver auth.UserDriver) {
		_, err := find(t, driver, disabledSubject)

		if !errors.Is(err, auth.ErrUserDisabled) || errors.Is(err, auth.ErrUserNotFound) {
			t.Errorf("Find(%q) error = %v, want ErrUserDisabled only", disabledSubject, err)
		}
	})

	run("InvalidSubject", func(t *testing.T, driver auth.UserDriver) {
		for _, subject := range []string{"", strings.Repeat("1", 256), "1 2", "1\x7f", "é1"} {
			if _, err := find(t, driver, subject); !errors.Is(err, auth.ErrInvalidSubject) || errors.Is(err, auth.ErrUserNotFound) {
				t.Errorf("Find(%q) error = %v, want ErrInvalidSubject only", subject, err)
			}
		}
	})
}
