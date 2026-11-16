package access

import (
	"arkoren.dev/core/http/router"
)

// Mount registers the default routes of the enabled features on r. Only the
// routes of enabled features are mounted; "Always" routes need nothing.
//
//	Login       POST /register (with Registration), POST /login, POST /login/challenge,
//	            GET /login/pending, POST /logout
//	Step-up     POST /confirm
//	Email       POST /email/verify, POST /email/verify/resend
//	Password    POST /password/forgot, POST /password/reset, PUT /password
//	Two-factor  POST /two-factor/setup, POST /two-factor, DELETE /two-factor,
//	            POST /two-factor/recovery-codes
//	Tokens      GET /tokens, DELETE /tokens/{id}, POST /tokens (personal
//	            tokens), POST /device-tokens, DELETE /device-tokens/current
//	            (device tokens)
//	Sessions    GET /sessions, DELETE /sessions/{id}, DELETE /sessions (always)
//	Providers   GET /auth/<name>, GET and POST /auth/<name>/callback, and for
//	            native providers GET /auth/<name>/nonce, POST /auth/<name>/native
//	Connections GET /connections, POST /connections/{provider},
//	            DELETE /connections/{id}
//	Account     DELETE /account (built-in users only)
//
// The password routes need [Builder.Password]; of them, /register also needs
// [password.Builder.Registration] (off by default; New refuses it without users
// that can be created), and /password/forgot and /password/reset need [Builder.Deliver],
// because without it no user could receive a reset link. Mount skips a route
// whose requirement is missing instead of panicking, so it never panics for a
// configuration [New] accepted; the matching XHandler method still panics when
// asked for directly. The challenge and pending routes need a registered second factor,
// /confirm a registered step-up method, and the email routes the built-in users
// and [Builder.Deliver]. The token routes need [Builder.Tokens]: listing and
// revoking serve both kinds, POST /tokens needs [apitoken.Builder.PersonalTokens]
// and the two device routes [apitoken.Builder.DeviceTokens] (both default to
// on). /logout and the session routes are always mounted.
//
// r must already run [Access.Middleware]. The routes live at the paths above:
// the connect handler redirects to "/auth/<name>" and the providers' callbacks
// are mounted there, so do not mount under a prefix. To serve other paths, register
// the XHandler methods yourself; for another connect target use
// [federation.Links.ConnectHandler] on [Access.Links]. The connect, token,
// two-factor setup and enable handlers enforce the verified-email rule
// themselves, so they are gated on every path, including the package handlers
// of [Access.Links], [Access.Issuer] and [Access.Factors] used directly.
// Mounting twice, or on a
// router that already has these patterns, panics like any duplicate route.
//
// Example:
//
//	web := routes.With(iam.Middleware())
//	iam.Mount(web)
func (iam *Access) Mount(r *router.Router) {
	iam.mountPassword(r)
	iam.mountLogin(r)
	iam.mountEmail(r)
	iam.mountTwoFactor(r)
	iam.mountTokens(r)
	r.Get("/sessions", iam.ListSessionsHandler())
	r.Delete("/sessions/{id}", iam.DeleteSessionHandler())
	r.Delete("/sessions", iam.DeleteOtherSessionsHandler())
	iam.mountProviders(r)
	iam.mountConnections(r)
	if iam.directory != nil {
		r.Delete("/account", iam.DeleteAccountHandler())
	}
}

// mountPassword registers the password routes when [Builder.Password] is set:
// login and change always, registration when users can be created, and the
// forgot and reset routes when [Builder.Deliver] is set.
func (iam *Access) mountPassword(r *router.Router) {
	if iam.verifier == nil {
		return
	}
	r.Post("/login", iam.LoginHandler())
	r.Put("/password", iam.ChangePasswordHandler())
	if iam.verifier.Registration() {
		r.Post("/register", iam.RegisterHandler())
	}
	if iam.deliver != nil {
		r.Post("/password/reset", iam.ResetPasswordHandler())
		r.Post("/password/forgot", iam.ForgotPasswordHandler())
	}
}

// mountLogin registers the guard's routes: the challenge and pending routes
// with a second factor, /logout always and /confirm with a step-up method.
func (iam *Access) mountLogin(r *router.Router) {
	if iam.secondFactor {
		r.Post("/login/challenge", iam.ChallengeHandler())
		r.Get("/login/pending", iam.PendingHandler())
	}
	r.Post("/logout", iam.LogoutHandler())
	if iam.stepUp {
		r.Post("/confirm", iam.ConfirmHandler())
	}
}

// mountEmail registers the email verification routes with the built-in users
// and [Builder.Deliver].
func (iam *Access) mountEmail(r *router.Router) {
	if iam.directory == nil || iam.deliver == nil {
		return
	}
	r.Post("/email/verify", iam.VerifyEmailHandler())
	r.Post("/email/verify/resend", iam.ResendVerificationHandler())
}

// mountTwoFactor registers the two-factor routes when [Builder.TwoFactor] is
// set.
func (iam *Access) mountTwoFactor(r *router.Router) {
	if iam.factors == nil {
		return
	}
	r.Post("/two-factor/setup", iam.SetupTwoFactorHandler())
	r.Post("/two-factor", iam.EnableTwoFactorHandler())
	r.Delete("/two-factor", iam.DisableTwoFactorHandler())
	r.Post("/two-factor/recovery-codes", iam.RecoveryCodesHandler())
}

// mountTokens registers the token routes when [Builder.Tokens] is set.
func (iam *Access) mountTokens(r *router.Router) {
	if iam.issuer == nil {
		return
	}
	r.Get("/tokens", iam.ListTokensHandler())
	r.Delete("/tokens/{id}", iam.DeleteTokenHandler())
	if iam.issuer.PersonalTokens() {
		r.Post("/tokens", iam.CreateTokenHandler())
	}
	if iam.issuer.DeviceTokens() {
		r.Post("/device-tokens", iam.ExchangeHandler())
		r.Delete("/device-tokens/current", iam.DeleteDeviceTokenHandler())
	}
}

// mountConnections registers the connection routes when [Builder.Provider]
// added any provider.
func (iam *Access) mountConnections(r *router.Router) {
	if iam.links == nil {
		return
	}
	r.Get("/connections", iam.ListConnectionsHandler())
	r.Post("/connections/{provider}", iam.ConnectHandler())
	r.Delete("/connections/{id}", iam.DeleteConnectionHandler())
}
