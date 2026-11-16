package access_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
	"arkoren.dev/ark/access/account"
	"arkoren.dev/ark/access/apitoken"
	"arkoren.dev/ark/access/internal/apptest"
	"arkoren.dev/ark/access/password"
)

// TestHandlersPanicWhenFeatureDisabled verifies each handler of a disabled
// feature fails at composition.
func TestHandlersPanicWhenFeatureDisabled(t *testing.T) {
	t.Parallel()

	iam, err := newFacadeWith(t, wire{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, build := range map[string]func(){
		"RegisterHandler":           func() { iam.RegisterHandler() },
		"LoginHandler":              func() { iam.LoginHandler() },
		"ChallengeHandler":          func() { iam.ChallengeHandler() },
		"PendingHandler":            func() { iam.PendingHandler() },
		"ConfirmHandler":            func() { iam.ConfirmHandler() },
		"VerifyEmailHandler":        func() { iam.VerifyEmailHandler() },
		"ResendVerificationHandler": func() { iam.ResendVerificationHandler() },
		"ForgotPasswordHandler":     func() { iam.ForgotPasswordHandler() },
		"ResetPasswordHandler":      func() { iam.ResetPasswordHandler() },
		"ChangePasswordHandler":     func() { iam.ChangePasswordHandler() },
		"SetupTwoFactorHandler":     func() { iam.SetupTwoFactorHandler() },
		"EnableTwoFactorHandler":    func() { iam.EnableTwoFactorHandler() },
		"DisableTwoFactorHandler":   func() { iam.DisableTwoFactorHandler() },
		"RecoveryCodesHandler":      func() { iam.RecoveryCodesHandler() },
		"ListTokensHandler":         func() { iam.ListTokensHandler() },
		"CreateTokenHandler":        func() { iam.CreateTokenHandler() },
		"DeleteTokenHandler":        func() { iam.DeleteTokenHandler() },
		"ExchangeHandler":           func() { iam.ExchangeHandler() },
		"DeleteDeviceTokenHandler":  func() { iam.DeleteDeviceTokenHandler() },
		"ListConnectionsHandler":    func() { iam.ListConnectionsHandler() },
		"ConnectHandler":            func() { iam.ConnectHandler() },
		"DeleteConnectionHandler":   func() { iam.DeleteConnectionHandler() },
	} {
		if !panics(build) {
			t.Errorf("%s did not panic with its feature disabled", name)
		}
	}
	// These work without any feature.
	iam.LogoutHandler()
	iam.ListSessionsHandler()
	iam.DeleteSessionHandler()
	iam.DeleteOtherSessionsHandler()
	iam.DeleteAccountHandler()
	iam.RequireVerified()
}

// panics reports whether f panics.
func panics(f func()) (panicked bool) {
	defer func() {
		panicked = recover() != nil
	}()
	f()

	return false
}

// TestRegisterHandlerPanicsWithoutRegistration verifies the handler of a
// password setup that did not enable registration fails at composition.
func TestRegisterHandlerPanicsWithoutRegistration(t *testing.T) {
	t.Parallel()

	iam, err := newFacade(t, func(iam *access.Builder) { iam.Password(hasher()) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !panics(func() { iam.RegisterHandler() }) {
		t.Fatal("RegisterHandler did not panic with registration off")
	}
}

// TestTokenKindHandlersPanicWhenKindDisabled verifies the handlers of a
// disabled token kind fail at composition.
func TestTokenKindHandlersPanicWhenKindDisabled(t *testing.T) {
	t.Parallel()

	personal, err := newFacade(t, func(iam *access.Builder) {
		iam.Tokens(func(tokens *apitoken.Builder) { tokens.DeviceTokens(false) })
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	device, err := newFacade(t, func(iam *access.Builder) {
		iam.Tokens(func(tokens *apitoken.Builder) { tokens.PersonalTokens(false) })
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	personal.CreateTokenHandler()
	device.ExchangeHandler()
	device.DeleteDeviceTokenHandler()
	for name, build := range map[string]func(){
		"ExchangeHandler without device tokens":          func() { personal.ExchangeHandler() },
		"DeleteDeviceTokenHandler without device tokens": func() { personal.DeleteDeviceTokenHandler() },
		"CreateTokenHandler without personal tokens":     func() { device.CreateTokenHandler() },
	} {
		if !panics(build) {
			t.Errorf("%s did not panic", name)
		}
	}
}

// TestResetAndVerificationAreRequiredForTheirHandlers verifies the
// composition-time panics of the handler methods for a missing Reset or
// Verification; Mount instead skips those routes (see
// TestMountNeverPanicsOnAcceptedConfiguration).
func TestResetAndVerificationAreRequiredForTheirHandlers(t *testing.T) {
	t.Parallel()

	iam, err := newFacadeWith(t, wire{}, func(iam *access.Builder) { iam.Password(hasher()) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, build := range map[string]func(){
		"ForgotPasswordHandler":     func() { iam.ForgotPasswordHandler() },
		"ResetPasswordHandler":      func() { iam.ResetPasswordHandler() },
		"VerifyEmailHandler":        func() { iam.VerifyEmailHandler() },
		"ResendVerificationHandler": func() { iam.ResendVerificationHandler() },
	} {
		if !panics(build) {
			t.Errorf("%s did not panic without Reset or Verification", name)
		}
	}
}

// registerBoth registers ada@example.com as a new address and then again, typed
// differently, as a taken one, on separate devices, and returns the two replies.
func registerBoth(sys *app) (fresh, taken apptest.Reply) {
	fresh = sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"correct horse"}`)
	taken = sys.client.NewDevice().Post("/register", `{"email":"Ada@example.com","password":"another horse"}`)

	return fresh, taken
}

// TestRegisterWithResetAndVerificationDispatchesOnBothPaths verifies a new
// address dispatches a verification for the new subject and a taken one a reset
// notice for the identifier as typed, with identical answers.
func TestRegisterWithResetAndVerificationDispatchesOnBothPaths(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newAppWith(t, db, instance, both, func(iam *access.Builder) { iam.Password(hasher(), registration) })
		fresh, taken := registerBoth(sys)
		if fresh.Status != http.StatusAccepted || taken.Status != fresh.Status || taken.Body != fresh.Body {
			t.Fatalf("new = %d %q, taken = %d %q, want the same 202", fresh.Status, fresh.Body, taken.Status, taken.Body)
		}
		subject := mustSubject(t, sys)
		if got := sys.box.dispatchedVerifies; len(got) != 1 || got[0].Subject != subject {
			t.Fatalf("verifications dispatched = %+v, want one for the new subject", got)
		}
		if got := sys.box.dispatchedResets; len(got) != 1 || got[0] != (password.ResetRequest{Identifier: "Ada@example.com", Notice: true}) {
			t.Fatalf("resets dispatched = %+v, want one notice for the identifier as typed", got)
		}
	})
}

// TestRegisterWithVerificationOnlyAsksTheOwnerToVerify verifies without Reset a
// taken address dispatches a verification for its owner's subject, as the new
// address does for the new subject.
func TestRegisterWithVerificationOnlyAsksTheOwnerToVerify(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newAppWith(t, db, instance, wire{verify: true}, func(iam *access.Builder) { iam.Password(hasher(), registration) })
		fresh, taken := registerBoth(sys)
		if fresh.Status != http.StatusAccepted || taken.Status != fresh.Status || taken.Body != fresh.Body {
			t.Fatalf("new = %d %q, taken = %d %q, want the same 202", fresh.Status, fresh.Body, taken.Status, taken.Body)
		}
		subject := mustSubject(t, sys)
		if got := sys.box.dispatchedVerifies; len(got) != 2 || got[0].Subject != subject || got[1].Subject != subject {
			t.Fatalf("verifications dispatched = %+v, want two for %s", got, subject)
		}
		if len(sys.box.dispatchedResets) != 0 {
			t.Fatalf("resets dispatched = %+v, want none", sys.box.dispatchedResets)
		}
	})
}

// TestRegisterWithResetOnlyDispatchesNothing verifies with Reset and no
// Verification neither path dispatches anything, so they cost the same.
func TestRegisterWithResetOnlyDispatchesNothing(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newAppWith(t, db, instance, wire{reset: true}, func(iam *access.Builder) { iam.Password(hasher(), registration) })
		fresh, taken := registerBoth(sys)
		if fresh.Status != http.StatusAccepted || taken.Status != fresh.Status || taken.Body != fresh.Body {
			t.Fatalf("new = %d %q, taken = %d %q, want the same 202", fresh.Status, fresh.Body, taken.Status, taken.Body)
		}
		if len(sys.box.dispatchedVerifies) != 0 || len(sys.box.dispatchedResets) != 0 {
			t.Fatalf("dispatched verifications %+v and resets %+v, want none", sys.box.dispatchedVerifies, sys.box.dispatchedResets)
		}
	})
}

// TestRegisterDispatchFailureStaysAccepted verifies a dispatch that fails, on
// the new path and on the taken one (both dispatch a verification without
// Reset), is logged and the answer is still 202.
func TestRegisterDispatchFailureStaysAccepted(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newAppWith(t, db, instance, wire{verify: true}, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Directory(func(accounts *account.Builder) {
				accounts.Verification(newSigner(t, ""), func(context.Context, account.VerifyRequest) error { return errors.New("queue down") }, func(context.Context, account.VerifyMessage) error { return nil })
			})
		})
		fresh, taken := registerBoth(sys)
		if fresh.Status != http.StatusAccepted || taken.Status != http.StatusAccepted {
			t.Fatalf("new = %d %s, taken = %d %s, want 202 for both", fresh.Status, fresh.Body, taken.Status, taken.Body)
		}
	})
}

// TestResendDispatchesAndTheWorkerSkipsAVerifiedUser verifies the resend route
// dispatches for the logged-in subject whatever their state, and the worker
// sends only while the email is unverified.
func TestResendDispatchesAndTheWorkerSkipsAVerifiedUser(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) { iam.Password(hasher(), registration) })
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Post("/email/verify/resend", ""); reply.Status != http.StatusAccepted {
			t.Fatalf("resend = %d %s", reply.Status, reply.Body)
		}
		if _, verifications := sys.box.counts(); verifications != 2 {
			t.Fatalf("verification emails after a resend = %d, want 2", verifications)
		}
		if reply := sys.client.Post("/email/verify", `{"token":"`+sys.box.lastVerify(t).Signed+`"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("verify = %d %s", reply.Status, reply.Body)
		}
		if reply := sys.client.Post("/email/verify/resend", ""); reply.Status != http.StatusAccepted {
			t.Fatalf("resend after verifying = %d %s", reply.Status, reply.Body)
		}
		if got := len(sys.box.dispatchedVerifies); got != 3 {
			t.Fatalf("dispatched %d verifications, want 3: the route dispatches for a verified user too", got)
		}
		if _, verifications := sys.box.counts(); verifications != 2 {
			t.Fatalf("verification emails = %d, want 2: a verified user is sent nothing", verifications)
		}
	})
}

// TestVerifyLinkSecondClickIsInvalid verifies the second click on a link is the
// invalid-link problem, not a second success.
func TestVerifyLinkSecondClickIsInvalid(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) { iam.Password(hasher(), registration) })
		register(t, sys.client, "ada@example.com")
		body := `{"token":"` + sys.box.lastVerify(t).Signed + `"}`
		if reply := sys.client.Post("/email/verify", body); reply.Status != http.StatusNoContent {
			t.Fatalf("first click = %d %s", reply.Status, reply.Body)
		}
		if reply := sys.client.Post("/email/verify", body); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("second click = %d %s, want 422", reply.Status, reply.Body)
		}
	})
}
