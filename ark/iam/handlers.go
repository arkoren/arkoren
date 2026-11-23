package iam

import (
	"net/http"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/iam/account"
	"arkoren.dev/ark/iam/internal/web"
)

// need panics at composition when a handler is built for a feature that is not
// enabled, so the mistake surfaces at startup and not on the first request.
func need(enabled bool, handler, requirement string) {
	if !enabled {
		panic("access: " + handler + " requires " + requirement)
	}
}

// RegisterHandler returns the handler of POST /register; see
// [password.Verifier.RegisterHandler]. It always answers 202 and does not log
// the user in. With the built-in users it stores no job unless
// [account.Builder.Verification] is also configured: then a new user gets a
// verification email and, for a taken address, nothing is created and the owner
// gets a reset notice ([password.Builder.Reset], which registration with the
// built-in users requires). With Reset only, nothing is stored for a new or
// a taken address, so both cost the same. The work is the same on both paths,
// so the answer and its time do not tell them apart; a failing dispatch is
// logged and the answer stays 202. It panics unless [Builder.Password] is
// enabled with [password.Builder.Registration].
//
// Example:
//
//	web.Post("/register", identity.RegisterHandler(), password.RegisterOperation)
func (identity *IAM) RegisterHandler() router.Handler {
	need(identity.verifier != nil, "RegisterHandler", "Builder.Password")
	need(identity.registration, "RegisterHandler", "password.Builder.Registration, with password.Builder.Reset for the built-in users")

	return identity.verifier.RegisterHandler()
}

// LoginHandler returns the handler of POST /login; see
// [password.Verifier.LoginHandler]. It panics unless [Builder.Password] is
// enabled.
//
// Example:
//
//	web.Post("/login", identity.LoginHandler(), password.LoginOperation)
func (identity *IAM) LoginHandler() router.Handler {
	need(identity.verifier != nil, "LoginHandler", "Builder.Password")

	return identity.verifier.LoginHandler(identity.guard)
}

// ChallengeHandler returns the handler of POST /login/challenge, which finishes
// a parked login with one of the user's second-factor methods; see
// [auth.Guard.ChallengeHandler]. It panics unless a second-factor method is
// registered (two-factor enabled, or [Builder.SecondFactor]).
//
// Example:
//
//	web.Post("/login/challenge", identity.ChallengeHandler(), iam.ChallengeOperation)
func (identity *IAM) ChallengeHandler() router.Handler {
	need(identity.secondFactor, "ChallengeHandler", "Builder.TwoFactor or Builder.SecondFactor")

	return web.RequireJSON(identity.guard.ChallengeHandler())
}

// PendingHandler returns the handler of GET /login/pending, which tells a client
// with a parked login which methods it can use; see [auth.Guard.PendingHandler].
// It panics unless a second-factor method is registered.
//
// Example:
//
//	web.Get("/login/pending", identity.PendingHandler(), iam.PendingOperation)
func (identity *IAM) PendingHandler() router.Handler {
	need(identity.secondFactor, "PendingHandler", "Builder.TwoFactor or Builder.SecondFactor")

	return identity.guard.PendingHandler()
}

// LogoutHandler returns the handler of POST /logout, which ends the current
// login; see [auth.Guard.LogoutHandler]. It is always available.
//
// Example:
//
//	web.Post("/logout", identity.LogoutHandler(), iam.LogoutOperation)
func (identity *IAM) LogoutHandler() router.Handler {
	return web.RequireJSON(identity.guard.LogoutHandler())
}

// ConfirmHandler returns the handler of POST /confirm, which re-proves a
// logged-in user with a step-up method ("pwd" or "otp" by default) and so makes
// them fresh; see [auth.Guard.ConfirmHandler]. It panics unless a step-up
// method is registered (password or two-factor enabled, or [Builder.StepUp]).
//
// Example:
//
//	web.Post("/confirm", identity.ConfirmHandler(), iam.ConfirmOperation)
func (identity *IAM) ConfirmHandler() router.Handler {
	need(identity.stepUp, "ConfirmHandler", "Builder.Password, Builder.TwoFactor or Builder.StepUp")

	return web.RequireJSON(identity.guard.ConfirmHandler())
}

