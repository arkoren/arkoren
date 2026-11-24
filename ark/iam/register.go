package iam

import (
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/iam/account"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/federation"
	"arkoren.dev/ark/iam/password"
	"arkoren.dev/ark/iam/permission"
	"arkoren.dev/ark/iam/twofactor"
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
//	Account     GET /account (with Account), DELETE /account (built-in users only)
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
// [permission.Builder.Management]. GET /account needs [Builder.Account]. /logout and the
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
	r.Get("/sessions", identity.ListSessionsHandler(), ListSessionsOperation)
	r.Delete("/sessions/{id}", identity.DeleteSessionHandler(), DeleteSessionOperation)
	r.Delete("/sessions", identity.DeleteOtherSessionsHandler(), DeleteOtherSessionsOperation)
	identity.registerProviders(r)
	identity.registerConnections(r)
	if identity.account != nil {
		r.Get("/account", identity.AccountHandler(), AccountOperation)
	}
	if identity.directory != nil {
		r.Delete("/account", identity.DeleteAccountHandler(), DeleteAccountOperation)
	}
}

// registerPassword registers the password routes when [Builder.Password] is set:
// login and change always, registration when users can be created, and the
// forgot and reset routes when [password.Builder.Reset] is set.
func (identity *IAM) registerPassword(r *router.Router) {
	if identity.verifier == nil {
		return
	}
	r.Post("/login", identity.LoginHandler(), password.LoginOperation)
	r.Put("/password", identity.ChangePasswordHandler(), password.ChangeOperation)
	if identity.registration {
		r.Post("/register", identity.RegisterHandler(), password.RegisterOperation)
	}
	if identity.reset() {
		r.Post("/password/reset", identity.ResetPasswordHandler(), password.ResetOperation)
		r.Post("/password/forgot", identity.ForgotPasswordHandler(), password.ForgotOperation)
	}
}

// registerLogin registers the guard's routes: the challenge and pending routes
// with a second factor, /logout always and /confirm with a step-up method.
func (identity *IAM) registerLogin(r *router.Router) {
	if identity.secondFactor {
		r.Post("/login/challenge", identity.ChallengeHandler(), ChallengeOperation)
		r.Get("/login/pending", identity.PendingHandler(), PendingOperation)
	}
	r.Post("/logout", identity.LogoutHandler(), LogoutOperation)
	if identity.stepUp {
		r.Post("/confirm", identity.ConfirmHandler(), ConfirmOperation)
	}
}

// registerEmail registers the email verification routes with the built-in users
// and [account.Builder.Verification].
func (identity *IAM) registerEmail(r *router.Router) {
	if !identity.verification() {
		return
	}
	r.Post("/email/verify", identity.VerifyEmailHandler(), account.VerifyOperation)
	r.Post("/email/verify/resend", identity.ResendVerificationHandler(), account.ResendOperation)
}

// registerTwoFactor registers the two-factor routes when [Builder.TwoFactor] is
// set.
func (identity *IAM) registerTwoFactor(r *router.Router) {
	if identity.factors == nil {
		return
	}
	r.Post("/two-factor/setup", identity.SetupTwoFactorHandler(), twofactor.SetupOperation)
	r.Post("/two-factor", identity.EnableTwoFactorHandler(), twofactor.EnableOperation)
	r.Delete("/two-factor", identity.DisableTwoFactorHandler(), twofactor.DisableOperation)
	r.Post("/two-factor/recovery-codes", identity.RecoveryCodesHandler(), twofactor.RecoveryCodesOperation)
}

// registerTokens registers the token routes when [Builder.Tokens] is set.
func (identity *IAM) registerTokens(r *router.Router) {
	if identity.issuer == nil {
		return
	}
	r.Get("/tokens", identity.ListTokensHandler(), apitoken.ListOperation)
	r.Delete("/tokens/{id}", identity.DeleteTokenHandler(), apitoken.DeleteOperation)
	if identity.issuer.PersonalTokens() {
		r.Post("/tokens", identity.CreateTokenHandler(), apitoken.CreateOperation)
	}
	if identity.issuer.DeviceTokens() {
		r.Post("/device-tokens", identity.ExchangeHandler(), apitoken.ExchangeOperation)
		r.Delete("/device-tokens/current", identity.DeleteDeviceTokenHandler(), apitoken.DeleteCurrentOperation)
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
		r.Get("/me/permissions", identity.SelfPermissionsHandler(), permission.SelfOperation)
	}
	if _, managed := identity.permissions.Management(); !managed {
		return
	}
	r.Get("/users/{subject}/permissions", identity.UserPermissionsHandler(), permission.UserOperation)
	r.Put("/users/{subject}/permissions/{name}", identity.GrantPermissionHandler(), permission.GrantOperation)
	r.Delete("/users/{subject}/permissions/{name}", identity.RevokePermissionHandler(), permission.RevokeOperation)
	r.Put("/users/{subject}/roles/{name}", identity.GrantRoleHandler(), permission.GrantRoleOperation)
	r.Delete("/users/{subject}/roles/{name}", identity.RevokeRoleHandler(), permission.RevokeRoleOperation)
}

// registerConnections registers the connection routes when [Builder.Provider]
// added any provider. POST and DELETE share one path template, so the wildcard
// has one name, "id" (the provider's name for POST, the connection's ID for
// DELETE): OpenAPI cannot tell "/connections/{id}" from "/connections/{provider}"
// and the export rejects both names on one path.
func (identity *IAM) registerConnections(r *router.Router) {
	if identity.links == nil {
		return
	}
	r.Get("/connections", identity.ListConnectionsHandler(), federation.ListOperation)
	r.Post("/connections/{id}", identity.ConnectHandler(), federation.ConnectOperation)
	r.Delete("/connections/{id}", identity.DeleteConnectionHandler(), federation.DeleteOperation)
}
