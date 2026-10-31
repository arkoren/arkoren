# arkoren.dev/driver/oauth

OpenID Connect and OAuth2 login, plus JWT bearer verification, for Arkoren, built on `arkoren.dev/core/http/auth`.
This is an auth driver: it provides an `Authenticator` source and HTTP handlers rather than implementing a `Driver`.

```sh
go get arkoren.dev/driver/oauth
```

```go
import oauth "arkoren.dev/driver/oauth/oauthauth"
```

The examples also use `auth`, `router`, and `session` from their
`arkoren.dev/core/http/` packages, `gate` from `arkoren.dev/core/http/auth/gate`,
`csrf` from `arkoren.dev/core/http/middleware/csrf`, `application` from
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

Supply your own `login` callback (`func(context.Context, oauth.Identity) (int,
error)`) to map a verified identity to your application's user ID. Match by
issuer and subject, never by email alone; see [Identity mapping](#identity-mapping).
Given an existing `r` router, `store` cache, and your `users.Find` session loader,
register the browser routes:

```go
identity := auth.NewSessionIdentity(users.Find)
r.Use(session.Middleware(store))
r.Use(csrf.Middleware())
r.Use(auth.Middleware(identity.Authenticator()))
r.Get("/login", client.Login()) // supports ?return_to=/local/path
r.Get("/oauth/callback", client.Callback(identity, login))
r.Post("/logout", client.Logout(identity))
```

Use HTTPS in production. Apple sends a cross-site POST callback, which CSRF
middleware blocks. Register only that callback outside `csrf.Middleware`, or
use `csrf.Builder.Protection` with an `http.CrossOriginProtection` configured
with `AddInsecureBypassPattern("POST /oauth/callback")`. Its encrypted login
state cookie protects that callback; keep CSRF protection on logout and other
cookie-authenticated state-changing routes.

## API authentication

If API handlers need your local `User`, verify the JWT before resolving it
with your application's `users.UpsertByIdentity` operation. First construct a
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
bearer := func(ctx context.Context, token string) (User, error) {
	identity, err := verifier.Verify(ctx, token)
	if err != nil {
		return User{}, err
	}

	return users.UpsertByIdentity(ctx, identity.Issuer, identity.Subject, identity.Email)
}
```

Add `auth.Bearer(bearer)` to the authentication middleware and protect local-user
routes with `auth.Require[User]()`. The `oauth.HasScopes` rule instead needs an
`oauth.Identity` in the context; it cannot authorize the mapped `User` above.
For an API that works directly with verified identities, use a separate group
and your `createPost` handler:

```go
api := r.With(auth.Middleware(auth.Bearer(verifier.Verify)))
api.With(gate.Require(oauth.HasScopes("posts:write"))).Post("/posts", createPost)
```

## API

- `oauth.New(provider, cipher, clientID, configure...)`: pure constructor; `client.Start(ctx)` runs OIDC discovery (no-op for OAuth2).
- `oauth.OIDC(issuerURL)`, `oauth.OAuth2(endpoint, userInfo)`: providers.
- `client.Login()`, `client.Callback(identity, login)`, `client.Logout(identity)`: browser flow handlers. `login` maps the external
  identity to the app's user ID. Mapper errors matching `auth.ErrNoCredentials`
  become `ErrInvalidCallback` while retaining their causes; other mapper errors
  pass through. Returning `auth.ErrUnauthenticated` refuses login with 401.
- Callback and logout select the identity's session once, without default-slot
  fallback. A missing session returns 401 before consuming the login cookie or
  calling the provider or mapper. Invalid identity configuration or a nil mapper
  returns a configuration error before those effects. Successful mutation still
  depends on session middleware for response-time persistence.
- `client.Verify(ctx, token)`: verifies a JWT access token; use as `auth.Bearer(client.Verify)`. OIDC only; needs `ClientBuilder.Audiences`
  (`ErrNoAudiences` otherwise).
- `oauth.HasScopes(scopes...)`: gate rule requiring all scopes.
  It captures a copy of the requirements; caller slice edits cannot weaken it.
- Errors: `ErrInvalidLoginRequest`, `ErrInvalidCallback`, `ErrProviderUnavailable`, `ErrNotStarted`, `ErrUnsupported`, `ErrNoAudiences`.

`ClientBuilder` provides `ClientSecret`, `RedirectURL`, `Scopes`, `Audiences`, `Name`
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
nothing. Successful callbacks report subject and issuer through `LoginSucceeded`;
those identifiers still require your application's privacy policy. `LoggedOut`
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
[`oauthauth` API reference](https://pkg.go.dev/arkoren.dev/driver/oauth/oauthauth).