// VerifyEmailHandler returns the handler of POST /email/verify; see
// [account.Directory.VerifyHandler]. It needs a browser session of the account
// the link belongs to (the user opens the link while logged in), so someone
// else's click cannot verify an account they do not hold. It panics without the
// built-in directory or without [account.Builder.Verification].
//
// Example:
//
//	web.Post("/email/verify", identity.VerifyEmailHandler(), account.VerifyOperation)
func (identity *IAM) VerifyEmailHandler() router.Handler {
	need(identity.verification(), "VerifyEmailHandler", "the built-in users table and account.Builder.Verification")

	return identity.directory.VerifyHandler()
}

// ResendVerificationHandler returns the handler of POST /email/verify/resend;
// see [account.Directory.ResendHandler]. It panics without the built-in
// directory or without [account.Builder.Verification]. It is throttled per user
// and per client address and stores the work as a job for the worker that
// [IAM.RegisterJobs] registers.
//
// Example:
//
//	web.Post("/email/verify/resend", identity.ResendVerificationHandler(), account.ResendOperation)
func (identity *IAM) ResendVerificationHandler() router.Handler {
	need(identity.verification(), "ResendVerificationHandler", "the built-in users table and account.Builder.Verification")

	return identity.directory.ResendHandler()
}

// ForgotPasswordHandler returns the handler of POST /password/forgot; see
// [password.Verifier.ForgotHandler]. It panics unless [Builder.Password] is
// enabled with [password.Builder.Reset]. The work is stored as a job for the
// worker that [IAM.RegisterJobs] registers, and the answer is an empty 202
// whether or not the user exists.
//
// Example:
//
//	web.Post("/password/forgot", identity.ForgotPasswordHandler(), password.ForgotOperation)
func (identity *IAM) ForgotPasswordHandler() router.Handler {
	need(identity.reset(), "ForgotPasswordHandler", "Builder.Password and password.Builder.Reset")

	return identity.verifier.ForgotHandler()
}

// ResetPasswordHandler returns the handler of POST /password/reset; see
// [password.Verifier.ResetHandler]. It panics unless [Builder.Password] is
// enabled with [password.Builder.Reset].
//
// Example:
//
//	web.Post("/password/reset", identity.ResetPasswordHandler(), password.ResetOperation)
func (identity *IAM) ResetPasswordHandler() router.Handler {
	need(identity.reset(), "ResetPasswordHandler", "Builder.Password and password.Builder.Reset")

	return identity.verifier.ResetHandler(identity.guard)
}

// reset reports whether password reset is configured.
func (identity *IAM) reset() bool {
	return identity.verifier != nil && identity.verifier.ResetEnabled()
}

// verification reports whether email verification is configured: the built-in
// users with [account.Builder.Verification].
func (identity *IAM) verification() bool {
	return identity.directory != nil && identity.directory.VerificationEnabled()
}

// ChangePasswordHandler returns the handler of PUT /password; see
// [password.Verifier.ChangeHandler]. It panics unless [Builder.Password] is
// enabled.
//
// Example:
//
//	web.Put("/password", identity.ChangePasswordHandler(), password.ChangeOperation)
func (identity *IAM) ChangePasswordHandler() router.Handler {
	need(identity.verifier != nil, "ChangePasswordHandler", "Builder.Password")

	return identity.verifier.ChangeHandler(identity.guard)
}

