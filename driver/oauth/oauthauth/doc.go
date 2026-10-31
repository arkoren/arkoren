// Package oauthauth logs users in with OpenID Connect and OAuth2 providers, and
// verifies OIDC bearer tokens. Import it as oauth:
//
//	import oauth "arkoren.dev/driver/oauth/oauthauth"
//
// It plugs into [arkoren.dev/core/http/auth] without being a driver: [Client.Login]
// and [Client.Callback] run the browser Authorization Code flow with PKCE
// and store the user with auth.SessionIdentity.Login; [Client.Logout] ends it;
// [Client.Verify] verifies JWT access tokens and fits auth.Bearer directly.
// Authorization is done with gate rules such as [HasScopes].
//
// The login state (state, nonce, PKCE verifier, return target) lives in a
// short-lived, encrypted, HttpOnly cookie per login, not in the session, so
// several logins can run in parallel and a login needs no session until the
// callback. Production deployments must use HTTPS, an exact provider-registered
// callback URL, and CSRF protection on authenticated state-changing routes.
// Cookies expire after ten minutes and are deleted from the browser at callback;
// the package has no server-side used-state registry. Copied cookies are not
// revoked by deletion: the provider must enforce one-time authorization codes.
//
// # End to end
//
// Place this setup inside an application composition callback
// (func(compose *application.Composer)), passed to app.Run. The cipher's cleanup
// is active before later validation, and discovery starts only after composition
// succeeds. Keep the route registrations in the same callback.
//
//	// The cipher seals the login cookies; keep the key stable across instances.
//	// Any crypto.Driver works, e.g. arkoren.dev/driver/aes/aescrypto.
//	driver := compose.Must(aes.New(key))
//	compose.Cleanup(driver.Stop)
//
//	cipher := compose.Must(crypto.New(driver))
//
//	client := compose.Must(oauth.New(
//		oauth.OIDC("https://id.example.com/realms/app"),
//		cipher,
//		"web",
//		func(client *oauth.ClientBuilder) {
//			client.ClientSecret(os.Getenv("OAUTH_CLIENT_SECRET"))
//			client.RedirectURL("https://app.example.com/oauth/callback")
//			client.Scopes("openid", "profile", "email")
//			client.Audiences("https://api.example.com")
//			client.AllowInsecure(compose.App().Environment() == application.Development)
//		},
//	))
//
//	// Start performs OIDC discovery.
//	compose.Startup(client.Start)
//
//	// Find or create the local user of an external identity.
//	login := func(ctx context.Context, identity oauth.Identity) (int, error) {
//		user, err := users.UpsertByIdentity(ctx, identity.Issuer, identity.Subject, identity.Email)
//
//		return user.ID, err
//	}
//
//	// Browser routes: the session holds the local user ID.
//	identity := auth.NewSessionIdentity(users.Find)
//	compose.Require(identity.Err())
//	web := r.With(
//		session.Middleware(store, func(cookies *session.Builder) {
//			cookies.Name(session.HostPrefix + "app.session")
//		}),
//
//		auth.Middleware(identity.Authenticator()),
//	)
//	web.Get("/login", client.Login()) // supports ?return_to=/local/path
//	web.Get("/oauth/callback", client.Callback(identity, login))
//	web.Post("/logout", client.Logout(identity))
//	web.With(auth.Require[User]()).Get("/me", me)
//
//	// API routes: the user is the verified token identity, so scope rules apply.
//	// The construction callback configures the API's accepted audience above.
//	api := r.With(auth.Middleware(auth.Bearer(client.Verify)))
//	api.With(gate.Require(oauth.HasScopes("posts:write"))).Post("/posts", create)
//
// # Identity mapping
//
// Callback and Logout resolve exactly the identity's selected session before
// side effects. Missing selected sessions return auth.ErrUnauthenticated without
// default fallback. Invalid identities and nil mappers are configuration errors;
// mapper errors matching auth.ErrNoCredentials become ErrInvalidCallback, while
// other mapper errors pass through. Explicit identity.Login calls do not check
// the session against its handle. A physical session has one identity slot, and
// same-K definitions or compatible persisted JSON do not enforce realm isolation.
// Handles do not isolate cookies, storage or user context (still keyed by U).
//
// Match local users on the pair Issuer + Subject, which the provider guarantees
// stable and unique. Never link accounts by email: providers differ in whether
// they verify it, so an attacker could register the victim's address at a lax
// provider and take over the account.
// OIDC rejects an empty final subject after provider-specific mapping; the GitHub
// preset requires a positive numeric user ID before invoking the local mapper.
// If the token endpoint rejects an invalid or consumed code with invalid_grant,
// Callback wraps ErrInvalidCallback, not ErrProviderUnavailable. Native causes
// remain available through errors.Is/As; they are not safe to log indiscriminately.
//
// # Local development
//
// HTTPS and localhost work in Chrome and Firefox, because the login cookie is
// Secure and uses the "__Host-" prefix. Safari rejects such cookies over HTTP,
// on .test domains and on LAN addresses. In those cases use
// ClientBuilder.AllowInsecure(app.Environment() == application.Development)
// in the construction callback: the cookie loses Secure and the prefix.
// Never enable it in production.
//
// # Bearer tokens
//
// Verify and auth.Bearer work for OIDC providers only. OAuth2 providers have no
// signed tokens to verify and return ErrUnsupported. Verify requires
// ClientBuilder.Audiences naming your API and returns ErrNoAudiences otherwise:
// the client ID is not a default because it is the audience of ID tokens.
// Browser callbacks always use the positional client ID, independently of API
// audiences. Verify rejects nonce and typ=ID claims as purpose heuristics only.
// A valid unmarked JWT matching the configured audience can pass; the package
// cannot prove its access-token purpose without provider/application policy.
// Prefer distinct browser and API audiences and enforce any required native
// token-purpose rules in the application's verifier before authorizing requests.
// Native discovery and verification failures retain context cancellation/deadline
// identity when their failed I/O observes it, even if the dependency formats it.
//
// # Events
//
// With [ClientBuilder.Events], [Client.Callback] publishes [LoginSucceeded]
// ("oauth.login.succeeded") with the subject and issuer, or [LoginFailed]
// ("oauth.login.failed"); [Client.Logout] publishes [LoggedOut]
// ("oauth.logged-out"); and [Client.Verify] publishes [TokenRejected]
// ("oauth.token.rejected"). Payloads never carry tokens, claims, or email.
// LoginFailed.Err is ErrInvalidCallback, ErrProviderUnavailable, or a fixed opaque
// local-login failure without causes. ProviderError accepts only the seven RFC
// 6749 authorization-error hints; absent or unknown strings become empty. It is
// untrusted request metadata, not an authenticated provider diagnosis.
// TokenRejected.Err is a fixed opaque rejection with no native cause. These event
// errors are not the errors returned by Callback or Verify; returned causes are
// preserved for explicit inspection. Configuration exits before verification or
// cookie consumption do not publish outcome events. The optional bus stays
// shared, and its logger handles listener failures without changing outcomes.
//
// # Providers
//
//	| Provider | Setup |
//	|---|---|
//	| Google | oauth.Google(); use GoogleBuilder.HostedDomain for one Workspace domain |
//	| Microsoft | `oauth.Microsoft("common")`, or a tenant ID; Subject is the `oid` claim |
//	| GitHub | `oauth.GitHub()` (OAuth2; subject is the numeric user ID) |
//	| Apple | `oauth.Apple(teamID, keyID, identity)` with an Apple-issued key.Signing (form_post; see CSRF note) |
//	| Okta | `oauth.OIDC("https://<org>.okta.com")` |
//	| Auth0 | `oauth.OIDC("https://<tenant>.auth0.com/")` |
//	| Keycloak | `oauth.OIDC("https://<host>/realms/<realm>")` |
//	| GitLab | `oauth.OIDC("https://gitlab.com")` |
//	| LinkedIn | `oauth.OIDC("https://www.linkedin.com/oauth")` |
//	| Zitadel | `oauth.OIDC("https://<instance>.zitadel.cloud")` |
//
// Google Workspace restrictions require a verified hosted-domain claim, not just
// the authorization hint:
//
//	provider := oauth.Google(func(provider *oauth.GoogleBuilder) {
//		provider.HostedDomain("example.com")
//	})
//	_ = provider
//
// Client and Google callbacks run directly once in order on one fresh draft;
// nil entries panic and expanded nil or empty slices omit edits. Retained drafts
// cannot change published clients or providers. Scopes and audiences are copied
// on input and publication; service pointers and function captures stay shared.
// Other provider presets have no optional settings or construction callbacks.
// New validates the final cookie-name prefix before serving requests. PKCE starts
// enabled; explicit false disables it unless a provider disables it regardless.
// Client has no Stop method and does not own its shared cipher or event bus.
// Keep constructor pointers rather than copying atomic Client state. Formatting
// a Client pointer or a ClientBuilder value/pointer emits fixed redaction markers;
// arbitrary caller-supplied objects and returned error causes are not sanitized.
//
// Apple sends its callback as a cross-site POST, so CSRF middleware rejects it.
// Register the callback outside csrf.Middleware, or use csrf.Builder.Protection
// with an http.CrossOriginProtection that calls
// AddInsecureBypassPattern("POST /auth/apple/callback"). The login state and
// cookie are the CSRF defense of that route.
//
// The package supports local logout only. It does not support multiple
// providers per client, token refresh, opaque-token introspection, provider
// logout, back-channel logout, JARM, PAR, or DPoP.
package oauthauth
