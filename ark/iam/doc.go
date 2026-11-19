// Package iam composes the authentication features of Arkoren into one
// facade: password login and optional registration, email verification, password reset,
// two-factor authentication, API and device tokens, sign-in with external
// providers, connections, permissions and roles, and account deletion, as JSON
// routes on core's [auth.Guard].
//
// The module is dependency-free (core, foundation and the standard library) and
// split into packages that never import each other; only this root package
// imports them:
//
//	account     the users table (email), signed email verification links, the user provider
//	password    login, confirm, change, forgot and reset; register when enabled; signed reset links
//	twofactor   TOTP and recovery codes
//	apitoken    personal and device tokens, the bearer authenticator, scopes and their grant ceiling
//	federation  links external identities to users, create on first login when enabled
//	permission  roles and permissions assigned to users (core gate definitions), last-holder protection
//
// It never imports ark/oauth: a module boundary is a dependency boundary. The
// [Provider] and [NativeProvider] interfaces are satisfied structurally by
// *oauthauth.Client.
//
// # Wiring
//
//	scheme := totp.New("Acme") // the name authenticator apps show for this account
//	identity, err := iam.New(db, store, func(identity *iam.Builder) {
//		identity.Password(true, hasher, func(passwords *password.Builder) {
//			passwords.Registration(true) // off by default
//			passwords.Reset(true, signer, dispatchReset, deliverReset)
//		})
//		identity.Directory(func(accounts *account.Builder) {
//			accounts.Verification(true, signer, dispatchVerification, deliverVerification)
//		})
//		identity.TwoFactor(true, scheme, cipher)
//		identity.Tokens(true)
//		identity.Permissions(true, func(policy *permission.Builder) {
//			policy.Roles(Viewer, Editor, Admin).KeepOne(Admin)
//		})
//		identity.Provider(true, google)
//		identity.Provider(true, github, func(login *iam.ProviderBuilder) {
//			login.SignUp(true) // off by default, per provider
//		})
//		identity.Me(true, func(me *iam.MeBuilder) {
//			me.Permissions(true).TwoFactor(true).Login(true)
//		})
//	})
//	if err != nil {
//		return err
//	}
//	if err := migrator.Add(identity.Migrations()...); err != nil { // enabled features only
//		return err
//	}
//	web := routes.With(identity.Middleware()) // session, guard and token authenticators
//	web.Group("/", identity.Register)
//
//	api := routes.With(identity.Middleware(), auth.Require())
//	api.Get("/posts", listPosts)
//
// Every feature is off until enabled with its first argument, `enabled`, and
// stays off when the method is never called; with false its settings are unused
// and the components it requires may be nil. A setting that depends on a
// feature that is off is ignored with one warning logged through slog.Default,
// and missing components or invalid values still fail [New] with
// [ErrInvalidConfig]. The two ways users appear on
// their own are off by default too: self-service registration ([password.Builder.Registration]) and
// creating a user on a first provider login ([ProviderBuilder.SignUp]). With
// both off the application adds users itself, with [IAM.Directory] ([account.Directory.Create],
// [account.Directory.CreateVerified]) or its own users. The application brings the database, the
// cache, a hasher for passwords and a cipher for two-factor secrets; the facade
// does no I/O at construction, the application runs the migrations, and
// [IAM.Ping] is the readiness check.
//
// The default client address is the connection host, ignoring forwarding
// headers. If a reverse proxy sits in front of the application, configure
// [Builder.ClientIP] with a resolver that trusts only that infrastructure.
// The resolvers in arkoren.dev/core/http/clientip return addresses for login
// records; the throttles independently group IPv6 addresses into /64 buckets.
//
// # Email links
//
// The module never sends mail and starts no goroutine for it. Password reset
// ([password.Builder.Reset]) and email verification ([account.Builder.Verification])
// each take a signer from core's http/link package and two functions of the
// application: dispatch, called inside the request, stores the work durably (a
// queue job, a workflow run, an outbox row) or returns an error; deliver, called
// later by the worker, renders and sends the email from a [password.ResetMessage]
// or an [account.VerifyMessage]. The worker hands each stored request to
// [IAM.SendReset] or [IAM.SendVerification], which have the shape of a queue
// handler:
//
//	signer, err := link.New(secret, func(links *link.Builder) {
//		links.Origin("https://app.example.com")
//	})
//	resetJob := queue.NewJob[password.ResetRequest]("send-reset")
//	dispatchReset := func(ctx context.Context, request password.ResetRequest) error {
//		return dispatcher.Dispatch(ctx, resetJob, request)
//	}
//	deliverReset := func(ctx context.Context, message password.ResetMessage) error {
//		href := message.Link("https://app.example.com/reset-password")
//		_, err := mailer.Send(ctx, resetEmail(message.Address, href, message.Notice))
//		return err
//	}
//	// after iam.New:
//	err = consumer.Handle(resetJob, identity.SendReset)
//
// A link is a signed value bound to the state its action changes, so there are
// no token tables: a reset link is bound to the user's password hash and stops
// working once the password changes, a verification link to the user's email and
// whether it is verified. Both expire. The signed value reaches the front end in
// the URL fragment; the page reads location.hash and posts it as the token of
// POST /password/reset or POST /email/verify.
//
// Forgot-password dispatches the identifier exactly as typed, whether or not the
// user exists, and answers 202; a dispatch error is a 500. deliver may run more
// than once for one request, because a failing deliver is retried by the queue.
//
// # Routes
//
// [IAM.Register] registers these, and each has an IAM.XHandler method for
// custom paths. Bodies and responses are JSON.
//
//	Login       POST /register (with Registration) · POST /login · POST /login/challenge · GET /login/pending · POST /logout
//	Step-up     POST /confirm
//	Email       POST /email/verify · POST /email/verify/resend
//	Password    POST /password/forgot · POST /password/reset · PUT /password
//	Two-factor  POST /two-factor/setup · POST /two-factor · DELETE /two-factor · POST /two-factor/recovery-codes
//	Tokens      GET /tokens · POST /tokens (personal) · DELETE /tokens/{id} · POST /device-tokens · DELETE /device-tokens/current (device)
//	Sessions    GET /sessions · DELETE /sessions/{id} · DELETE /sessions
//	Providers   GET /auth/<name> · GET|POST /auth/<name>/callback · GET /auth/<name>/nonce · POST /auth/<name>/native
//	Connections GET /connections · POST /connections/{provider} · DELETE /connections/{id}
//	Permissions GET /me/permissions (SelfService) · GET /users/{subject}/permissions · PUT|DELETE /users/{subject}/permissions/{name} · PUT|DELETE /users/{subject}/roles/{name} (Management)
//	Account     GET /me (Me) · DELETE /account
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
//   - Password login counts only failures, per identifier and client address
//     together (10), per identifier across addresses (50) and per client address
//     (100) in 15 minutes, so someone failing from elsewhere cannot lock a user
//     out; an unknown identifier costs a dummy hash. Forgot-password, register and
//     resend are throttled per identifier and per address too.
//   - Register, when enabled, answers an empty 202 and logs nobody in, whether or not the address
//     was taken; for a taken one it creates nothing. With the built-in users
//     (which need [password.Builder.Reset] for registration) and Verification it
//     dispatches the same work on both paths: a verification for a new address and
//     a reset notice to the owner of a taken one; with only Reset it dispatches
//     nothing. A failed dispatch is logged. It runs within the registration
//     throttle.
//   - Every POST, PUT and PATCH route requires the media type application/json
//     (415 otherwise, even without a body), which stops cross-site HTML forms.
//     It is core's contenttype middleware, applied inside each handler so it also
//     holds on custom paths; DELETE routes are not checked. It is
//     not CSRF protection: mount core's csrf.Middleware next to
//     [IAM.Middleware].
//   - Forgot-password and resend answer an empty 202 once their work is
//     dispatched, so neither the answer nor its timing says whether the user
//     exists. Mail goes to the address stored with the user, never to the text of
//     the request.
//   - A password change keeps the caller, ends the user's other logins and
//     revokes all their tokens, personal ones too (only device tokens with
//     [Builder.KeepPersonalTokens]); enabling two-factor does the same; a reset
//     rotates the user, which ends every session, pending login and API token,
//     and never logs the user in. Of two concurrent submits of one reset link
//     exactly one succeeds.
//   - A completed reset proves the mailbox. When the email was unverified it is
//     marked verified and every connection, the authenticator, the recovery codes
//     and all tokens are deleted, because whoever registered the address first
//     may have attached them. Until verified, an account cannot connect a
//     provider, enroll two-factor or create tokens (403 email-unverified).
//   - Enabling two-factor ends the user's other logins. Disabling it and
//     regenerating recovery codes need a recent authenticator or recovery code;
//     a fresh password alone is 403 with methods ["otp"], and a recovery code used
//     within the step-up window also counts as the fresh login.
//   - A token's scopes are capped by what [apitoken.Builder.Grants] lets the user
//     hold: at issue (403 scope-not-granted) and again on every request, or
//     within [apitoken.Builder.GrantTTL] of a change. With [Builder.Permissions]
//     the cap is the user's permissions from the store, and its catalog the
//     store's permission names, unless a Grants call in [Builder.Tokens]
//     replaces both.
//   - With [Builder.Permissions] the guard loads the user's expanded roles and
//     permissions on every authenticated session request
//     ([permission.Store.Load]), so core's gate.HasPermission and gate.HasRole
//     work on sessions; a token carries its scopes and never roles. Sessions
//     then pass scope and permission rules, so mount csrf.Middleware on every
//     route a session can reach. The management routes need a fresh browser
//     session holding [permission.Builder.Management], and a caller can grant or
//     revoke only what they hold (403 not-grantable); the last holder of a
//     [permission.Builder.KeepOne] role cannot lose it (409 last-holder).
//   - A first login with an unknown provider identity creates no user unless
//     [ProviderBuilder.SignUp] is on for that provider (403 no-account), and then only when the
//     provider proved an email (403 email-unverified).
//   - Federation never links by email, with one exception: an identity whose
//     email the provider verified takes over the user who holds that email and
//     never verified it, with or without sign-up. In one transaction the
//     password, connections, authenticator, recovery codes and tokens are
//     deleted, the email is marked verified, the user is rotated (ending the
//     registrant's sessions and, after the commit, their login records), the
//     identity is linked, [federation.AccountClaimed] is published and the owner is
//     logged in.
//     A verified or disabled user is never taken over. Connecting a provider is a
//     POST that leaves a ten-minute, single-use intent in the session. A user
//     cannot remove their last way to log in (409).
//   - Email verification never blocks login; protect routes with
//     [IAM.RequireVerified], which also works with [Users.Verified].
//   - Device tokens unused past their idle limit, expired tokens and tokens a
//     version change of the user ended are deleted by [IAM.Prune], which you
//     schedule (it is func(context.Context) error); the token list already hides
//     the ended ones.
//   - A password reset of a user whose email was already verified does not delete
//     their provider connections: only the reset of an unverified account purges
//     (see above). Someone who held the old password could have connected a
//     provider of their own, so review the connections after a reset, for
//     example from a listener of [password.WasReset] that
//     lists or removes them with [IAM.Links], and
//     from one of [federation.AccountClaimed].
//   - Events carry subjects only (see each package) on [Builder.Events].
//
// # Your own users
//
// [Builder.Users] replaces the built-in users table (identified by email) with
// the application's: a provider plus Lookup, Create and Rotate functions. No
// users table is migrated, and the routes and handlers that need the directory
// (email verification, account deletion) are absent or panic at composition,
// and [IAM.RequireVerified] needs [Users.Verified]. The other packages work unchanged, because they only
// see subjects. Lookup returns the stored address mail goes to, Create gets
// whether the provider verified the email and must fail on an existing
// identifier, and the unverified-account rules above apply only when you give
// [Users.Verified] and [Users.ProveEmail]; without them the application must
// enforce the same rules itself. Registration and sign-up with a provider stay
// off until [password.Builder.Registration] and [ProviderBuilder.SignUp] are
// enabled, and each needs [Users.Create]; [New] fails without it. The
// application adds its own users in its own store, and [Builder.Directory],
// which configures the built-in table, cannot be combined with Users.
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
package iam
