package twofactor

import (
	"context"
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"

	"arkoren.dev/ark/iam/internal/web"
)

// codesBody is the response of the handlers that return recovery codes.
type codesBody struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// requireSecondFactor returns nil when the session holds a factor of a
// second-factor method (the authenticator, a recovery code, or one registered
// with [Builder.Methods]) that this server verified within the step-up window,
// and otherwise [auth.ErrStepUpRequired] whose "methods" lists the user's
// enrolled second-factor methods the confirm route accepts: the authenticator
// and the registered ones, not recovery codes, which only a login can spend. A
// recent password, which core's freshness accepts, is deliberately not enough:
// whoever has the password and an old session must not be able to weaken the
// second factor. The user proves a code with POST /confirm and retries.
func (factors *Factors) requireSecondFactor(ctx context.Context, identity auth.Identity) error {
	if factors.hasRecent(identity, factors.secondFactorMethods()) {
		return nil
	}
	methods, err := factors.enrolledMethods(ctx, identity.Subject())
	if err != nil {
		return err
	}

	return auth.ErrStepUpRequired.Derive(func(details *problem.Builder) {
		details.Extension("methods", methods)
	})
}

// secondFactorMethods lists the methods whose recent proof counts as a second
// factor: the authenticator, a recovery code and the registered methods.
func (factors *Factors) secondFactorMethods() []auth.Method {
	accepted := []auth.Method{auth.MethodOTP, MethodRecovery}
	for _, credential := range factors.extra {
		accepted = append(accepted, credential.Method)
	}

	return accepted
}

// freshOrRecovered returns the session identity of r when it is fresh
// ([auth.Guard.Fresh]) or proved a recovery code within the step-up window. The
// handlers that weaken two-factor use it instead of [web.Fresh]: a user who lost
// the authenticator logs in with a recovery code, which is no step-up method, so
// core's freshness would call them stale although they just proved a second
// factor, and they could not disable two-factor or replace their codes, the
// two things a user without the device needs to do.
func (factors *Factors) freshOrRecovered(guard *auth.Guard, r *http.Request) (auth.Identity, error) {
	identity, err := web.Session(r)
	if err != nil {
		return auth.Identity{}, err
	}
	if factors.hasRecent(identity, []auth.Method{MethodRecovery}) {
		return identity, nil
	}
	if err := guard.Fresh(r); err != nil {
		return auth.Identity{}, err
	}

	return identity, nil
}

// hasRecent reports whether identity proved one of the methods within the
// step-up window, by a factor this server stamped, never one from the future.
func (factors *Factors) hasRecent(identity auth.Identity, accepted []auth.Method) bool {
	now := time.Now()

	return slices.ContainsFunc(identity.Factors(), func(factor auth.Factor) bool {
		return slices.Contains(accepted, factor.Method) && !factor.At.IsZero() && !factor.At.After(now) && now.Sub(factor.At) <= factors.window
	})
}

// enrolledMethods lists the methods the confirm route accepts from subject: the
// authenticator, then the registered methods the user is enrolled in.
func (factors *Factors) enrolledMethods(ctx context.Context, subject string) ([]auth.Method, error) {
	methods := []auth.Method{auth.MethodOTP}
	for _, credential := range factors.extra {
		enrolled, err := credential.Enrolled(ctx, subject)
		if err != nil {
			return nil, err
		}
		if enrolled {
			methods = append(methods, credential.Method)
		}
	}

	return methods, nil
}

// SetupHandler returns the handler of POST /two-factor/setup. It starts
// enrollment and answers 200 with {"secret": "...", "uri": "otpauth://..."} for a
// QR code; the label is the user's email claim when the record has one, else the
// subject. It needs a browser session and a fresh login ([auth.Guard.Fresh]); a
// user without a step-up method is fresh right after any login, so a first
// enrollment needs nothing special. A user who is already enabled gets
// [ErrAlreadyEnabled] (409). Calling it again before enabling replaces the
// pending secret.
//
// Example:
//
//	web.Post("/two-factor/setup", factors.SetupHandler(guard), twofactor.SetupOperation)
func (factors *Factors) SetupHandler(guard *auth.Guard) router.Handler {
	return web.RequireJSON(func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		if err := web.Verified(r.Context(), factors.verified, identity.Subject()); err != nil {
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
	})
}

