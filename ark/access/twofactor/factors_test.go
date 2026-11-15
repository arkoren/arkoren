package twofactor_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/twofactor"
)

// rewind lets the last accepted TOTP step be accepted again, so a test can
// verify more than one code within one 30-second period.
func rewind(t *testing.T, db *database.Database, instance string) {
	t.Helper()

	_, err := db.Exec(testContext(t), query.Table(instance+"_totp_secrets", func(statement *query.Builder) {
		statement.Unfiltered().Update(map[string]any{"last_step": 0})
	}))
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
}

// TestEnrollActivateRoundTrip verifies setup, enable, the recovery codes and
// the core enrollment event on every database.
func TestEnrollActivateRoundTrip(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		enrollment, err := sys.factors.Enroll(ctx, "1", "ada@example.com")
		if err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		if !strings.HasPrefix(enrollment.URI, "otpauth://totp/Example:") {
			t.Fatalf("URI = %q", enrollment.URI)
		}
		if enabled, _ := sys.factors.Enrolled(ctx, "1"); enabled {
			t.Fatal("a pending enrollment counts as enrolled")
		}
		codes, err := sys.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret))
		if err != nil {
			t.Fatalf("Activate: %v", err)
		}
		if len(codes) != 3 || len(codes[0]) != 19 {
			t.Fatalf("recovery codes = %v", codes)
		}
		status, err := sys.factors.Status(ctx, "1")
		if err != nil || !status.Enabled || status.RecoveryCodes != 3 || status.EnabledAt.IsZero() {
			t.Fatalf("Status = %+v, %v", status, err)
		}
		if len(sys.enrolled) != 1 || sys.enrolled[0].Subject != "1" || sys.enrolled[0].Method != auth.MethodOTP {
			t.Fatalf("CredentialEnrolled events = %+v", sys.enrolled)
		}
	})
}

// TestEnrollOtpauthLabelEscaping verifies a colon in the label cannot corrupt
// the provisioning URI.
func TestEnrollOtpauthLabelEscaping(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, nil)
		enrollment, err := sys.factors.Enroll(testContext(t), "1", "ada:lovelace/x@example.com")
		if err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		if strings.Count(strings.SplitN(enrollment.URI, "?", 2)[0], ":") != 2 {
			t.Fatalf("URI label has stray colons: %q", enrollment.URI)
		}
	})
}

// TestEnrollWhenEnabledConflicts verifies enrollment is refused after enabling
// and that a second setup before enabling replaces the pending secret.
func TestEnrollWhenEnabledConflicts(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		first, _ := sys.factors.Enroll(ctx, "1", "a")
		second, err := sys.factors.Enroll(ctx, "1", "a")
		if err != nil || first.Secret == second.Secret {
			t.Fatalf("second Enroll = %+v, %v", second, err)
		}
		if _, err := sys.factors.Activate(ctx, "1", currentCode(t, first.Secret)); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("replaced secret still activates: %v", err)
		}
		if _, err := sys.factors.Activate(ctx, "1", currentCode(t, second.Secret)); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		if _, err := sys.factors.Enroll(ctx, "1", "a"); !errors.Is(err, twofactor.ErrAlreadyEnabled) {
			t.Fatalf("Enroll when enabled error = %v", err)
		}
	})
}

// TestActivateWithoutEnrollIsNotEnrolling verifies the 409.
func TestActivateWithoutEnrollIsNotEnrolling(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, nil)
		if _, err := sys.factors.Activate(testContext(t), "1", "123456"); !errors.Is(err, twofactor.ErrNotEnrolling) {
			t.Fatalf("error = %v, want ErrNotEnrolling", err)
		}
	})
}

// TestActivateThrottlesWrongCodes verifies the eleventh wrong code is 429.
func TestActivateThrottlesWrongCodes(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		if _, err := sys.factors.Enroll(ctx, "1", "a"); err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		for range 10 {
			if _, err := sys.factors.Activate(ctx, "1", "000000"); !errors.Is(err, twofactor.ErrInvalidCode) {
				t.Fatalf("wrong code error = %v", err)
			}
		}
		if _, err := sys.factors.Activate(ctx, "1", "000000"); !errors.Is(err, twofactor.ErrThrottled) {
			t.Fatalf("eleventh attempt error = %v, want ErrThrottled", err)
		}
	})
}

// TestVerifyRejectsReplayedStep verifies a code works once: the string that
// was accepted is refused when submitted again.
func TestVerifyRejectsReplayedStep(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		enrollment, _ := sys.factors.Enroll(ctx, "1", "a")
		activation := currentCode(t, enrollment.Secret)
		if _, err := sys.factors.Activate(ctx, "1", activation); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		// The activation code is spent: submitting the same string is a replay.
		if _, err := sys.factors.Verify(ctx, "1", activation); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("replayed code error = %v", err)
		}
		rewind(t, db, instance)
		code := currentCode(t, enrollment.Secret)
		factor, err := sys.factors.Verify(ctx, "1", code)
		if err != nil || factor.Method != auth.MethodOTP || factor.Kind != auth.KindPossession {
			t.Fatalf("Verify = %+v, %v", factor, err)
		}
		if _, err := sys.factors.Verify(ctx, "1", code); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("second Verify error = %v", err)
		}
	})
}