// SetupTwoFactorHandler returns the handler of POST /two-factor/setup; see
// [twofactor.Factors.SetupHandler]. It panics unless [Builder.TwoFactor] is
// enabled.
//
// An unverified email is 403 [account.ErrEmailUnverified]: whoever registered
// an address first must not attach this before its owner proves it (with your
// own users, see [Users.Verified]).
//
// Example:
//
//	web.Post("/two-factor/setup", identity.SetupTwoFactorHandler(), twofactor.SetupOperation)
func (identity *IAM) SetupTwoFactorHandler() router.Handler {
	need(identity.factors != nil, "SetupTwoFactorHandler", "Builder.TwoFactor")

	return identity.factors.SetupHandler(identity.guard)
}

// EnableTwoFactorHandler returns the handler of POST /two-factor; see
// [twofactor.Factors.EnableHandler]. It panics unless [Builder.TwoFactor] is
// enabled.
//
// An unverified email is 403 [account.ErrEmailUnverified] (see
// [Users.Verified] with your own users). Enabling ends the user's other logins.
//
// Example:
//
//	web.Post("/two-factor", identity.EnableTwoFactorHandler(), twofactor.EnableOperation)
func (identity *IAM) EnableTwoFactorHandler() router.Handler {
	need(identity.factors != nil, "EnableTwoFactorHandler", "Builder.TwoFactor")

	return identity.factors.EnableHandler(identity.guard)
}

// DisableTwoFactorHandler returns the handler of DELETE /two-factor; see
// [twofactor.Factors.DisableHandler]. It panics unless [Builder.TwoFactor] is
// enabled.
//
// Example:
//
//	web.Delete("/two-factor", identity.DisableTwoFactorHandler(), twofactor.DisableOperation)
func (identity *IAM) DisableTwoFactorHandler() router.Handler {
	need(identity.factors != nil, "DisableTwoFactorHandler", "Builder.TwoFactor")

	return identity.factors.DisableHandler(identity.guard)
}

// RecoveryCodesHandler returns the handler of POST /two-factor/recovery-codes;
// see [twofactor.Factors.RecoveryCodesHandler]. It panics unless
// [Builder.TwoFactor] is enabled.
//
// Example:
//
//	web.Post("/two-factor/recovery-codes", identity.RecoveryCodesHandler(), twofactor.RecoveryCodesOperation)
func (identity *IAM) RecoveryCodesHandler() router.Handler {
	need(identity.factors != nil, "RecoveryCodesHandler", "Builder.TwoFactor")

	return identity.factors.RecoveryCodesHandler(identity.guard)
}

// ListTokensHandler returns the handler of GET /tokens; see
// [apitoken.Issuer.ListHandler]. It panics unless [Builder.Tokens] is enabled.
//
// Example:
//
//	web.Get("/tokens", identity.ListTokensHandler(), apitoken.ListOperation)
func (identity *IAM) ListTokensHandler() router.Handler {
	need(identity.issuer != nil, "ListTokensHandler", "Builder.Tokens")

	return identity.issuer.ListHandler()
}

// CreateTokenHandler returns the handler of POST /tokens; see
// [apitoken.Issuer.CreateHandler]. It panics unless [Builder.Tokens] is enabled
// with [apitoken.Builder.PersonalTokens] (the default). A scope the user may not
// hold is 403 [apitoken.ErrScopeNotGranted].
//
// An unverified email is 403 [account.ErrEmailUnverified] (see
// [Users.Verified] with your own users).
//
// Example:
//
//	web.Post("/tokens", identity.CreateTokenHandler(), apitoken.CreateOperation)
func (identity *IAM) CreateTokenHandler() router.Handler {
	need(identity.issuer != nil, "CreateTokenHandler", "Builder.Tokens")
	need(identity.issuer.PersonalTokens(), "CreateTokenHandler", "apitoken.Builder.PersonalTokens")

	return identity.issuer.CreateHandler(identity.guard)
}

