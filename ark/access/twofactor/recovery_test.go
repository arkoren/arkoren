package twofactor_test

import (
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access/twofactor"
)

// TestRecoverCodeSingleUse verifies normalization, single use and the event.
func TestRecoverCodeSingleUse(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		enrollment, _ := sys.factors.Enroll(ctx, "1", "a")
		codes, _ := sys.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret))
		typed := strings.ToLower(strings.ReplaceAll(codes[0], "-", " "))
		factor, err := sys.factors.Recover(ctx, "1", typed)
		if err != nil || factor.Method != twofactor.MethodRecovery {
			t.Fatalf("Recover = %+v, %v", factor, err)
		}
		if _, err := sys.factors.Recover(ctx, "1", codes[0]); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("second Recover error = %v", err)
		}
		if _, err := sys.factors.Recover(ctx, "2", codes[1]); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("another user's code error = %v", err)
		}
		status, _ := sys.factors.Status(ctx, "1")
		if status.RecoveryCodes != 2 {
			t.Fatalf("remaining codes = %d, want 2", status.RecoveryCodes)
		}
	})
}

// TestRecoverConcurrentSameCodeOneWins verifies the atomic spend.
func TestRecoverConcurrentSameCodeOneWins(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		enrollment, _ := sys.factors.Enroll(ctx, "1", "a")
		codes, _ := sys.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret))
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := sys.factors.Recover(ctx, "1", codes[0])
				results <- err
			}()
		}
		succeeded := 0
		for range 2 {
			if err := <-results; err == nil {
				succeeded++
			}
		}
		if succeeded != 1 {
			t.Fatalf("%d recoveries succeeded, want 1", succeeded)
		}
	})
}

// TestRegenerateInvalidatesOldCodes verifies the old codes stop working.
func TestRegenerateInvalidatesOldCodes(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		if _, err := sys.factors.Regenerate(ctx, "1"); !errors.Is(err, twofactor.ErrNotEnabled) {
			t.Fatalf("Regenerate when disabled error = %v", err)
		}
		enrollment, _ := sys.factors.Enroll(ctx, "1", "a")
		old, _ := sys.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret))
		fresh, err := sys.factors.Regenerate(ctx, "1")
		if err != nil || len(fresh) != 3 {
			t.Fatalf("Regenerate = %v, %v", fresh, err)
		}
		if _, err := sys.factors.Recover(ctx, "1", old[0]); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("old code error = %v", err)
		}
		if _, err := sys.factors.Recover(ctx, "1", fresh[0]); err != nil {
			t.Fatalf("new code error = %v", err)
		}
	})
}
