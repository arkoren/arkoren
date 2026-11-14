package access

import (
	"net/http"

	"arkoren.dev/core/http/router"
)

// Mount registers the default routes of the enabled features on r. Only the
// routes of enabled features are mounted; "Always" routes need nothing.
//
//	Login       POST /register, POST /login, POST /login/challenge,
//	            GET /login/pending, POST /logout
//	Step-up     POST /confirm
//	Email       POST /email/verify, POST /email/verify/resend
//	Password    POST /password/forgot, POST /password/reset, PUT /password
//	Two-factor  POST /two-factor/setup, POST /two-factor, DELETE /two-factor,
//	            POST /two-factor/recovery-codes
//	Tokens      GET /tokens, POST /tokens, DELETE /tokens/{id},
//	            POST /device-tokens, DELETE /device-tokens/current
//	Sessions    GET /sessions, DELETE /sessions/{id}, DELETE /sessions (always)
//	Providers   GET /auth/<name>, GET and POST /auth/<name>/callback, and for
//	            native providers GET /auth/<name>/nonce, POST /auth/<name>/native
//	Connections GET /connections, POST /connections/{provider},
//	            DELETE /connections/{id}
//	Account     DELETE /account (built-in users only)
//
// The password routes need [Builder.Password], and Mount panics when it is
// enabled without [Builder.Deliver], because the forgot route has no way to send
// its link. The challenge and pending routes need a registered second factor,
// /confirm a registered step-up method, and the email routes the built-in users
// and [Builder.Deliver]. /logout and the session routes are always mounted.
//
// r must already run [Access.Middleware]; mount on a sub-router to add a prefix
// (r.Group("/account", iam.Mount)). Custom paths use the XHandler methods.
// Mounting twice, or on a router that already has these patterns, panics like
// any duplicate route.
//
// Example:
//
//	web := routes.With(iam.Middleware())
//	iam.Mount(web)
func (iam *Access) Mount(r *router.Router) {
	both := []string{http.MethodGet, http.MethodPost}
	if iam.verifier != nil {
		r.Post("/register", iam.RegisterHandler())
		r.Post("/login", iam.LoginHandler())
		r.Put("/password", iam.ChangePasswordHandler())
		r.Post("/password/reset", iam.ResetPasswordHandler())
		r.Post("/password/forgot", iam.ForgotPasswordHandler())
	}
	if iam.hasSecondFactor() {
		r.Post("/login/challenge", iam.ChallengeHandler())
		r.Get("/login/pending", iam.PendingHandler())
	}
	r.Post("/logout", iam.LogoutHandler())
	if iam.hasStepUp() {
		r.Post("/confirm", iam.ConfirmHandler())
	}
	if iam.directory != nil && iam.deliver != nil {
		r.Post("/email/verify", iam.VerifyEmailHandler())
		r.Post("/email/verify/resend", iam.ResendVerificationHandler())
	}
	if iam.factors != nil {
		r.Post("/two-factor/setup", iam.SetupTwoFactorHandler())
		r.Post("/two-factor", iam.EnableTwoFactorHandler())
		r.Delete("/two-factor", iam.DisableTwoFactorHandler())
		r.Post("/two-factor/recovery-codes", iam.RecoveryCodesHandler())
	}
	if iam.issuer != nil {
		r.Get("/tokens", iam.ListTokensHandler())
		r.Post("/tokens", iam.CreateTokenHandler())
		r.Delete("/tokens/{id}", iam.DeleteTokenHandler())
		r.Post("/device-tokens", iam.ExchangeHandler())
		r.Delete("/device-tokens/current", iam.DeleteDeviceTokenHandler())
	}
	r.Get("/sessions", iam.ListSessionsHandler())
	r.Delete("/sessions/{id}", iam.DeleteSessionHandler())
	r.Delete("/sessions", iam.DeleteOtherSessionsHandler())
	for _, provider := range iam.providers {
		base := "/auth/" + provider.Name()
		r.Get(base, provider.LoginHandler())
		r.Methods(both, base+"/callback", provider.CallbackHandler(iam.guard, iam.links.Resolve))
		if native, ok := provider.(NativeProvider); ok {
			r.Get(base+"/nonce", native.NonceHandler())
			r.Post(base+"/native", native.NativeHandler(iam.guard, iam.links.Resolve))
		}
	}
	if iam.links != nil {
		r.Get("/connections", iam.ListConnectionsHandler())
		r.Post("/connections/{provider}", iam.ConnectHandler())
		r.Delete("/connections/{id}", iam.DeleteConnectionHandler())
	}
	if iam.directory != nil {
		r.Delete("/account", iam.DeleteAccountHandler())
	}
}
