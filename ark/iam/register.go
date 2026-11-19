package iam

import (
	"arkoren.dev/core/http/router"
)

// Register adds the default routes of the enabled features to r. Only the
// routes of enabled features are registered; "Always" routes need nothing.
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
//	Permissions GET /me/permissions (with SelfService), GET /users/{subject}/permissions,
//	            PUT and DELETE /users/{subject}/permissions/{name} and
//	            /users/{subject}/roles/{name} (with Management)
//	Account     GET /me (with Me), DELETE /account (built-in users only)
//
// The password routes need [Builder.Password]; of them, /register also needs
// [password.Builder.Registration] (off by default; New refuses it without users
// that can be created), and /password/forgot and /password/reset need
// [password.Builder.Reset], because without it no user could receive a reset
// link. Register skips a route
// whose requirement is missing instead of panicking, so it never panics for a
// configuration [New] accepted; the matching XHandler method still panics when
// asked for directly. The challenge and pending routes need a registered second factor,
// /confirm a registered step-up method, and the email routes the built-in users
// and [account.Builder.Verification]. The token routes need [Builder.Tokens]: listing and
// revoking serve both kinds, POST /tokens needs [apitoken.Builder.PersonalTokens]
// and the two device routes [apitoken.Builder.DeviceTokens] (both default to
// on). The permission routes need [Builder.Permissions]: /me/permissions also
// [permission.Builder.SelfService] and the /users/{subject} routes
// [permission.Builder.Management]. GET /me needs [Builder.Me]. /logout and the
// session routes are always registered.
//
// Register never installs [IAM.Middleware]; r must already run it. The
// middleware identifies the user behind every request, session, token or
// device, and it belongs on every route of the application that needs to know
// who is calling, not only on IAM's. Install it once, on the router that
// carries all of those routes, and derive IAM's routes from that router, so
// the application's own routes and IAM's share one identification step and
// installing it twice (or forgetting it here) cannot happen by accident.
//
// The routes live at the paths above, at the root. The connect handler
// redirects to the absolute path "/auth/<name>" and the providers' callbacks
// are registered there, so r must not carry a path prefix: under a prefix the
// redirects would point at routes that do not exist, and nothing reports it.
// To serve other paths, register the XHandler methods yourself; for another
// connect target use [federation.Links.ConnectHandler] on [IAM.Links]. The
// XHandler methods stay public for custom paths, for a single feature, and
// for per-route middleware. The connect, token, two-factor setup and enable
// handlers enforce the verified-email rule themselves, so they are gated on
// every path, including the package handlers of [IAM.Links], [IAM.Issuer] and
// [IAM.Factors] used directly. Registering twice, or on a router that already
// has these patterns, panics like any duplicate route.
//
// Register is a plain func(*router.Router) method, so its method value fits
// [router.Router.Group].
//
// Example:
//
//	web := routes.With(identity.Middleware())
//	web.Group("/", identity.Register)
func (identity *IAM) Register(r *router.Router) {
	identity.registerPassword(r)
	identity.registerLogin(r)
	identity.registerEmail(r)
	identity.registerTwoFactor(r)
	identity.registerTokens(r)
	identity.registerPermissions(r)
	r.Get("/sessions", identity.ListSessionsHandler())
	r.Delete("/sessions/{id}", identity.DeleteSessionHandler())
	r.Delete("/sessions", identity.DeleteOtherSessionsHandler())
	identity.registerProviders(r)
	identity.registerConnections(r)
	if identity.me != nil {
		r.Get("/me", identity.MeHandler())
	}
	if identity.directory != nil {
		r.Delete("/account", identity.DeleteAccountHandler())
	}
}

// registerPassword registers the password routes when [Builder.Password] is set:
// login and change always, registration when users can be created, and the
// forgot and reset routes when [password.Builder.Reset] is set.
func (identity *IAM) registerPassword(r *router.Router) {
	if identity.verifier == nil {
		return
	}
	r.Post("/login", identity.LoginHandler())
	r.Put("/password", identity.ChangePasswordHandler())
	if identity.registration {
		r.Post("/register", identity.RegisterHandler())
	}
	if identity.reset() {
		r.Post("/password/reset", identity.ResetPasswordHandler())
		r.Post("/password/forgot", identity.ForgotPasswordHandler())
	}
}

// registerLogin registers the guard's routes: the challenge and pending routes
// with a second factor, /logout always and /confirm with a step-up method.
func (identity *IAM) registerLogin(r *router.Router) {
	if identity.secondFactor {
		r.Post("/login/challenge", identity.ChallengeHandler())
		r.Get("/login/pending", identity.PendingHandler())
	}
	r.Post("/logout", identity.LogoutHandler())
	if identity.stepUp {
		r.Post("/confirm", identity.ConfirmHandler())
	}
}

// registerEmail registers the email verification routes with the built-in users
// and [account.Builder.Verification].
func (identity *IAM) registerEmail(r *router.Router) {
	if !identity.verification() {
		return
	}
	r.Post("/email/verify", identity.VerifyEmailHandler())
	r.Post("/email/verify/resend", identity.ResendVerificationHandler())
}

// registerTwoFactor registers the two-factor routes when [Builder.TwoFactor] is
// set.
func (identity *IAM) registerTwoFactor(r *router.Router) {
	if identity.factors == nil {
		return
	}
	r.Post("/two-factor/setup", identity.SetupTwoFactorHandler())
	r.Post("/two-factor", identity.EnableTwoFactorHandler())
	r.Delete("/two-factor", identity.DisableTwoFactorHandler())
	r.Post("/two-factor/recovery-codes", identity.RecoveryCodesHandler())
}

// registerTokens registers the token routes when [Builder.Tokens] is set.
func (identity *IAM) registerTokens(r *router.Router) {
	if identity.issuer == nil {
		return
	}
	r.Get("/tokens", identity.ListTokensHandler())
	r.Delete("/tokens/{id}", identity.DeleteTokenHandler())
	if identity.issuer.PersonalTokens() {
		r.Post("/tokens", identity.CreateTokenHandler())
	}
	if identity.issuer.DeviceTokens() {
		r.Post("/device-tokens", identity.ExchangeHandler())
		r.Delete("/device-tokens/current", identity.DeleteDeviceTokenHandler())
	}
}

// registerPermissions registers the permission routes the store asked for:
// GET /me/permissions with [permission.Builder.SelfService], and the
// /users/{subject} routes with [permission.Builder.Management].
func (identity *IAM) registerPermissions(r *router.Router) {
	if identity.permissions == nil {
		return
	}
	if identity.permissions.SelfService() {
		r.Get("/me/permissions", identity.SelfPermissionsHandler())
	}
	if _, managed := identity.permissions.Management(); !managed {
		return
	}
	r.Get("/users/{subject}/permissions", identity.UserPermissionsHandler())
	r.Put("/users/{subject}/permissions/{name}", identity.GrantPermissionHandler())
	r.Delete("/users/{subject}/permissions/{name}", identity.RevokePermissionHandler())
	r.Put("/users/{subject}/roles/{name}", identity.GrantRoleHandler())
	r.Delete("/users/{subject}/roles/{name}", identity.RevokeRoleHandler())
}

// registerConnections registers the connection routes when [Builder.Provider]
// added any provider.
func (identity *IAM) registerConnections(r *router.Router) {
	if identity.links == nil {
		return
	}
	r.Get("/connections", identity.ListConnectionsHandler())
	r.Post("/connections/{provider}", identity.ConnectHandler())
	r.Delete("/connections/{id}", identity.DeleteConnectionHandler())
}
