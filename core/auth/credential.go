package auth

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"arkoren.dev/foundation/problem"
)

// ErrInvalidCredential is the 403 problem for a credential that did not check
// out: a wrong code or password, or a method the user is not enrolled in. The
// two look the same on purpose, so a client cannot probe which methods a user
// has. A [Credential.Verify] returns an error wrapping it to say "wrong input":
// the attempt then stays counted against the user's limit (see
// [GuardBuilder.MaxAttempts]). Any other error from Verify is an infrastructure
// failure; the guard refunds the attempt and returns the error unchanged. A
// locked-out user of [Guard.ConfirmHandler] receives it too. It is 403, not 401,
// because the confirm route serves signed-in users and a 401 invites login
// redirects that would discard their session; the challenge route uses it too, so
// a client handles one problem for a wrong code.
//
// Example:
//
//	func verify(r *http.Request, subject string, input jsontext.Value) (auth.Factor, error) {
//		if !checkCode(subject, input) {
//			return auth.Factor{}, auth.ErrInvalidCredential
//		}
//		return auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession}, nil
//	}
var ErrInvalidCredential = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:core:auth:invalid-credential")
	details.Title("Invalid credential")
	details.Detail("The credential is not valid.")
	details.Status(http.StatusForbidden)
})

// ErrUnknownMethod is the 422 problem returned by [Guard.ChallengeHandler] and
// [Guard.ConfirmHandler] when the request names no method, or one that is not
// registered for that route or cannot be completed there (a redirect method has
// no [Credential.Verify]). It costs no attempt.
//
// Example:
//
//	if errors.Is(err, auth.ErrUnknownMethod) {
//		// Show the list of methods again.
//	}
var ErrUnknownMethod = problem.UnprocessableContent().Derive(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:core:auth:unknown-method")
	details.Title("Unknown method")
	details.Detail("The method is not available here.")
})

// errLockedOut reports a credential check refused because the user's attempts in
// the window already exceed [GuardBuilder.MaxAttempts]. Handlers translate it:
// the challenge discards the pending login, the confirm route answers like a
// wrong credential.
var errLockedOut = errors.New("auth: too many credential attempts")

// Credential is one way a user proves themselves again, registered with
// [GuardBuilder.SecondFactor] to finish a parked login or with
// [GuardBuilder.StepUp] to re-prove a logged-in user. A module that owns a
// method (a password, a TOTP authenticator, a passkey) exports its Credential
// and the application registers it, so core never imports the module and a
// third-party module cooperates exactly like ours.
//
// Example:
//
//	credential := auth.Credential{
//		Method: auth.MethodOTP,
//		Kind:   auth.KindPossession,
//		Enrolled: func(ctx context.Context, subject string) (bool, error) {
//			return authenticators.Has(ctx, subject)
//		},
//		Verify: func(r *http.Request, subject string, input jsontext.Value) (auth.Factor, error) {
//			var body struct {
//				Code string `json:"code"`
//			}
//			if err := json.Unmarshal(input, &body); err != nil || !authenticators.Check(r.Context(), subject, body.Code) {
//				return auth.Factor{}, auth.ErrInvalidCredential
//			}
//			return auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession}, nil
//		},
//	}
type Credential struct {
	// Method names the credential in requests and in [Outcome.Methods]. It is an
	// RFC 8176 value or the module's own; it must not be empty. A registry holds
	// each method once. The factor [Credential.Verify] returns is recorded under
	// this method whatever it sets.
	Method Method

	// Kind is what the credential proves and the default for the factor Verify
	// returns. Verify may return another Kind (a passkey that checked user
	// verification can report [KindInherence]); the session then keeps both.
	Kind Kind

	// Enrolled reports whether subject has this credential. It must not be nil.
	// A true answer makes the method available to the user: it parks logins and
	// is listed in challenges. An error stops the operation that asked, so a
	// failing lookup never loosens a rule.
	Enrolled func(ctx context.Context, subject string) (bool, error)

	// Verify checks the method's input, the request body without its "method"
	// member, for subject and returns the factor it proved. At is set by the
	// guard. An error wrapping [ErrInvalidCredential] means the input was wrong;
	// any other error is an infrastructure failure that refunds the attempt.
	// Verify must not write the response or touch the session. A nil Verify marks
	// a redirect method (for example an external provider): it is listed with
	// the other methods but completed through its own route, so the challenge
	// and confirm handlers answer [ErrUnknownMethod] for it.
	Verify func(r *http.Request, subject string, input jsontext.Value) (Factor, error)
}

// validateCredentials reports the first problem of a registry, named by role in
// the error: an empty method, a nil Enrolled or a method registered twice.
func validateCredentials(role string, credentials []Credential) error {
	for i, credential := range credentials {
		if credential.Method == "" {
			return fmt.Errorf("auth: %s credential with an empty method", role)
		}

		if credential.Enrolled == nil {
			return fmt.Errorf("auth: %s credential %q has a nil Enrolled", role, credential.Method)
		}

		if slices.ContainsFunc(credentials[:i], func(earlier Credential) bool { return earlier.Method == credential.Method }) {
			return fmt.Errorf("auth: duplicate %s method %q", role, credential.Method)
		}
	}

	return nil
}

// enrolled returns the methods of credentials in which subject is enrolled, in
// registration order. It is never nil. The first error from an Enrolled function
// stops the lookup and is returned, so callers fail closed.
func enrolled(ctx context.Context, subject string, credentials []Credential) ([]Method, error) {
	methods := []Method{}

	for _, credential := range credentials {
		ok, err := credential.Enrolled(ctx, subject)

		if err != nil {
			return nil, err
		}

		if ok {
			methods = append(methods, credential.Method)
		}
	}

	return methods, nil
}
