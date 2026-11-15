# arkoren.dev/ark/oauth

OpenID Connect and OAuth2 login, plus JWT bearer verification, for Arkoren, built on `arkoren.dev/core/auth`.
It is an ark: it logs users in through `*auth.Guard` and verifies bearer tokens, and has no tables of its own.

Moved from `arkoren.dev/driver/oauth` (last version v0.1.0). Change the import
path to `arkoren.dev/ark/oauth`; the package name `oauthauth` is unchanged.

```sh
go get arkoren.dev/ark/oauth
```

```go
import oauth "arkoren.dev/ark/oauth/oauthauth"
```

The examples also use `auth` and `gate` from `arkoren.dev/core/auth`, `session`
from `arkoren.dev/core/http/session`, `csrf` from `arkoren.dev/core/http/middleware/csrf`, `application` from
`arkoren.dev/core/application`, and standard-library `context` and `os`.

## Creating a client

Browser login needs an encrypted state cookie so the callback can be tied to
the login that started it. First create `cipher`, a `*crypto.Encrypter`, with
your chosen [encryption driver](https://pkg.go.dev/arkoren.dev/core/service/crypto).
Keep its key secret and stable across instances. Given your existing
`app`, configure the provider and its registered callback URL inside your
composition callback (`func(compose *application.Composer)`). Keep the browser
route registrations below in that same callback:

```go
client := compose.Must(oauth.New(
	oauth.OIDC("https://id.example.com/realms/app"),
	cipher,
	"web",
	func(client *oauth.ClientBuilder) {
		client.ClientSecret(os.Getenv("OAUTH_CLIENT_SECRET"))
		client.RedirectURL("https://app.example.com/oauth/callback")
		client.Scopes("openid", "profile", "email")
	},
))
```

Browser callbacks always validate ID tokens against the positional client ID.
`Audiences` applies only to API bearer verification and never broadens callback
acceptance. You can use the same client for both, or separate clients when their
application configuration differs.

Register OIDC discovery with the application lifecycle:

```go
compose.Startup(client.Start)
```

## Browser login

Supply your own `resolve` callback, an `auth.Resolver` (`func(context.Context,
auth.External) (string, error)`), to map a verified identity to the subject of your application's
user, for example with the `Subject` method of your typed user driver. Match by
issuer and subject, never by email alone; see [Identity mapping](#identity-mapping).
Given an existing `r` router, `store` cache and `provider` (`*auth.UserProvider`),
create the shared guard and register the browser routes:

```go
guard, err := auth.NewGuard(provider, store)
if err != nil {
	return err
}

r.Use(session.Middleware(store))
r.Use(csrf.Middleware())
r.Use(auth.Middleware(guard.Authenticator()))
r.Get("/login", client.LoginHandler()) // supports ?return_to=/local/path
r.Get("/oauth/callback", client.CallbackHandler(guard, resolve))
r.Post("/logout", guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
	logout.Redirect("/")
}))
```

## Second factors

When `guard` has a registered second-factor check for the user, `Begin` holds the
login as pending and the callback redirects to the local path set with
`ClientBuilder.ChallengeURL`, without publishing `LoginSucceeded` or carrying
`return_to`. Your challenge page finishes the login through core's challenge route
(`guard.ChallengeHandler()`). Without
a challenge URL the callback ends the pending login and fails with
`ErrNoChallengeURL`; it never signs the user in without the second factor.

```go
client, err := oauth.New(oauth.OIDC("https://id.example.com/realms/app"), cipher, "web",
	func(client *oauth.ClientBuilder) {
		client.RedirectURL("https://app.example.com/oauth/callback")
		client.ChallengeURL("/login/challenge")
	},
)
```

Use HTTPS in production. Apple sends a cross-site POST callback, which CSRF
middleware blocks. Register only that callback outside `csrf.Middleware`, or
use `csrf.Builder.Protection` with an `http.CrossOriginProtection` configured
with `AddInsecureBypassPattern("POST /oauth/callback")`. Its encrypted login
state cookie protects that callback; keep CSRF protection on logout and other
cookie-authenticated state-changing routes. The POST callback only verifies the identity: it keeps it in a 30-second sealed `SameSite=Lax` cookie and
answers `303` to the callback path, where the top-level GET navigation carries the session cookie and finishes the
login, so connecting Apple to a logged-in user links the identity instead of creating a second account. The identity
links to the session present at that GET, and the session cookie must be `SameSite=Lax` (the default; Strict
breaks connect). Route both methods to the callback (`r.Methods([]string{"GET", "POST"}, ...)`), and give the
client an absolute `RedirectURL` with a path: for a `form_post` provider, `New` rejects a non-empty `RedirectURL`
that is not absolute with a path. The hand-off expiry check tolerates 5 seconds of clock skew between servers.

### Upgrading

Breaking for `form_post` providers (Apple): the callback must now be mounted for both GET and POST. Before, a
POST alone was enough. A GET that carries a code is rejected for these providers; only the hand-off from the
POST completes the login. `ark/access` `Mount` already routes both methods. `RedirectURL` must be absolute with
a path.

## Freshness

The federated factor is stamped with this server's clock at the callback; the
provider's `auth_time` is ignored. Core's rule decides what that means for
`guard.RequireFresh`: a recent factor from a step-up method counts, or any recent
factor when the user has no step-up method. `oauth.MethodFederated` is not a
step-up method, so a user with a password or TOTP is not made fresh by a silent
re-login, and a user with neither is fresh right after logging in.
`/login?fresh=1` still adds `prompt=login&max_age=0` to an OIDC authorization
request, but only as a hint to the provider with no security meaning. To require
a genuinely new login, for example before exchanging a session for a device
token, use `guard.RecentLogin`.

## Native login

A mobile app that signs in with Google's or Apple's SDK holds an ID token and no
authorization code. Two routes on the app's session (its cookie jar) log it in:

```go
client := compose.Must(oauth.New(oauth.Google(), cipher, webClientID,
	func(client *oauth.ClientBuilder) {
		client.NativeAudiences(webClientID, iosClientID)
	},
))

r.Get("/auth/google/nonce", client.NonceHandler())
r.Post("/auth/google/native", client.NativeHandler(guard, resolve))
```

`NonceHandler` answers `{"nonce"}` and keeps it in the session for ten minutes.
The app passes the nonce to the SDK and posts `{"id_token"}` to `NativeHandler`,
which spends the nonce, verifies signature, expiry, issuer, audience and nonce,
calls `resolve` and `guard.Begin`, and answers `204` (logged in) or `202` with the
second-factor methods. Accepted audiences are exactly `NativeAudiences`: for
Google the server client ID the apps set in their SDK, which Google documents as the
token's audience (normally the web client ID), plus any iOS or Android client ID they
request tokens for, for Apple the bundle ID.
Google returns the nonce unchanged. Apple copies the request nonce into the token
as sent, so the app sends the lowercase hex SHA-256 of the nonce and the handler
accepts only that. Other presets return `ErrUnsupported`. See the
[browser login guide](../../cmd/arkoren/skills/arkoren/references/guides/authentication/oauth.md#native-login)
for the provider facts and their sources.

## API authentication

`Verify` returns the external `auth.External`, never an `auth.Identity`: the
application maps issuer and subject to its own subject. First construct a
dedicated verifier inside the composition callback with the API's registered
audience, not the browser client ID:

```go
verifier := compose.Must(oauth.New(oauth.OIDC("https://id.example.com/realms/app"), cipher, "api", func(client *oauth.ClientBuilder) {
	client.Audiences("https://api.example.com")
}))
compose.Startup(verifier.Start)
```

Both client instances share your existing stable `cipher`. After discovery
succeeds, use `verifier` in the application mapping:

```go
bearer := auth.Bearer(func(ctx context.Context, token string) (auth.Identity, error) {
	external, err := verifier.Verify(ctx, token)
	if err != nil {
		return auth.Identity{}, err
	}

	subject, err := users.SubjectFor(ctx, external.Issuer, external.Subject)
	if err != nil {
		return auth.Identity{}, err
	}

	scope, _ := external.Claims["scope"].(string)

	return auth.NewIdentity(subject, func(principal *auth.IdentityBuilder) {
		principal.Scopes(strings.Fields(scope)...)
	}), nil
})
```

Add `bearer` to the authentication middleware and protect routes with
`auth.Require()`. Because the wrapper carries the token's scopes on the
`auth.Identity`, `gate.HasScopes` authorizes them, given your `createPost`
handler:

```go
api := r.With(auth.Middleware(bearer))
api.With(gate.Require(gate.HasScopes("posts:write"))).Post("/posts", createPost)
```

## API

- `oauth.New(provider, cipher, clientID, configure...)`: pure constructor; `client.Start(ctx)` runs OIDC discovery (no-op for OAuth2).
- `oauth.OIDC(issuerURL)`, `oauth.OAuth2(endpoint, userInfo)`: providers.
- `client.Name()`: the login method name (`google`, `github`, `microsoft`, `apple`, else `oauth`).
- `client.LoginHandler()`, `client.CallbackHandler(guard, resolve)`: browser flow handlers. `resolve` is an
  `auth.Resolver`: it maps the external `auth.External` identity to the app's user subject. Mapper errors matching `auth.ErrNoCredentials`
  become `ErrInvalidCallback` while retaining their causes; other mapper errors
  pass through. A subject the user provider does not know or has disabled is
  `auth.ErrUnauthenticated` (401); an invalid subject is `auth.ErrInvalidSubject`.
- `client.NonceHandler()`, `client.NativeHandler(guard, resolve)`: native ID-token login for Google and Apple.
- The handlers use the guard's session without default-slot fallback (the native handlers use the default session). A
  missing session returns 401 before consuming the login cookie or calling the
  provider or mapper. A nil guard or mapper returns a configuration error before
  those effects. Successful mutation still depends on session middleware for
  response-time persistence.
- `client.Verify(ctx, token)`: verifies a JWT access token and returns the external `auth.External`; wrap it for `auth.Bearer` as shown above. OIDC only; needs `ClientBuilder.Audiences`
  (`ErrNoAudiences` otherwise).
- Errors: `ErrInvalidLoginRequest`, `ErrInvalidCallback`, `ErrProviderUnavailable`, `ErrNotStarted`, `ErrUnsupported`, `ErrNoAudiences`, `ErrNoNativeAudiences`, `ErrNoChallengeURL`.
- `oauth.MethodFederated` is the `auth.Method` of every login this module proves.

`ClientBuilder` provides `ClientSecret`, `RedirectURL`, `ChallengeURL`, `Scopes`, `Audiences`,
`NativeAudiences`, `Name` (default: the preset's name, else `oauth`), `AllowInsecure(bool)`, `PKCE(bool)` (default on), and `Events`.
Callbacks run directly once in order on one fresh draft before validation. Nil entries panic;
expanded nil or empty slices omit edits. Retained drafts cannot change published settings.
Scope and audience slices are copied at input and publication; services stay shared.
`Config` remains a redacted formatting representation, never a constructor input.
`New` rejects invalid final HTTP cookie names. `Client` does not own or stop the
shared cipher or event bus. Keep its constructor pointer rather than copying its
atomic state. Client pointers and retained `ClientBuilder` values/pointers format
as fixed redaction markers; arbitrary caller-owned dependencies are not traversed.

## Login cookie

Each login sets one cookie `__Host-<Name>.<12 chars of base64url(sha256(state))>`: encrypted with the crypto
cipher, HttpOnly, Secure, Path=/, Max-Age=600, SameSite=Lax (None for `form_post` providers, which also set a 30-second Lax hand-off cookie). A browser keeps at
most five; the callback accepts GET or POST (for `form_post` providers a GET that carries a code is rejected), deletes the cookie, and rejects missing, tampered, expired or
state-mismatched cookies with `ErrInvalidCallback`. The client deletes the browser's
cookie but keeps no server-side used-state registry; one-time authorization codes
must be enforced by the provider. Deletion does not revoke a copied cookie before
its expiry. A token endpoint's `invalid_grant` response (including an expired or
consumed code) becomes `ErrInvalidCallback`, not a provider outage; ask the user
to start a new login rather than replaying that code. Other exchange failures
wrap `ErrProviderUnavailable`. Original native causes remain inspectable.

## Identity mapping

The resolver receives an `auth.External`. Match local users on the pair `Issuer`
+ `Subject`, which the provider guarantees stable and unique. `Email` is set,
with `EmailVerified` true, only on the provider's positive evidence; never link
accounts by email otherwise: providers differ in whether they verify it, so an
attacker could register the victim's address at a lax provider and take over the
account. `Claims["name"]` is the display name and, for browser logins,
`Claims["scope"]` holds the granted scopes as a space-delimited string. OIDC
rejects a missing final subject after provider mapping. The GitHub preset
requires a positive numeric user ID before the mapper runs.

## Local development

HTTPS and localhost work in Chrome and Firefox, because the login cookie is
Secure and uses the "__Host-" prefix. Safari rejects such cookies over HTTP,
on `.test` domains and on LAN addresses. In those cases use
`ClientBuilder.AllowInsecure(app.Environment() == application.Development)` in the construction callback: the cookie loses Secure and the
prefix. Never enable it in production.

## Bearer tokens

`Client.Verify` works for OIDC providers only; a plain OAuth2 preset returns
`ErrUnsupported`. `auth.Bearer` itself is not OIDC-specific: it accepts your own
verifier for API keys or other token formats. Verification checks signature,
expiry, issuer, and audience, and rejects a `nonce` claim or `typ=ID`. Keep a
distinct API audience rather than relying on those markers alone. Marker rejection
is a heuristic: an unmarked JWT matching an accepted audience can pass. Enforce
provider-specific token-purpose policy in your application's verifier if needed;
there is no generic access-token-purpose guarantee or required `at+jwt` profile.
This package does not acquire, refresh, or issue API access tokens.

Discovery and verification failures retain original native errors and restore
context cancellation/deadline identity when failing native I/O observes it. Use
`errors.Is`/`errors.As` to inspect them. Native diagnostics can contain claims or
provider text; do not log returned errors or unwrapped causes indiscriminately.

## Observing login outcomes

Set `ClientBuilder.Events(bus)` to share your existing `event.Bus`. Nil publishes
nothing. Completed logins, browser or native, report the external subject and issuer and the
local subject through `LoginSucceeded` (a login held for a second factor publishes
nothing); those identifiers still require your application's privacy policy.
Local logout belongs to core: `guard.LogoutHandler` publishes `auth.LoggedOut`.

`LoginFailed.Err` contains only `ErrInvalidCallback`, `ErrProviderUnavailable`, or
an opaque local-login failure. It never retains native or mapper causes.
`ProviderError` preserves only the RFC 6749 hints `invalid_request`,
`unauthorized_client`, `access_denied`, `unsupported_response_type`, `invalid_scope`,
`server_error`, and `temporarily_unavailable`; absent or unknown values become
empty. The hint is untrusted request metadata, not an authenticated diagnosis.
`TokenRejected.Err` is a fixed opaque rejection without token, claims or causes.
Do not compare opaque errors by diagnostic text. Inspect the original returned
error explicitly if you need its cause; events intentionally do not expose it.
Configuration and missing-session exits before cookie consumption, and bearer
configuration exits before verification, publish no outcome event. Listener
failures use the shared bus's logger and cannot change the recorded outcome.

## Providers

| Provider | Setup |
|---|---|
| Google | `oauth.Google()`; use `GoogleBuilder.HostedDomain` for one Workspace domain; native login |
| Microsoft | `oauth.Microsoft("common")`, or a tenant ID; Subject is the `oid` claim |
| GitHub | `oauth.GitHub()` (OAuth2; subject is the numeric user ID) |
| Apple | `oauth.Apple(teamID, keyID, identity)` with `key.Signing` loaded from Apple's `.p8` file, not locally generated (form_post; see CSRF note); native login |
| Okta | `oauth.OIDC("https://<org>.okta.com")` |
| Auth0 | `oauth.OIDC("https://<tenant>.auth0.com/")` |
| Keycloak | `oauth.OIDC("https://<host>/realms/<realm>")` |
| GitLab | `oauth.OIDC("https://gitlab.com")` |
| LinkedIn | `oauth.OIDC("https://www.linkedin.com/oauth")` |
| Zitadel | `oauth.OIDC("https://<instance>.zitadel.cloud")` |

Restrict Google to a verified Workspace domain:

```go
provider := oauth.Google(func(provider *oauth.GoogleBuilder) {
	provider.HostedDomain("example.com")
})
```

The preset checks the verified `hd` claim as well as sending the account-chooser hint.
Google callbacks use the same direct ordered once-only rules and detach the completed draft.
OIDC, OAuth2, Apple, Microsoft, and GitHub have no optional builders.

For provider-specific email handling, Apple callback setup, and event payloads,
follow the [browser login guide](../../cmd/arkoren/skills/arkoren/references/guides/authentication/oauth.md).
For every exported identifier, see the
[`oauthauth` API reference](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth).
