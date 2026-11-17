package twofactor_test

import (
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/internal/apptest"
)

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
// enroll, enable, disable nor regenerate.
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

// loggedInWithRecoveryCode enables two-factor for user "1" and returns a new
// device that logged in with the first recovery code, so its only second factor
// is a recovery code, which is no step-up method.
func loggedInWithRecoveryCode(t *testing.T, sys *system) *apptest.Client {
	t.Helper()

	_, codes := sys.enable(t, sys.client, "1")
	device := sys.client.NewDevice()
	device.Post("/dev-login/1", "")

	if reply := device.Post("/login/challenge", `{"method":"recovery","code":"`+codes[0]+`"}`); reply.Status != http.StatusNoContent {
		t.Fatalf("recovery challenge = %d %s", reply.Status, reply.Body)
	}

	return device
}

// TestHandlersDisableAfterRecoveryLogin verifies a user who logged in with a
// recovery code, having lost the authenticator, can disable two-factor: the
// recent recovery code counts as the fresh login that core's freshness, which
// knows only the authenticator as a step-up method, would refuse.
func TestHandlersDisableAfterRecoveryLogin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, nil)
		device := loggedInWithRecoveryCode(t, sys)

		if reply := device.Do(http.MethodDelete, "/two-factor", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("disable after a recovery login = %d %s, want 204", reply.Status, reply.Body)
		}
	})
}

// TestHandlersRegenerateAfterRecoveryLogin verifies a recent recovery code also
// lets the user replace their recovery codes.
func TestHandlersRegenerateAfterRecoveryLogin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, nil)
		device := loggedInWithRecoveryCode(t, sys)

		if reply := device.Post("/two-factor/recovery-codes", ""); reply.Status != http.StatusOK {
			t.Fatalf("regenerate after a recovery login = %d %s, want 200", reply.Status, reply.Body)
		}
	})
}
