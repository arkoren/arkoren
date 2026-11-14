package twofactor

import (
	"net/http"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/access/internal/web"
)

// codesBody is the response of the handlers that return recovery codes.
type codesBody struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// SetupHandler returns the handler of POST /two-factor/setup. It starts
// enrolment and answers 200 with {"secret": "...", "uri": "otpauth://..."} for a
// QR code; the label is the user's email claim when the record has one, else the
// subject. It needs a browser session and a fresh login ([auth.Guard.Fresh]); a
// user without a step-up method is fresh right after any login, so a first
// enrolment needs nothing special. A user who is already enabled gets
// [ErrAlreadyEnabled] (409). Calling it again before enabling replaces the
// pending secret.
//
// Example:
//
//	web.Post("/two-factor/setup", factors.SetupHandler(guard))
func (factors *Factors) SetupHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		label := identity.Subject()
		if email, ok := identity.Record().Claims["email"].(string); ok && email != "" {
			label = email
		}
		enrollment, err := factors.Enroll(r.Context(), identity.Subject(), label)
		if err != nil {
			return err
		}

		return response.JSON(w, http.StatusOK, enrollment)
	}
}

// EnableHandler returns the handler of POST /two-factor. The body is
// {"code": "123456"}, the first code of the pending authenticator. It enables
// two-factor, creates the recovery codes and answers 200 with
// {"recovery_codes": [...]}, the only time they are shown. The session records
// the code as a fresh factor, so the user stays fresh for the next credential
// change. A wrong code is
// [ErrInvalidCode] (422), too many are [ErrThrottled] (429), and a user who
// never ran setup gets [ErrNotEnrolling] (409). It needs a browser session and a
// fresh login.
//
// Example:
//
//	web.Post("/two-factor", factors.EnableHandler(guard))
func (factors *Factors) EnableHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		body, err := web.Decode[codeBody](r)
		if err != nil {
			return err
		}
		codes, err := factors.Activate(r.Context(), identity.Subject(), body.Code)
		if err != nil {
			return err
		}
		// Begin on the session of the same subject keeps the login and records
		// the code as a factor from now on, so the user stays fresh. It runs after
		// Activate committed, not inside its transaction: it reads credential
		// state through the request context, which must not wait for a connection
		// the transaction holds. The recovery codes are shown only once, so a
		// session-store failure here must not turn into an error that loses them;
		// the user is merely asked to confirm again before the next change.
		_, _ = guard.Begin(r, identity.Subject(), auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession})

		return response.JSON(w, http.StatusOK, codesBody{RecoveryCodes: codes})
	}
}

// DisableHandler returns the handler of DELETE /two-factor. It removes the
// authenticator, a pending enrolment and the recovery codes, and answers 204.
// It needs a browser session and a fresh login, so a user who has an
// authenticator proves a code first (POST /confirm). A user without an
// authenticator gets [ErrNotEnabled] (409).
//
// Example:
//
//	web.Delete("/two-factor", factors.DisableHandler(guard))
func (factors *Factors) DisableHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		enabled, err := factors.Enrolled(r.Context(), identity.Subject())
		if err != nil {
			return err
		}
		if !enabled {
			return ErrNotEnabled
		}
		if err := factors.Remove(r.Context(), identity.Subject()); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// RecoveryCodesHandler returns the handler of POST /two-factor/recovery-codes.
// It replaces the recovery codes and answers 200 with {"recovery_codes": [...]};
// the old codes stop working. It needs a browser session and a fresh login. A
// user without an authenticator gets [ErrNotEnabled] (409).
//
// Example:
//
//	web.Post("/two-factor/recovery-codes", factors.RecoveryCodesHandler(guard))
func (factors *Factors) RecoveryCodesHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		codes, err := factors.Regenerate(r.Context(), identity.Subject())
		if err != nil {
			return err
		}

		return response.JSON(w, http.StatusOK, codesBody{RecoveryCodes: codes})
	}
}
