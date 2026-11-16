package access

import (
	"net/http"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/access/account"
	"arkoren.dev/ark/access/internal/web"
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
// the user in. With the built-in users and [account.Builder.Verification] it
// dispatches a verification email for a new user; for a taken address it creates
// nothing and dispatches, in the same way, a reset notice to the owner
// ([password.Builder.Reset]) or, without Reset, a verification email for the
// owner. Without Verification nothing is dispatched on either path. The work is
// the same on both paths, so the answer and its time do not tell them apart; a
// failing dispatch is logged and the answer stays 202. It panics unless
// [Builder.Password] is enabled with [password.Builder.Registration].
//
// Example:
//
//	web.Post("/register", iam.RegisterHandler())
func (iam *Access) RegisterHandler() router.Handler {
	need(iam.verifier != nil, "RegisterHandler", "Builder.Password")
	need(iam.verifier.Registration(), "RegisterHandler", "password.Builder.Registration")

	return iam.verifier.RegisterHandler()
}

// LoginHandler returns the handler of POST /login; see
// [password.Verifier.LoginHandler]. It panics unless [Builder.Password] is
// enabled.
//
// Example:
//
//	web.Post("/login", iam.LoginHandler())
func (iam *Access) LoginHandler() router.Handler {
	need(iam.verifier != nil, "LoginHandler", "Builder.Password")

	return iam.verifier.LoginHandler(iam.guard)
}

// ChallengeHandler returns the handler of POST /login/challenge, which finishes
// a parked login with one of the user's second-factor methods; see
// [auth.Guard.ChallengeHandler]. It panics unless a second-factor method is
// registered (two-factor enabled, or [Builder.SecondFactor]).
//
// Example:
//
//	web.Post("/login/challenge", iam.ChallengeHandler())
func (iam *Access) ChallengeHandler() router.Handler {
	need(iam.secondFactor, "ChallengeHandler", "Builder.TwoFactor or Builder.SecondFactor")

	return web.RequireJSON(iam.guard.ChallengeHandler())
}

// PendingHandler returns the handler of GET /login/pending, which tells a client
// with a parked login which methods it can use; see [auth.Guard.PendingHandler].
// It panics unless a second-factor method is registered.
//
// Example:
//
//	web.Get("/login/pending", iam.PendingHandler())
func (iam *Access) PendingHandler() router.Handler {
	need(iam.secondFactor, "PendingHandler", "Builder.TwoFactor or Builder.SecondFactor")

	return iam.guard.PendingHandler()
}

// LogoutHandler returns the handler of POST /logout, which ends the current
// login; see [auth.Guard.LogoutHandler]. It is always available.
//
// Example:
//
//	web.Post("/logout", iam.LogoutHandler())
func (iam *Access) LogoutHandler() router.Handler {
	return web.RequireJSON(iam.guard.LogoutHandler())
}

// ConfirmHandler returns the handler of POST /confirm, which re-proves a
// logged-in user with a step-up method ("pwd" or "otp" by default) and so makes
// them fresh; see [auth.Guard.ConfirmHandler]. It panics unless a step-up
// method is registered (password or two-factor enabled, or [Builder.StepUp]).
//
// Example:
//
//	web.Post("/confirm", iam.ConfirmHandler())
func (iam *Access) ConfirmHandler() router.Handler {
	need(iam.stepUp, "ConfirmHandler", "Builder.Password, Builder.TwoFactor or Builder.StepUp")

	return web.RequireJSON(iam.guard.ConfirmHandler())
}

// VerifyEmailHandler returns the handler of POST /email/verify; see
// [account.Directory.VerifyHandler]. It needs a browser session of the account
// the link belongs to (the user opens the link while logged in), so someone
// else's click cannot verify an account they do not hold. It panics without the
// built-in directory or without [account.Builder.Verification].
//
// Example:
//
//	web.Post("/email/verify", iam.VerifyEmailHandler())
func (iam *Access) VerifyEmailHandler() router.Handler {
	need(iam.verification(), "VerifyEmailHandler", "the built-in users table and account.Builder.Verification")

	return iam.directory.VerifyHandler()
}

// ResendVerificationHandler returns the handler of POST /email/verify/resend;
// see [account.Directory.ResendHandler]. It panics without the built-in
// directory or without [account.Builder.Verification]. It is throttled per user
// and per client address and dispatches the work for the worker that runs
// [Access.SendVerification].
//
// Example:
//
//	web.Post("/email/verify/resend", iam.ResendVerificationHandler())
func (iam *Access) ResendVerificationHandler() router.Handler {
	need(iam.verification(), "ResendVerificationHandler", "the built-in users table and account.Builder.Verification")

	return iam.directory.ResendHandler()
}

// ForgotPasswordHandler returns the handler of POST /password/forgot; see
// [password.Verifier.ForgotHandler]. It panics unless [Builder.Password] is
// enabled with [password.Builder.Reset]. The work is dispatched for the worker
// that runs [Access.SendReset], and the answer is an empty 202 whether or not
// the user exists.
//
// Example:
//
//	web.Post("/password/forgot", iam.ForgotPasswordHandler())
func (iam *Access) ForgotPasswordHandler() router.Handler {
	need(iam.reset(), "ForgotPasswordHandler", "Builder.Password and password.Builder.Reset")

	return iam.verifier.ForgotHandler()
}

// ResetPasswordHandler returns the handler of POST /password/reset; see
// [password.Verifier.ResetHandler]. It panics unless [Builder.Password] is
// enabled with [password.Builder.Reset].
//
// Example:
//
//	web.Post("/password/reset", iam.ResetPasswordHandler())
func (iam *Access) ResetPasswordHandler() router.Handler {
	need(iam.reset(), "ResetPasswordHandler", "Builder.Password and password.Builder.Reset")

	return iam.verifier.ResetHandler(iam.guard)
}

// reset reports whether password reset is configured.
func (iam *Access) reset() bool {
	return iam.verifier != nil && iam.verifier.ResetEnabled()
}

// verification reports whether email verification is configured: the built-in
// users with [account.Builder.Verification].
func (iam *Access) verification() bool {
	return iam.directory != nil && iam.directory.VerificationEnabled()
}

// ChangePasswordHandler returns the handler of PUT /password; see
// [password.Verifier.ChangeHandler]. It panics unless [Builder.Password] is
// enabled.
//
// Example:
//
//	web.Put("/password", iam.ChangePasswordHandler())
func (iam *Access) ChangePasswordHandler() router.Handler {
	need(iam.verifier != nil, "ChangePasswordHandler", "Builder.Password")

	return iam.verifier.ChangeHandler(iam.guard)
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
//	web.Post("/two-factor/setup", iam.SetupTwoFactorHandler())
func (iam *Access) SetupTwoFactorHandler() router.Handler {
	need(iam.factors != nil, "SetupTwoFactorHandler", "Builder.TwoFactor")

	return iam.factors.SetupHandler(iam.guard)
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
//	web.Post("/two-factor", iam.EnableTwoFactorHandler())
func (iam *Access) EnableTwoFactorHandler() router.Handler {
	need(iam.factors != nil, "EnableTwoFactorHandler", "Builder.TwoFactor")

	return iam.factors.EnableHandler(iam.guard)
}

// DisableTwoFactorHandler returns the handler of DELETE /two-factor; see
// [twofactor.Factors.DisableHandler]. It panics unless [Builder.TwoFactor] is
// enabled.
//
// Example:
//
//	web.Delete("/two-factor", iam.DisableTwoFactorHandler())
func (iam *Access) DisableTwoFactorHandler() router.Handler {
	need(iam.factors != nil, "DisableTwoFactorHandler", "Builder.TwoFactor")

	return iam.factors.DisableHandler(iam.guard)
}

// RecoveryCodesHandler returns the handler of POST /two-factor/recovery-codes;
// see [twofactor.Factors.RecoveryCodesHandler]. It panics unless
// [Builder.TwoFactor] is enabled.
//
// Example:
//
//	web.Post("/two-factor/recovery-codes", iam.RecoveryCodesHandler())
func (iam *Access) RecoveryCodesHandler() router.Handler {
	need(iam.factors != nil, "RecoveryCodesHandler", "Builder.TwoFactor")

	return iam.factors.RecoveryCodesHandler(iam.guard)
}

// ListTokensHandler returns the handler of GET /tokens; see
// [apitoken.Issuer.ListHandler]. It panics unless [Builder.Tokens] is enabled.
//
// Example:
//
//	web.Get("/tokens", iam.ListTokensHandler())
func (iam *Access) ListTokensHandler() router.Handler {
	need(iam.issuer != nil, "ListTokensHandler", "Builder.Tokens")

	return iam.issuer.ListHandler()
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
//	web.Post("/tokens", iam.CreateTokenHandler())
func (iam *Access) CreateTokenHandler() router.Handler {
	need(iam.issuer != nil, "CreateTokenHandler", "Builder.Tokens")
	need(iam.issuer.PersonalTokens(), "CreateTokenHandler", "apitoken.Builder.PersonalTokens")

	return iam.issuer.CreateHandler(iam.guard)
}

// DeleteTokenHandler returns the handler of DELETE /tokens/{id}; see
// [apitoken.Issuer.DeleteHandler]. It panics unless [Builder.Tokens] is
// enabled.
//
// Example:
//
//	web.Delete("/tokens/{id}", iam.DeleteTokenHandler())
func (iam *Access) DeleteTokenHandler() router.Handler {
	need(iam.issuer != nil, "DeleteTokenHandler", "Builder.Tokens")

	return iam.issuer.DeleteHandler()
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
//	web.Post("/device-tokens", iam.ExchangeHandler())
func (iam *Access) ExchangeHandler() router.Handler {
	need(iam.issuer != nil, "ExchangeHandler", "Builder.Tokens")
	need(iam.issuer.DeviceTokens(), "ExchangeHandler", "apitoken.Builder.DeviceTokens")

	return iam.issuer.ExchangeHandler(iam.guard)
}

// DeleteDeviceTokenHandler returns the handler of DELETE /device-tokens/current,
// which revokes the presented token (a device's logout); see
// [apitoken.Issuer.DeleteCurrentHandler]. It panics unless [Builder.Tokens] is
// enabled with [apitoken.Builder.DeviceTokens] (the default).
//
// Example:
//
//	api.Delete("/device-tokens/current", iam.DeleteDeviceTokenHandler())
func (iam *Access) DeleteDeviceTokenHandler() router.Handler {
	need(iam.issuer != nil, "DeleteDeviceTokenHandler", "Builder.Tokens")
	need(iam.issuer.DeviceTokens(), "DeleteDeviceTokenHandler", "apitoken.Builder.DeviceTokens")

	return iam.issuer.DeleteCurrentHandler()
}

// ListSessionsHandler returns the handler of GET /sessions: 200 with the user's
// logins (device, address, times; the request's own is marked current) as a JSON
// array. It needs a browser session. It is always available.
//
// Example:
//
//	web.Get("/sessions", iam.ListSessionsHandler())
func (iam *Access) ListSessionsHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if _, err := web.Session(r); err != nil {
			return err
		}
		logins, err := iam.guard.Logins(r)
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
//	web.Delete("/sessions/{id}", iam.DeleteSessionHandler())
func (iam *Access) DeleteSessionHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Session(r)
		if err != nil {
			return err
		}
		if err := iam.guard.Revoke(r.Context(), identity.Subject(), r.PathValue("id")); err != nil {
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
//	web.Delete("/sessions", iam.DeleteOtherSessionsHandler())
func (iam *Access) DeleteOtherSessionsHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if _, err := web.Session(r); err != nil {
			return err
		}
		if err := iam.guard.RevokeOthers(r); err != nil {
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
//	web.Get("/connections", iam.ListConnectionsHandler())
func (iam *Access) ListConnectionsHandler() router.Handler {
	need(iam.links != nil, "ListConnectionsHandler", "Builder.Provider")

	return iam.links.ListHandler()
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
//	web.Post("/connections/{provider}", iam.ConnectHandler())
func (iam *Access) ConnectHandler() router.Handler {
	need(iam.links != nil, "ConnectHandler", "Builder.Provider")

	return iam.links.ConnectHandler(iam.guard, iam.connectTargets())
}

// DeleteConnectionHandler returns the handler of DELETE /connections/{id}; see
// [federation.Links.DeleteHandler]. It panics unless [Builder.Provider] is used.
//
// Example:
//
//	web.Delete("/connections/{id}", iam.DeleteConnectionHandler())
func (iam *Access) DeleteConnectionHandler() router.Handler {
	need(iam.links != nil, "DeleteConnectionHandler", "Builder.Provider")

	return iam.links.DeleteHandler(iam.guard)
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
//	web.Delete("/account", iam.DeleteAccountHandler())
func (iam *Access) DeleteAccountHandler() router.Handler {
	need(iam.directory != nil, "DeleteAccountHandler", "the built-in users table")

	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(iam.guard, r)
		if err != nil {
			return err
		}
		if err := iam.directory.Delete(r.Context(), identity.Subject()); err != nil {
			return err
		}
		// The user and their login records are gone; ending the request's own
		// session is cleanup, and the session no longer authenticates anyway.
		_ = iam.guard.Logout(r)

		return response.Status(w, http.StatusNoContent)
	}
}

// RequireVerified returns middleware that admits only users whose email is
// verified. An anonymous request returns [auth.ErrUnauthenticated]; an
// unverified user [account.ErrEmailUnverified] (403). Install it after auth
// middleware.
//
// With the built-in users it is [account.RequireVerified]: it judges by the
// "email_verified" claim the session recorded at login, so a user who verified
// since then passes after logging in again. With [Builder.Users] it asks
// [Users.Verified] for the user's current state on every request, so a user who
// just verified passes at once, and the answer is as fresh as the application's
// own lookup; an error of that function passes through. It panics at
// composition only when there is neither the built-in directory nor
// [Users.Verified].
//
// Example:
//
//	posts := web.With(auth.Require(), iam.RequireVerified())
func (iam *Access) RequireVerified() router.Middleware {
	need(iam.verified != nil, "RequireVerified", "the built-in users table or Users.Verified")
	if iam.directory != nil {
		return account.RequireVerified()
	}

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			identity, err := request.Identity(r)
			if err != nil {
				return err
			}
			if err := web.Verified(r.Context(), iam.verified, identity.Subject()); err != nil {
				return err
			}

			return next(w, r)
		}
	}
}