// DeleteTokenHandler returns the handler of DELETE /tokens/{id}; see
// [apitoken.Issuer.DeleteHandler]. It panics unless [Builder.Tokens] is
// enabled.
//
// Example:
//
//	web.Delete("/tokens/{id}", identity.DeleteTokenHandler(), apitoken.DeleteOperation)
func (identity *IAM) DeleteTokenHandler() router.Handler {
	need(identity.issuer != nil, "DeleteTokenHandler", "Builder.Tokens")

	return identity.issuer.DeleteHandler()
}

// ExchangeHandler returns the handler of POST /device-tokens, which trades a
// fresh login for a device token and ends the session; see
// [apitoken.Issuer.ExchangeHandler]. It panics unless [Builder.Tokens] is
// enabled with [apitoken.Builder.DeviceTokens] (the default).
//
// An unverified email is 403 [account.ErrEmailUnverified] (see
// [Users.Verified] with your own users).
//
// Example:
//
//	web.Post("/device-tokens", identity.ExchangeHandler(), apitoken.ExchangeOperation)
func (identity *IAM) ExchangeHandler() router.Handler {
	need(identity.issuer != nil, "ExchangeHandler", "Builder.Tokens")
	need(identity.issuer.DeviceTokens(), "ExchangeHandler", "apitoken.Builder.DeviceTokens")

	return identity.issuer.ExchangeHandler(identity.guard)
}

// DeleteDeviceTokenHandler returns the handler of DELETE /device-tokens/current,
// which revokes the presented token (a device's logout); see
// [apitoken.Issuer.DeleteCurrentHandler]. It panics unless [Builder.Tokens] is
// enabled with [apitoken.Builder.DeviceTokens] (the default).
//
// Example:
//
//	api.Delete("/device-tokens/current", identity.DeleteDeviceTokenHandler(), apitoken.DeleteCurrentOperation)
func (identity *IAM) DeleteDeviceTokenHandler() router.Handler {
	need(identity.issuer != nil, "DeleteDeviceTokenHandler", "Builder.Tokens")
	need(identity.issuer.DeviceTokens(), "DeleteDeviceTokenHandler", "apitoken.Builder.DeviceTokens")

	return identity.issuer.DeleteCurrentHandler()
}

// ListSessionsHandler returns the handler of GET /sessions: 200 with the user's
// logins (device, address, times; the request's own is marked current) as a JSON
// array. It needs a browser session. It is always available.
//
// Example:
//
//	web.Get("/sessions", identity.ListSessionsHandler(), iam.ListSessionsOperation)
func (identity *IAM) ListSessionsHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if _, err := web.Session(r); err != nil {
			return err
		}
		logins, err := identity.guard.Logins(r)
		if err != nil {
			return err
		}

		return response.JSON(w, http.StatusOK, logins)
	}
}

// DeleteSessionHandler returns the handler of DELETE /sessions/{id}: it ends
// the login with that id (another device, or this one) and answers 204; an
// unknown id changes nothing. It needs a browser session. It is always
// available.
//
// Example:
//
//	web.Delete("/sessions/{id}", identity.DeleteSessionHandler(), iam.DeleteSessionOperation)
func (identity *IAM) DeleteSessionHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user, err := web.Session(r)
		if err != nil {
			return err
		}
		if err := identity.guard.Revoke(r.Context(), user.Subject(), r.PathValue("id")); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// DeleteOtherSessionsHandler returns the handler of DELETE /sessions, which
// ends every login of the user except the one that made the request and answers
// 204. It needs a browser session. It is always available.
//
// Example:
//
//	web.Delete("/sessions", identity.DeleteOtherSessionsHandler(), iam.DeleteOtherSessionsOperation)
func (identity *IAM) DeleteOtherSessionsHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if _, err := web.Session(r); err != nil {
			return err
		}
		if err := identity.guard.RevokeOthers(r); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// ListConnectionsHandler returns the handler of GET /connections; see
