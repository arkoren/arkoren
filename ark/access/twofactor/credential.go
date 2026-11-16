package twofactor

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"arkoren.dev/core/auth"
)

// codeBody is the input of the code credentials.
type codeBody struct {
	Code string `json:"code"`
}

// credential builds the credential of one method around verify.
func credential(method auth.Method, enrolled func(context.Context, string) (bool, error), verify func(context.Context, string, string) (auth.Factor, error)) auth.Credential {
	return auth.Credential{
		Method:   method,
		Kind:     auth.KindPossession,
		Enrolled: enrolled,
		Verify: func(r *http.Request, subject string, input jsontext.Value) (auth.Factor, error) {
			var body codeBody
			if err := json.Unmarshal(input, &body); err != nil {
				return auth.Factor{}, fmt.Errorf("twofactor: %w", auth.ErrInvalidCredential)
			}
			factor, err := verify(r.Context(), subject, body.Code)
			if errors.Is(err, ErrInvalidCode) {
				return auth.Factor{}, fmt.Errorf("twofactor: %w", auth.ErrInvalidCredential)
			}

			return factor, err
		},
	}
}

// Credential returns the [auth.Credential] of the authenticator: method "otp",
// kind possession, request body {"method": "otp", "code": "123456"}. Register it
// with the guard's SecondFactor registry to finish a parked login
// (POST /login/challenge) and with its StepUp registry to re-prove a logged-in
// user (POST /confirm). A wrong code wraps [auth.ErrInvalidCredential], which
// core counts as an attempt.
//
// Example:
//
//	policy.SecondFactor(factors.Credential(), factors.RecoveryCredential())
//	policy.StepUp(passwords.Credential(), factors.Credential())
func (factors *Factors) Credential() auth.Credential {
	return credential(auth.MethodOTP, factors.Enrolled, factors.Verify)
}

// RecoveryCredential returns the [auth.Credential] of the recovery codes:
// method [MethodRecovery], kind possession, request body
// {"method": "recovery", "code": "abcd-efgh-ijkl-mnop"}. It is enrolled while the
// user has an authenticator and an unspent code. Register it with the SecondFactor
// registry after [Factors.Credential]; it is not a step-up method, so core's
// freshness never counts a spent recovery code. [Factors.DisableHandler] and
// [Factors.RecoveryCodesHandler] do accept one used within [Builder.StepUpWindow]
// as the fresh login they need.
//
// Example:
//
//	policy.SecondFactor(factors.Credential(), factors.RecoveryCredential())
func (factors *Factors) RecoveryCredential() auth.Credential {
	return credential(MethodRecovery, func(ctx context.Context, subject string) (bool, error) {
		enabled, err := factors.Enrolled(ctx, subject)
		if err != nil || !enabled {
			return false, err
		}
		remaining, err := factors.unused(ctx, subject)

		return remaining > 0, err
	}, factors.Recover)
}