// EnableHandler returns the handler of POST /two-factor. The body is
// {"code": "123456"}, the first code of the pending authenticator. It enables
// two-factor, creates the recovery codes and answers 200 with
// {"recovery_codes": [...]}, the only time they are shown. Every other login of
// the user ends, so a session opened before the second factor existed cannot
// outlive it, and the session that enabled it records the code as a fresh
// factor, so the user stays fresh for the next credential change. A wrong code is
// [ErrInvalidCode] (422), too many are [ErrThrottled] (429), and a user who
// never ran setup gets [ErrNotEnrolling] (409). It needs a browser session and a
// fresh login.
//
// Example:
//
//	web.Post("/two-factor", factors.EnableHandler(guard), twofactor.EnableOperation)
func (factors *Factors) EnableHandler(guard *auth.Guard) router.Handler {
	return web.RequireJSON(func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		if err := web.Verified(r.Context(), factors.verified, identity.Subject()); err != nil {
			return err
		}
		body, err := web.Parse(r, codeSchema)
		if err != nil {
			return err
		}
		codes, err := factors.enable(guard, r, identity.Subject(), body.Code)
		if err != nil {
			return err
		}
		// Begin on the session of the same subject keeps the login and records
		// the code as a factor from now on, so the user stays fresh. It runs after
		// the transaction committed. The recovery codes are shown only once, so a
		// session-store failure here must not turn into an error that loses them;
		// the user is merely asked to confirm again before the next change.
		_, _ = guard.Begin(r, identity.Subject(), auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession})

		return response.JSON(w, http.StatusOK, codesBody{RecoveryCodes: codes})
	})
}

// enable activates the authenticator of subject, ends every other login and
// runs OnEnable, in one transaction: a session an attacker opened before the
// user had a second factor must not outlive it, and if ending it fails nothing
// was activated, so two-factor stays off and no recovery code was shown and
// lost. The request carries the transaction's context so the guard's reads join
// it instead of waiting for another connection.
func (factors *Factors) enable(guard *auth.Guard, r *http.Request, subject, code string) ([]string, error) {
	var codes []string
	err := factors.db.WithTransaction(r.Context(), func(ctx context.Context) error {
		var err error
		if codes, err = factors.Activate(ctx, subject, code); err != nil {
			return err
		}
		if err := guard.RevokeOthers(r.WithContext(ctx)); err != nil {
			return err
		}
		if factors.enabled == nil {
			return nil
		}

		return factors.enabled(ctx, subject)
	})

	return codes, err
}

// DisableHandler returns the handler of DELETE /two-factor. It removes the
// authenticator, a pending enrollment and the recovery codes, and answers 204.
// It needs a browser session and a fresh login, and in addition a recent
// authenticator or recovery code (see [Builder.StepUpWindow]): a password alone,
// however recent, answers [auth.ErrStepUpRequired] (403) with methods ["otp"],
// and the user proves a code with POST /confirm and retries. A recovery code
// used within the window also counts as the fresh login, so a user who lost the
// device can log in with a code and disable two-factor. A user without an
// authenticator gets [ErrNotEnabled] (409).
//
// Example:
//
//	web.Delete("/two-factor", factors.DisableHandler(guard), twofactor.DisableOperation)
func (factors *Factors) DisableHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := factors.freshOrRecovered(guard, r)
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
		if err := factors.requireSecondFactor(r.Context(), identity); err != nil {
			return err
		}
		if err := factors.Remove(r.Context(), identity.Subject()); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// RecoveryCodesHandler returns the handler of POST /two-factor/recovery-codes.
// It replaces the recovery codes and answers 200 with {"recovery_codes": [...]};
// the old codes stop working. It needs a browser session, a fresh login and a
// recent authenticator or recovery code, like [Factors.DisableHandler] (a
// recovery code used within the window also counts as the fresh login); a
// password alone is [auth.ErrStepUpRequired] (403) with methods ["otp"]. A user
// without an authenticator gets [ErrNotEnabled] (409).
//
// Example:
//
//	web.Post("/two-factor/recovery-codes", factors.RecoveryCodesHandler(guard), twofactor.RecoveryCodesOperation)
func (factors *Factors) RecoveryCodesHandler(guard *auth.Guard) router.Handler {
	return web.RequireJSON(func(w http.ResponseWriter, r *http.Request) error {
		identity, err := factors.freshOrRecovered(guard, r)
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
		if err := factors.requireSecondFactor(r.Context(), identity); err != nil {
			return err
		}
		codes, err := factors.Regenerate(r.Context(), identity.Subject())
		if err != nil {
			return err
		}

		return response.JSON(w, http.StatusOK, codesBody{RecoveryCodes: codes})
	})
}
