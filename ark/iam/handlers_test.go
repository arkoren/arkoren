package iam_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/account"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/internal/apptest"
	"arkoren.dev/ark/iam/password"
)

// TestHandlersPanicWhenFeatureDisabled verifies each handler of a disabled
// feature fails at composition.
func TestHandlersPanicWhenFeatureDisabled(t *testing.T) {
	t.Parallel()

	identity, err := newFacadeWith(t, wire{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, build := range map[string]func(){
		"RegisterHandler":           func() { identity.RegisterHandler() },
		"LoginHandler":              func() { identity.LoginHandler() },
		"ChallengeHandler":          func() { identity.ChallengeHandler() },
		"PendingHandler":            func() { identity.PendingHandler() },
		"ConfirmHandler":            func() { identity.ConfirmHandler() },
		"VerifyEmailHandler":        func() { identity.VerifyEmailHandler() },
		"ResendVerificationHandler": func() { identity.ResendVerificationHandler() },
		"ForgotPasswordHandler":     func() { identity.ForgotPasswordHandler() },
		"ResetPasswordHandler":      func() { identity.ResetPasswordHandler() },
		"ChangePasswordHandler":     func() { identity.ChangePasswordHandler() },
		"SetupTwoFactorHandler":     func() { identity.SetupTwoFactorHandler() },
		"EnableTwoFactorHandler":    func() { identity.EnableTwoFactorHandler() },
		"DisableTwoFactorHandler":   func() { identity.DisableTwoFactorHandler() },
		"RecoveryCodesHandler":      func() { identity.RecoveryCodesHandler() },
		"ListTokensHandler":         func() { identity.ListTokensHandler() },
		"CreateTokenHandler":        func() { identity.CreateTokenHandler() },
		"DeleteTokenHandler":        func() { identity.DeleteTokenHandler() },
		"ExchangeHandler":           func() { identity.ExchangeHandler() },
		"DeleteDeviceTokenHandler":  func() { identity.DeleteDeviceTokenHandler() },
		"ListConnectionsHandler":    func() { identity.ListConnectionsHandler() },
		"ConnectHandler":            func() { identity.ConnectHandler() },
		"DeleteConnectionHandler":   func() { identity.DeleteConnectionHandler() },
	} {
		if !panics(build) {
			t.Errorf("%s did not panic with its feature disabled", name)
		}
	}
	// These work without any feature.
	identity.LogoutHandler()
	identity.ListSessionsHandler()
	identity.DeleteSessionHandler()
	identity.DeleteOtherSessionsHandler()
	identity.DeleteAccountHandler()
	identity.RequireVerified()
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

	identity, err := newFacade(t, func(identity *iam.Builder) { identity.Password(true, hasher()) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !panics(func() { identity.RegisterHandler() }) {
		t.Fatal("RegisterHandler did not panic with registration off")
	}
}

// TestTokenKindHandlersPanicWhenKindDisabled verifies the handlers of a
// disabled token kind fail at composition.
func TestTokenKindHandlersPanicWhenKindDisabled(t *testing.T) {
	t.Parallel()

	personal, err := newFacade(t, func(identity *iam.Builder) {
		identity.Tokens(true, func(tokens *apitoken.Builder) { tokens.DeviceTokens(false) })
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	device, err := newFacade(t, func(identity *iam.Builder) {
		identity.Tokens(true, func(tokens *apitoken.Builder) { tokens.PersonalTokens(false) })
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
// Verification; Register instead skips those routes (see
// TestRegisterNeverPanicsOnAcceptedConfiguration).
func TestResetAndVerificationAreRequiredForTheirHandlers(t *testing.T) {
	t.Parallel()

	identity, err := newFacadeWith(t, wire{}, func(identity *iam.Builder) { identity.Password(true, hasher()) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, build := range map[string]func(){
		"ForgotPasswordHandler":     func() { identity.ForgotPasswordHandler() },
		"ResetPasswordHandler":      func() { identity.ResetPasswordHandler() },
		"VerifyEmailHandler":        func() { identity.VerifyEmailHandler() },
		"ResendVerificationHandler": func() { identity.ResendVerificationHandler() },
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
		sys := newAppWith(t, db, instance, both, func(identity *iam.Builder) { identity.Password(true, hasher(), registration) })
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

// TestRegisterWithResetOnlyDispatchesNothing verifies with Reset and no
// Verification neither path dispatches anything, so they cost the same.
func TestRegisterWithResetOnlyDispatchesNothing(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newAppWith(t, db, instance, wire{reset: true}, func(identity *iam.Builder) { identity.Password(true, hasher(), registration) })
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
// the new path (the taken one dispatches a reset notice), is logged and the
// answer is still 202.
func TestRegisterDispatchFailureStaysAccepted(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newAppWith(t, db, instance, both, func(identity *iam.Builder) {
			identity.Password(true, hasher(), registration)
			identity.Directory(func(accounts *account.Builder) {
				accounts.Verification(true, newSigner(t, ""), func(context.Context, account.VerifyRequest) error { return errors.New("queue down") }, func(context.Context, account.VerifyMessage) error { return nil })
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) { identity.Password(true, hasher(), registration) })
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) { identity.Password(true, hasher(), registration) })
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
