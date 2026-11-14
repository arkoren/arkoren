package twofactor_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
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
// the core enrolment event on every database.
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
			t.Fatal("a pending enrolment counts as enrolled")
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

// TestEnrollWhenEnabledConflicts verifies enrolment is refused after enabling
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

// TestVerifyRejectsReplayedStep verifies a code works once.
func TestVerifyRejectsReplayedStep(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, nil)
		enrollment, _ := sys.factors.Enroll(ctx, "1", "a")
		if _, err := sys.factors.Activate(ctx, "1", currentCode(t, enrollment.Secret)); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		// The activation code is spent: the same code is a replay.
		if _, err := sys.factors.Verify(ctx, "1", currentCode(t, enrollment.Secret)); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("replayed code error = %v", err)
		}
		rewind(t, db, instance)
		factor, err := sys.factors.Verify(ctx, "1", currentCode(t, enrollment.Secret))
		if err != nil || factor.Method != auth.MethodOTP || factor.Kind != auth.KindPossession {
			t.Fatalf("Verify = %+v, %v", factor, err)
		}
		if _, err := sys.factors.Verify(ctx, "1", currentCode(t, enrollment.Secret)); !errors.Is(err, twofactor.ErrInvalidCode) {
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

		newCipher, _ := newCipher(t, oldDriver)
		rotated, err := twofactor.NewFactors(oldSys.provider, db, cacheOf(t), newCipher, "Example", func(factors *twofactor.Builder) {
			factors.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewFactors: %v", err)
		}
		if _, err := rotated.Verify(ctx, "1", currentCode(t, enrollment.Secret)); err != nil {
			t.Fatalf("Verify with the rotated key: %v", err)
		}
		// After resealing, a cipher that knows only the new key still reads it.
		onlyNew, err := twofactor.NewFactors(oldSys.provider, db, cacheOf(t), newCipher, "Example", func(factors *twofactor.Builder) {
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

// TestHandlersSetupEnableChallengeFlow verifies the whole HTTP flow: setup,
// enable, level multi, then a second device logs in with a TOTP code and with a
// recovery code.
func TestHandlersSetupEnableChallengeFlow(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, nil)
		secret, codes := sys.enable(t, sys.client, "1")
		if reply := sys.client.Do(http.MethodGet, "/level", ""); reply.Body != "2" {
			t.Fatalf("level after enabling = %q, want 2 (multi)", reply.Body)
		}
		rewind(t, db, instance)

		device := sys.client.NewDevice()
		login := device.Post("/dev-login/1", "")
		if login.Status != http.StatusAccepted || !strings.Contains(login.Body, `"otp","recovery"`) {
			t.Fatalf("login = %d %s, want 202 listing otp then recovery", login.Status, login.Body)
		}
		wrong := device.Post("/login/challenge", `{"method":"otp","code":"000000"}`)
		if wrong.Status != http.StatusForbidden {
			t.Fatalf("wrong code = %d %s", wrong.Status, wrong.Body)
		}
		ok := device.Post("/login/challenge", `{"method":"otp","code":"`+currentCode(t, secret)+`"}`)
		if ok.Status != http.StatusNoContent {
			t.Fatalf("challenge = %d %s", ok.Status, ok.Body)
		}

		third := sys.client.NewDevice()
		third.Post("/dev-login/1", "")
		recovery := third.Post("/login/challenge", `{"method":"recovery","code":"`+codes[0]+`"}`)
		if recovery.Status != http.StatusNoContent {
			t.Fatalf("recovery challenge = %d %s", recovery.Status, recovery.Body)
		}
	})
}

// TestHandlersEnableWrongCodeAndSetupWhenEnabled verifies the 422 and 409
// answers.
func TestHandlersEnableWrongCodeAndSetupWhenEnabled(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, nil)
		client := sys.client
		client.Post("/dev-login/1", "")
		if reply := client.Post("/two-factor", `{"code":"123456"}`); reply.Status != http.StatusConflict {
			t.Fatalf("enable without setup = %d %s", reply.Status, reply.Body)
		}
		client.Post("/two-factor/setup", "")
		if reply := client.Post("/two-factor", `{"code":"000000"}`); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("enable with a wrong code = %d %s", reply.Status, reply.Body)
		}
		sys.enable(t, sys.client.NewDevice(), "2")
		other := sys.client.NewDevice()
		other.Post("/dev-login/2", "")
		if reply := other.Post("/login/challenge", `{"method":"otp","code":"x"}`); reply.Status == http.StatusNoContent {
			t.Fatalf("a logged-in session passed a challenge: %d", reply.Status)
		}
	})
}

// TestHandlersRejectBearerIdentity verifies a token identity can neither
// enrol, enable, disable nor regenerate.
func TestHandlersRejectBearerIdentity(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, nil)
		bearer := "Authorization: Bearer " + bearerSubject
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, "/two-factor/setup"},
			{http.MethodPost, "/two-factor"},
			{http.MethodDelete, "/two-factor"},
			{http.MethodPost, "/two-factor/recovery-codes"},
		} {
			reply := sys.client.Do(route.method, route.path, `{"code":"123456"}`, bearer)
			if reply.Status != http.StatusUnauthorized {
				t.Errorf("%s %s with a bearer token = %d, want 401", route.method, route.path, reply.Status)
			}
		}
	})
}

// TestHandlersDisableNeedsFreshProof verifies a user with an authenticator must
// confirm before disabling once the proof is old, and that disabling clears it.
func TestHandlersDisableAndRegenerate(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, nil)
		secret, _ := sys.enable(t, sys.client, "1")
		regenerated := sys.client.Post("/two-factor/recovery-codes", "")
		if regenerated.Status != http.StatusOK {
			t.Fatalf("regenerate = %d %s", regenerated.Status, regenerated.Body)
		}
		rewind(t, db, instance)
		if reply := sys.client.Post("/confirm", `{"method":"otp","code":"`+currentCode(t, secret)+`"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("confirm = %d %s", reply.Status, reply.Body)
		}
		if reply := sys.client.Do(http.MethodDelete, "/two-factor", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("disable = %d %s", reply.Status, reply.Body)
		}
		if reply := sys.client.Do(http.MethodDelete, "/two-factor", ""); reply.Status != http.StatusConflict {
			t.Fatalf("second disable = %d, want 409", reply.Status)
		}
		if reply := sys.client.NewDevice().Post("/dev-login/1", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("login after disabling = %d, want 204", reply.Status)
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