// TestVerifyConcurrentSameCodeOneWins verifies the atomic step update.
func TestVerifyConcurrentSameCodeOneWins(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		enrollment, _ := sys.factors.Enroll(ctx, "1", "a")
		_, _ = sys.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret))
		rewind(t, db, instance)
		code := currentCode(t, enrollment.Secret)
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := sys.factors.Verify(ctx, "1", code)
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
			t.Fatalf("%d verifications succeeded, want 1", succeeded)
		}
	})
}

// TestVerifyUnenrolledLooksLikeWrongCode verifies no user state leaks.
func TestVerifyUnenrolledLooksLikeWrongCode(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		if _, err := sys.factors.Verify(ctx, "2", "123456"); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("Verify error = %v", err)
		}
		if _, err := sys.factors.Recover(ctx, "2", "abcd-efgh-ijkl-mnop"); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("Recover error = %v", err)
		}
	})
}

// TestRemoveDeletesEverything verifies Remove clears secret and codes and is
// idempotent.
func TestRemoveDeletesEverything(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		enrollment, _ := sys.factors.Enroll(ctx, "1", "a")
		_, _ = sys.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret))
		for range 2 {
			if err := sys.factors.Remove(ctx, "1"); err != nil {
				t.Fatalf("Remove: %v", err)
			}
		}
		if status, _ := sys.factors.Status(ctx, "1"); status.Enabled || status.RecoveryCodes != 0 {
			t.Fatalf("Status after Remove = %+v", status)
		}
	})
}

// TestForgetHookRollsBackWithTransaction verifies the forget hook joins the
// caller's transaction.
func TestForgetHookRollsBackWithTransaction(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		enrollment, _ := sys.factors.Enroll(ctx, "1", "a")
		_, _ = sys.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret))
		errRollback := errors.New("roll back")
		err := db.WithTransaction(ctx, func(ctx context.Context) error {
			if err := sys.provider.Forget(ctx, "1"); err != nil {
				return err
			}

			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("transaction error = %v", err)
		}
		if enabled, _ := sys.factors.Enrolled(ctx, "1"); !enabled {
			t.Fatal("a rolled-back forget deleted the secret")
		}
		if err := sys.provider.Forget(ctx, "1"); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if enabled, _ := sys.factors.Enrolled(ctx, "1"); enabled {
			t.Fatal("Forget left the secret")
		}
	})
}

// TestSecretKeyRotationReencrypts verifies a secret sealed with a previous key
// keeps working and is resealed with the current one.
func TestSecretKeyRotationReencrypts(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		oldCipher, oldDriver := newCipher(t)
		oldSys := newSystem(t, db, instance, oldCipher)
		enrollment, _ := oldSys.factors.Enroll(ctx, "1", "a")
		_, _ = oldSys.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret))
		rewind(t, db, instance)

		rotatedCipher, newDriver := newCipher(t, oldDriver)
		rotated, err := twofactor.NewFactors(oldSys.provider, db, cacheOf(t), rotatedCipher, "Example", func(factors *twofactor.Builder) {
			factors.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewFactors: %v", err)
		}
		if _, err := rotated.Verify(ctx, "1", currentCode(t, enrollment.Secret)); err != nil {
			t.Fatalf("Verify with the rotated key: %v", err)
		}
		// After resealing, a cipher that knows only the new key still reads it.
		onlyNewCipher, err := crypto.New(newDriver)
		if err != nil {
			t.Fatalf("crypto.New: %v", err)
		}
		onlyNew, err := twofactor.NewFactors(oldSys.provider, db, cacheOf(t), onlyNewCipher, "Example", func(factors *twofactor.Builder) {
			factors.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewFactors: %v", err)
		}
		rewind(t, db, instance)
		if _, err := onlyNew.Verify(ctx, "1", currentCode(t, enrollment.Secret)); err != nil {
			t.Fatalf("Verify after resealing: %v", err)
		}
	})
}

// TestTwoInstancesSeparateTables verifies two instances keep separate state.
func TestTwoInstancesSeparateTables(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		first := newSystem(t, db, instance+"_a", nil)
		second := newSystem(t, db, instance+"_b", nil)
		enrollment, _ := first.factors.Enroll(ctx, "1", "a")
		_, _ = first.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret))
		if enabled, _ := second.factors.Enrolled(ctx, "1"); enabled {
			t.Fatal("the second instance sees the first instance's secret")
		}
	})
}

// TestPingNamesMigrations verifies Ping reports missing tables.
func TestPingNamesMigrations(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		cipher, _ := newCipher(t)
		sys := newSystem(t, db, instance+"_x", cipher)
		factors, err := twofactor.NewFactors(sys.provider, db, cacheOf(t), cipher, "Example", func(factors *twofactor.Builder) {
			factors.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewFactors: %v", err)
		}
		if err := factors.Ping(ctx); !errors.Is(err, twofactor.ErrNotMigrated) {
			t.Fatalf("Ping error = %v, want ErrNotMigrated", err)
		}
		migrateUp(t, db, factors.Migrations()...)
		if err := factors.Ping(ctx); err != nil {
			t.Fatalf("Ping after migrations: %v", err)
		}
	})
}

// TestNewFactorsRejectsBadConfig verifies the configuration errors.
func TestNewFactorsRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := twofactor.NewFactors(nil, nil, nil, nil, "Example"); !errors.Is(err, twofactor.ErrInvalidConfig) {
		t.Fatalf("nil dependencies error = %v", err)
	}
}
