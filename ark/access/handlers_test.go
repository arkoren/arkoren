package access_test

import (
	"testing"

	"arkoren.dev/ark/access"
	"arkoren.dev/ark/access/apitoken"
)

// TestHandlersPanicWhenFeatureDisabled verifies each handler of a disabled
// feature fails at composition.
func TestHandlersPanicWhenFeatureDisabled(t *testing.T) {
	t.Parallel()

	iam, err := newFacade(t)
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

// TestDeliverIsRequiredForForgotAndVerify verifies the composition-time panics
// of the handler methods for a missing delivery function; Mount instead skips
// those routes (see TestMountNeverPanicsOnAcceptedConfiguration).
func TestDeliverIsRequiredForForgotAndVerify(t *testing.T) {
	t.Parallel()

	iam, err := newFacade(t, func(iam *access.Builder) { iam.Password(hasher(), registration) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, build := range map[string]func(){
		"ForgotPasswordHandler":     func() { iam.ForgotPasswordHandler() },
		"VerifyEmailHandler":        func() { iam.VerifyEmailHandler() },
		"ResendVerificationHandler": func() { iam.ResendVerificationHandler() },
	} {
		if !panics(build) {
			t.Errorf("%s did not panic without Deliver", name)
		}
	}
}
