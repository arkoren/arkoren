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

Supply your own `resolve` callback (`func(context.Context, oauth.Identity)
(string, error)`) to map a verified identity to the subject of your application's
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
r.Get("/login", client.Login()) // supports ?return_to=/local/path
r.Get("/oauth/callback", client.Callback(guard, resolve))
r.Post("/logout", guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
	logout.Redirect("/")
}))
```

## Second factors

When `guard` has a registered second-factor check for the user, `Begin` holds the
login as pending and the callback redirects to the local path set with
`ClientBuilder.ChallengeURL`, without publishing `LoginSucceeded` or carrying
`return_to`. Your challenge page finishes the login with `guard.Complete`. Without
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
cookie-authenticated state-changing routes.

## API authentication

`Verify` returns the external `oauth.Identity`, never an `auth.Identity`: the
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

	return auth.NewIdentity(subject, func(principal *auth.IdentityBuilder) {
		principal.Scopes(external.Scopes...)
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
- `client.Login()`, `client.Callback(guard, resolve)`: browser flow handlers. `resolve` maps the external
  identity to the app's user subject. Mapper errors matching `auth.ErrNoCredentials`
  become `ErrInvalidCallback` while retaining their causes; other mapper errors
  pass through. A subject the user provider does not know or has disabled is
  `auth.ErrUnauthenticated` (401); an invalid subject is `auth.ErrInvalidSubject`.
- `client.Logout(guard)` is deprecated: register `guard.LogoutHandler()` from core, which owns the logout route and
  publishes `auth.LoggedOut`. The method keeps its 401 without a session, 302 to `/` and `oauth.LoggedOut` behavior
  and is removed in a later minor release.
- Callback and logout use the guard's session without default-slot fallback. A
  missing session returns 401 before consuming the login cookie or calling the
  provider or mapper. A nil guard or mapper returns a configuration error before
  those effects. Successful mutation still depends on session middleware for
  response-time persistence.
- `client.Verify(ctx, token)`: verifies a JWT access token and returns the external `oauth.Identity`; wrap it for `auth.Bearer` as shown above. OIDC only; needs `ClientBuilder.Audiences`
  (`ErrNoAudiences` otherwise).
- Errors: `ErrInvalidLoginRequest`, `ErrInvalidCallback`, `ErrProviderUnavailable`, `ErrNotStarted`, `ErrUnsupported`, `ErrNoAudiences`, `ErrNoChallengeURL`.

`ClientBuilder` provides `ClientSecret`, `RedirectURL`, `ChallengeURL`, `Scopes`, `Audiences`, `Name`
(default `arkoren.oauth`), `AllowInsecure(bool)`, `PKCE(bool)` (default on), and `Events`.
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
cipher, HttpOnly, Secure, Path=/, Max-Age=600, SameSite=Lax (None for `form_post` providers). A browser keeps at
most five; the callback accepts GET or POST, deletes the cookie, and rejects missing, tampered, expired or
state-mismatched cookies with `ErrInvalidCallback`. The client deletes the browser's
cookie but keeps no server-side used-state registry; one-time authorization codes
must be enforced by the provider. Deletion does not revoke a copied cookie before
its expiry. A token endpoint's `invalid_grant` response (including an expired or
consumed code) becomes `ErrInvalidCallback`, not a provider outage; ask the user
to start a new login rather than replaying that code. Other exchange failures
wrap `ErrProviderUnavailable`. Original native causes remain inspectable.

## Identity mapping

Match local users on the pair `Issuer` + `Subject`, which the provider guarantees
stable and unique. Never link accounts by email: providers differ in whether
they verify it, so an attacker could register the victim's address at a lax
provider and take over the account.
OIDC rejects a missing final subject after provider mapping. The GitHub preset
requires a positive numeric user ID before the mapper runs. Callback fallback
scopes are independently copied, so mapper edits cannot change later logins.

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
nothing. Completed logins report subject and issuer through `LoginSucceeded` (a login held
for a second factor publishes nothing); those identifiers still require your
application's privacy policy. The deprecated `LoggedOut`
reports local session mutation, not provider logout or persisted session storage.

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
| Google | `oauth.Google()`; use `GoogleBuilder.HostedDomain` for one Workspace domain |
| Microsoft | `oauth.Microsoft("common")`, or a tenant ID; Subject is the `oid` claim |
| GitHub | `oauth.GitHub()` (OAuth2; subject is the numeric user ID) |
| Apple | `oauth.Apple(teamID, keyID, identity)` with `key.Signing` loaded from Apple's `.p8` file, not locally generated (form_post; see CSRF note) |
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
