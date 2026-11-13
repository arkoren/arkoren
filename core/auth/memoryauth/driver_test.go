package memoryauth_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/authtest"
	"arkoren.dev/core/auth/memoryauth"
)

// TestDriver runs the shared conformance suite.
func TestDriver(t *testing.T) {
	t.Parallel()

	authtest.RunUserDriver(t, func(t *testing.T, users []authtest.User) auth.UserDriver {
		driver := memoryauth.New()

		for _, user := range users {
			record := auth.UserRecord{Subject: user.Subject, Version: user.Version, Claims: user.Claims}

			if err := driver.Put(record); err != nil {
				t.Fatal(err)
			}

			if user.Disabled {
				if err := driver.Disable(user.Subject); err != nil {
					t.Fatal(err)
				}
			}
		}

		return driver
	})
}

// TestPutRejectsInvalidSubject verifies an invalid subject is not stored.
func TestPutRejectsInvalidSubject(t *testing.T) {
	t.Parallel()

	if err := memoryauth.New().Put(auth.UserRecord{Subject: "a b"}); !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("error = %v", err)
	}
}

// TestClaimsAreCopied verifies callers cannot change stored claims.
func TestClaimsAreCopied(t *testing.T) {
	t.Parallel()

	driver := memoryauth.New()
	claims := map[string]any{"role": "admin"}

	if err := driver.Put(auth.UserRecord{Subject: "1", Claims: claims}); err != nil {
		t.Fatal(err)
	}

	claims["role"] = "root"

	record, err := driver.Find(t.Context(), "1")

	if err != nil || record.Claims["role"] != "admin" {
		t.Fatalf("record = %v, error = %v", record, err)
	}

	record.Claims["role"] = "root"

	if again, _ := driver.Find(t.Context(), "1"); again.Claims["role"] != "admin" {
		t.Fatalf("stored claims changed: %v", again.Claims)
	}
}

// TestDisableAndDelete verifies Disable needs an existing user, Put re-enables,
// and Delete makes the user missing.
func TestDisableAndDelete(t *testing.T) {
	t.Parallel()

	driver := memoryauth.New()

	if err := driver.Disable("1"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("Disable missing = %v", err)
	}

	if err := driver.Put(auth.UserRecord{Subject: "1"}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Disable("1"); err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Find(t.Context(), "1"); !errors.Is(err, auth.ErrUserDisabled) {
		t.Fatalf("Find disabled = %v", err)
	}

	if err := driver.Put(auth.UserRecord{Subject: "1"}); err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Find(t.Context(), "1"); err != nil {
		t.Fatalf("Find re-enabled = %v", err)
	}

	driver.Delete("1")

	if _, err := driver.Find(t.Context(), "1"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("Find deleted = %v", err)
	}
}

// TestFindHonorsCanceledContext verifies a canceled context fails before lookup.
func TestFindHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := memoryauth.New().Find(ctx, "1"); !errors.Is(err, ctx.Err()) {
		t.Fatalf("error = %v", err)
	}
}
