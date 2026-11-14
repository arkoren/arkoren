// Package oauthauth logs users in with OpenID Connect and OAuth2 providers, and
// verifies OIDC bearer tokens. Import it as oauth:
//
//	import oauth "arkoren.dev/ark/oauth/oauthauth"
//
// It builds on [arkoren.dev/core/auth]: [Client.LoginHandler] and
// [Client.CallbackHandler] run the browser Authorization Code flow with PKCE and
// log the user in through auth.Guard.Begin with a federated factor
// ([MethodFederated]), so a registered second factor is honored;
// [Client.NonceHandler] and [Client.NativeHandler] log in a mobile app that
// signed in with Google or Apple's own SDK; auth.Guard.LogoutHandler ends the
// login; [Client.Verify] verifies JWT access tokens and returns the external
// [auth.External], which the application maps to its own subject for
// auth.Bearer. Authorization is done with gate rules such as gate.HasScopes.
//
// A Client satisfies the Provider and NativeProvider interfaces of ark/access
// structurally: Name, LoginHandler and CallbackHandler, and NonceHandler and
// NativeHandler, each taking only core types.
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
//	// Find or create the local user of an external identity and return its
//	// subject, the string the application's user provider knows it by.
//	resolve := func(ctx context.Context, external auth.External) (string, error) {
//		user, err := users.UpsertByIdentity(ctx, external.Issuer, external.Subject, external.Email)
//		if err != nil {
//			return "", err
//		}
//
//		return driver.Subject(user) // the application's typed user driver
//	}
//
//	// One guard serves every login method; it shares the session middleware's cache.
//	guard := compose.Must(auth.NewGuard(provider, store))
//
//	// Browser routes: the session holds the subject.
//	web := r.With(
//		session.Middleware(store, func(cookies *session.Builder) {
//			cookies.Name(session.HostPrefix + "app.session")
//		}),
//
//		auth.Middleware(guard.Authenticator()),
//	)
//	web.Get("/login", client.LoginHandler()) // supports ?return_to=/local/path
//	web.Get("/oauth/callback", client.CallbackHandler(guard, resolve))
//	web.Post("/logout", guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
//		logout.Redirect("/")
//	}))
//	web.With(auth.Require()).Get("/me", me)
//
//	// API routes: wrap Verify so the token maps to the application's subject.
//	// The construction callback configures the API's accepted audience above.
//	bearer := auth.Bearer(func(ctx context.Context, token string) (auth.Identity, error) {
//		external, err := client.Verify(ctx, token)
//		if err != nil {
//			return auth.Identity{}, err
//		}
//
//		subject, err := users.SubjectFor(ctx, external.Issuer, external.Subject)
//		if err != nil {
//			return auth.Identity{}, err
//		}
//
//		scope, _ := external.Claims["scope"].(string)
//
//		return auth.NewIdentity(subject, func(principal *auth.IdentityBuilder) {
//			principal.Scopes(strings.Fields(scope)...)
//		}), nil
//	})
//	api := r.With(auth.Middleware(bearer))
//	api.With(gate.Require(gate.HasScopes("posts:write"))).Post("/posts", create)
//
// # Second factors
//
// A login that needs a second factor is parked as pending by auth.Guard.Begin,
// which returns an auth.Outcome that is not complete. CallbackHandler then
// redirects to [ClientBuilder.ChallengeURL] and publishes no [LoginSucceeded];
// return_to is not carried over. The page there finishes the login through the
// core challenge route (auth.Guard.ChallengeHandler). Without a challenge URL
// CallbackHandler ends the pending login and fails with [ErrNoChallengeURL]; it
// never logs in without the second factor. [Client.NativeHandler] needs no
// challenge URL: it answers with outcome.Response, 202 and the methods to
// choose from.
//
// # Freshness
//
// The federated factor is stamped with this server's clock at the callback.
// The provider's auth_time claim is ignored, so a silent single sign-on
// approval looks exactly like a fresh login. Core decides what that means for
// auth.Guard.RequireFresh: a recent factor from a step-up method counts, or any
// recent factor when the user has no step-up method. [MethodFederated] is not a
// step-up method unless the application registers one, so a user with a
// password or TOTP is not made fresh by a silent re-login, and a user with
// neither is fresh right after logging in. The fresh=1 query parameter of
// [Client.LoginHandler] only asks the provider to authenticate again
// (prompt=login, max_age=0); it is a hint with no security meaning. Exchanging a
// session for a device token should use auth.Guard.RecentLogin, which a native
// or browser login satisfies and a silent re-login of an open session does not.
//
// # Native login
//
// A mobile app that signs in with the provider's SDK (Credential Manager or
// Google Sign-In, Sign in with Apple) holds an ID token and no authorization
// code. Google and Apple clients log it in in two steps on the app's session
// cookie: GET [Client.NonceHandler] returns {"nonce"}; the app gives the SDK
// that nonce (SHA-256 hex of it for Apple), then POSTs {"id_token"} to
// [Client.NativeHandler], which verifies signature, expiry, issuer, audience and
// nonce, calls the resolver and auth.Guard.Begin, and answers
// outcome.Response. The nonce is single use. Accepted audiences are exactly
// [ClientBuilder.NativeAudiences]: for Google the server client ID the app
// requests tokens for (usually the web client ID), otherwise its iOS or Android
// client ID; for Apple the app's bundle ID.
//
//	client := compose.Must(oauth.New(oauth.Google(), cipher, webClientID, func(client *oauth.ClientBuilder) {
//		client.NativeAudiences(webClientID, iosClientID)
//	}))
//	web.Get("/auth/google/nonce", client.NonceHandler())
//	web.Post("/auth/google/native", client.NativeHandler(guard, resolve))
//
// Both handlers use the default session of session.Middleware. Other presets
// return [ErrUnsupported].
//
// # Identity mapping
//
// CallbackHandler checks the session before side effects: a request without one
// returns auth.ErrUnauthenticated without consuming state. A nil guard or
// resolver is a configuration error. Resolver errors matching
// auth.ErrNoCredentials become ErrInvalidCallback, while other resolver errors
// pass through. A subject the user provider does not know or has disabled is
// auth.ErrUnauthenticated; an invalid subject is auth.ErrInvalidSubject.
//
// The resolver receives an [auth.External]: Issuer and Subject name the external
// account; Email and EmailVerified are set only on the provider's positive
// evidence; Claims holds the verified token claims, with the display name in
// Claims["name"] and the granted scopes as the string claim "scope" (browser
// logins; Verify keeps the token's own claims). Match local users on the pair
// Issuer + Subject, which the provider guarantees stable and unique. Never link
// accounts by email unless EmailVerified is true and your policy allows it:
// providers differ in whether they verify it, so an attacker could register the
// victim's address at a lax provider and take over the account.
// OIDC rejects an empty final subject after provider-specific mapping; the GitHub
// preset requires a positive numeric user ID before invoking the local mapper.
// If the token endpoint rejects an invalid or consumed code with invalid_grant,
// CallbackHandler wraps ErrInvalidCallback, not ErrProviderUnavailable. Native causes
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
// Verify, wrapped for auth.Bearer, works for OIDC providers only. OAuth2 providers have no
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
// With [ClientBuilder.Events], [Client.CallbackHandler] and
// [Client.NativeHandler] publish [LoginSucceeded] ("oauth.login.succeeded") with
// the external subject and issuer and the local subject, or [LoginFailed]
// ("oauth.login.failed"); and [Client.Verify] publishes [TokenRejected]
// ("oauth.token.rejected"). Payloads never carry tokens, claims, or email.
// LoginFailed.Err is ErrInvalidCallback, ErrProviderUnavailable, or a fixed opaque
// local-login failure without causes. ProviderError accepts only the seven RFC
// 6749 authorization-error hints; absent or unknown strings become empty. It is
// untrusted request metadata, not an authenticated provider diagnosis.
// TokenRejected.Err is a fixed opaque rejection with no native cause. These event
// errors are not the errors returned by the handlers or Verify; returned causes are
// preserved for explicit inspection. Configuration exits before verification or
// cookie consumption do not publish outcome events. The optional bus stays
// shared, and its logger handles listener failures without changing outcomes.
//
// # Providers
//
//	| Provider | Setup |
//	|---|---|
//	| Google | oauth.Google(); use GoogleBuilder.HostedDomain for one Workspace domain; native login |
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
// New validates the final client name as a cookie-name prefix before serving requests. PKCE starts
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
// cookie are the CSRF defense of that route. The Apple preset also supports
// native login.
//
// Each client has a name ([Client.Name]): "google", "github", "microsoft" or
// "apple" for the presets, "oauth" for [OIDC] and [OAuth2], or the value of
// [ClientBuilder.Name]. It starts the login cookie names, so give clients of
// one application distinct names when they share a provider kind.
//
// Local logout belongs to core (auth.Guard.LogoutHandler); this package has no
// logout of its own. It does not support multiple providers per client, token refresh, opaque-token introspection, provider
// logout, back-channel logout, JARM, PAR, DPoP, or the browser handoff of a
// system-browser login (RFC 8252) for mobile apps.
package oauthauth
