// Package access composes the authentication features of Arkoren into one
// facade: registration and password login, email verification, password reset,
// two-factor authentication, API and device tokens, sign-in with external
// providers, connections and account deletion, as JSON routes on core's
// [auth.Guard].
//
// The module is dependency-free (core, foundation and the standard library) and
// split into packages that never import each other; only this root package
// imports them:
//
//	account     the users table (email), email verification, the user provider
//	password    register, login, confirm, change, forgot and reset
//	twofactor   TOTP and recovery codes
//	apitoken    personal and device tokens, the bearer authenticator, scopes
//	federation  links external identities to users, create on first login
//
// It never imports ark/oauth: a module boundary is a dependency boundary. The
// [Provider] and [NativeProvider] interfaces are satisfied structurally by
// *oauthauth.Client.
//
// # Wiring
//
//	iam, err := access.New(db, store, func(iam *access.Builder) {
//		iam.Password(hasher)
//		iam.TwoFactor(cipher, "Example")
//		iam.Tokens()
//		iam.Provider(google, github)
//		iam.Deliver(send) // reset and verification links
//	})
//	if err != nil {
//		return err
//	}
//	if err := migrator.Add(iam.Migrations()...); err != nil { // enabled features only
//		return err
//	}
//	web := routes.With(iam.Middleware()) // session, guard and token authenticators
//	iam.Mount(web)
//
//	api := routes.With(iam.Middleware(), auth.Require())
//	api.Get("/me", me)
//
// Every feature is off until enabled. The application brings the database, the
// cache, a hasher for passwords and a cipher for two-factor secrets; the facade
// does no I/O at construction, the application runs the migrations, and
// [Access.Ping] is the readiness check.
//
// # Routes
//
// [Access.Mount] registers these, and each has an Access.XHandler method for
// custom paths. Bodies and responses are JSON.
//
//	Login       POST /register · POST /login · POST /login/challenge · GET /login/pending · POST /logout
//	Step-up     POST /confirm
//	Email       POST /email/verify · POST /email/verify/resend
//	Password    POST /password/forgot · POST /password/reset · PUT /password
//	Two-factor  POST /two-factor/setup · POST /two-factor · DELETE /two-factor · POST /two-factor/recovery-codes
//	Tokens      GET /tokens · POST /tokens · DELETE /tokens/{id} · POST /device-tokens · DELETE /device-tokens/current
//	Sessions    GET /sessions · DELETE /sessions/{id} · DELETE /sessions
//	Providers   GET /auth/<name> · GET|POST /auth/<name>/callback · GET /auth/<name>/nonce · POST /auth/<name>/native
//	Connections GET /connections · POST /connections/{provider} · DELETE /connections/{id}
//	Account     DELETE /account
//
// # Behavior
//
//   - A login answers 204 when complete, or 202 {"second_factor": {"methods":
//     [...]}} when the user must also answer a second factor; the client then
//     posts {"method": "...", ...} to /login/challenge. Users pick any enrolled
//     method, listed in registration order.
//   - Problems carry stable types "tag:arkoren.dev,2026-11:ark/access:<package>:<name>":
//     422 for a wrong secret or invalid input (core's own challenge and confirm
//     routes answer 403 for a wrong code), 401 for no login or pending state, 403
//     for step-up, 409 for a conflict, 429 with Retry-After when throttled.
//   - Every credential-changing handler requires a browser session
//     ([auth.Identity.Session]) and calls [auth.Guard.Fresh]; an API token can
//     never enroll, change or remove a credential. A user with no step-up method
//     is fresh right after logging in, so a first enrollment needs nothing special.
//   - Password login is throttled per identifier and per client address, and an
//     unknown identifier costs a dummy hash. A password change keeps the caller
//     and ends the user's other logins; a reset rotates the user, which ends every
//     session, pending login and API token, and never logs the user in.
//   - Federation never links by email. Connecting a provider is a POST that
//     leaves a ten-minute, single-use intent in the session. A user cannot remove
//     their last way to log in (409).
//   - Email verification never blocks login; protect routes with
//     [Access.RequireVerified].
//   - Events carry subjects only (see each package) on [Builder.Events].
//
// # Your own users
//
// [Builder.Users] replaces the built-in users table (identified by email) with
// the application's: a provider plus Lookup, Create and Rotate functions. No
// users table is migrated, and the routes and handlers that need the directory
// (email verification, account deletion, [Access.RequireVerified]) are absent or
// panic at composition. The other packages work unchanged, because they only
// see subjects.
//
// # Transactions and limits
//
// Every package assumes the one [database.Database] passed to [New]: writes that
// span packages join the transaction carried by the context, and account
// deletion removes every package's rows together. Store subjects as bytewise
// text in your own tables ([schema.Column.Bytewise]), because database default
// collations compare case-insensitively.
//
// The facade owns both the session middleware and the guard and keeps them on
// one lifetime ([Builder.SessionLifetime]) and on the default session handle,
// which the provider handlers of ark/oauth require.
package access