// [federation.Links.ListHandler]. It panics unless [Builder.Provider] is used.
//
// Example:
//
//	web.Get("/connections", identity.ListConnectionsHandler(), federation.ListOperation)
func (identity *IAM) ListConnectionsHandler() router.Handler {
	need(identity.links != nil, "ListConnectionsHandler", "Builder.Provider")

	return identity.links.ListHandler()
}

// ConnectHandler returns the handler of POST /connections/{provider}: it starts
// connecting the provider to the logged-in user and redirects (303) to
// "/auth/<name>"; see [federation.Links.ConnectHandler]. It panics unless
// [Builder.Provider] is used.
//
// An unverified email is 403 [account.ErrEmailUnverified] (see
// [Users.Verified] with your own users).
//
// Example:
//
//	web.Post("/connections/{provider}", identity.ConnectHandler(), federation.ConnectOperation)
func (identity *IAM) ConnectHandler() router.Handler {
	need(identity.links != nil, "ConnectHandler", "Builder.Provider")

	return identity.links.ConnectHandler(identity.guard, identity.connectTargets())
}

// DeleteConnectionHandler returns the handler of DELETE /connections/{id}; see
// [federation.Links.DeleteHandler]. It panics unless [Builder.Provider] is used.
//
// Example:
//
//	web.Delete("/connections/{id}", identity.DeleteConnectionHandler(), federation.DeleteOperation)
func (identity *IAM) DeleteConnectionHandler() router.Handler {
	need(identity.links != nil, "DeleteConnectionHandler", "Builder.Provider")

	return identity.links.DeleteHandler(identity.guard)
}

// DeleteAccountHandler returns the handler of DELETE /account: it deletes the
// logged-in user and answers 204. It needs a browser session and a fresh login
// ([auth.Guard.Fresh]). The user's row and every package's rows (passwords,
// two-factor, tokens, connections, login records) are removed in one
// transaction by the forget hooks, and a failure of any leaves the user in
// place. It panics without the built-in directory: with [Builder.Users] the
// application owns user deletion and calls
// [auth.UserProvider.Forget] itself.
//
// Example:
//
//	web.Delete("/account", identity.DeleteAccountHandler(), iam.DeleteAccountOperation)
func (identity *IAM) DeleteAccountHandler() router.Handler {
	need(identity.directory != nil, "DeleteAccountHandler", "the built-in users table")

	return func(w http.ResponseWriter, r *http.Request) error {
		user, err := web.Fresh(identity.guard, r)
		if err != nil {
			return err
		}
		if err := identity.directory.Delete(r.Context(), user.Subject()); err != nil {
			return err
		}
		// The user and their login records are gone; ending the request's own
		// session is cleanup, and the session no longer authenticates anyway.
		_ = identity.guard.Logout(r)

		return response.Status(w, http.StatusNoContent)
	}
}

// RequireVerified returns middleware that admits only users whose email is
// verified. An anonymous request returns [auth.ErrUnauthenticated]; an
// unverified user [account.ErrEmailUnverified] (403). Install it after auth
// middleware.
//
// With the built-in users it is [account.RequireVerified]: it judges by the
// "email_verified" claim of the user the guard read from the directory for this
// request, so a user who just verified passes at once. With [Builder.Users] it
// asks [Users.Verified] for the user's current state on every request, so the
// answer is as fresh as the application's own lookup; an error of that function
// passes through. It panics at
// composition only when there is neither the built-in directory nor
// [Users.Verified].
//
// Example:
//
//	posts := web.With(auth.Require(), identity.RequireVerified())
func (identity *IAM) RequireVerified() router.Middleware {
	need(identity.verified != nil, "RequireVerified", "the built-in users table or Users.Verified")
	if identity.directory != nil {
		return account.RequireVerified()
	}

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			user, err := request.Identity(r)
			if err != nil {
				return err
			}
			if err := web.Verified(r.Context(), identity.verified, user.Subject()); err != nil {
				return err
			}

			return next(w, r)
		}
	}
}
