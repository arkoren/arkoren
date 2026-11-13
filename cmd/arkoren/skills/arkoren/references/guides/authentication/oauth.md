---
title: OAuth Login
description: Complete external browser sign-in and map the provider identity to a local account safely.
order: 40
summary:
  - Configure an external provider and complete browser login through the shared login.
  - Link local accounts by issuer and subject, return their subject, and hand a second factor to its challenge page.
  - Apply each provider's callback and identity requirements and handle bounded outcome events.
---

# OAuth Login

Let a provider check a person's credentials, then create a local browser login
for the matching account. OpenID Connect (OIDC) adds verified ID tokens to the
OAuth 2 authorization flow; plain OAuth2 providers instead supply identity
through an authenticated user API. `oauthauth` supports both paths.

You still own local accounts and external-account linkage. The adapter completes
the provider flow, calls your mapper, and logs the returned account subject in
through the shared [Session Login](session-login.md) login, so a registered
[second factor](two-factor.md) still applies. It is not an authorization server and
does not acquire API tokens for arbitrary clients or refresh tokens for your
service. To accept an existing JWT at an API, use [OIDC Tokens](oidc-bearer.md).

## Choose Providers

Use a preset when it matches your provider; it includes identity checks that a
generic adapter does not infer. The matrix describes shipped adapter behavior,
not every feature offered by the provider. PKCE is the authorization-code
challenge sent by default, not a claim about provider-side enforcement.

### Compatibility Matrix

| Provider | Browser login | OIDC discovery | PKCE default | Verified-email filter | JWT verification |
| --- | --- | --- | --- | --- | --- |
| [Google](#google) | Yes | Yes | Yes | Yes | Yes — Requires explicit API audiences. |
| [Microsoft](#microsoft) | Yes — Enforces the selected tenant policy. | Yes | Yes | Yes — Requires boolean xms_edov. | Yes — Enforces the selected tenant policy. |
| [GitHub](#github) | Yes | No | Yes | Yes — Selects the primary verified address. | No |
| [Apple](#apple) | Yes — Requires a POST callback and key.Signing. | Yes | No | No | Yes — Requires explicit API audiences. |
| [Generic OIDC](#generic-oidc) | Yes | Yes | Yes | No | Yes — Requires explicit API audiences. |
| [Generic OAuth2](#generic-oauth2) | Yes | No | Yes | Partial — The application owns user-info validation. | No |

All clients require an encrypter. JWT verification requires OIDC, explicit API
audiences, and your provider's token-purpose policy; it is not interchangeable
with browser ID-token verification. A filtered email is still not an account key.

## Configure Clients

Register an application and exact callback URL with your provider. Obtain its
client ID and required credentials, then install the adapter in your module:

```sh
go get arkoren.dev/ark/oauth
```

Import it as `oauth`. The example uses an existing `cipher *crypto.Encrypter`
from [Encryption](../encryption/overview.md), plus `clientID` and `clientSecret`
loaded from your configuration/secret provider:

```go
import oauth "arkoren.dev/ark/oauth/oauthauth"

client, err := oauth.New(oauth.Google(), cipher, clientID,
	func(login *oauth.ClientBuilder) {
		login.ClientSecret(clientSecret)
		login.RedirectURL("https://app.example.com/oauth/callback")
	},
)
```

Handle the constructor error before using `client`. Construction validates
inputs but performs no network I/O. Use a stable encryption key shared by all
instances that can receive callbacks; key provisioning belongs in
[Encryption Keys](../encryption/keys.md), not in each request handler.

For OIDC, the positional `clientID` is the required browser ID-token audience,
its intended recipient. `ClientBuilder.Audiences` configures only
[API verification](oidc-bearer.md#prepare-verification), not this callback check.
PKCE starts enabled; disable it only for a provider that cannot support it.
Apple forces it off independently of this setting.

Use a distinct `ClientBuilder.Name` for each provider client. It prefixes state
cookies and defaults to `arkoren.oauth`; invalid cookie names fail construction.
Configuration callbacks run once in order, and nil callback entries panic.
The published client copies its settings and scope/audience slices; retained
drafts cannot change it, but services and captured application state remain shared.

## Start Discovery

OIDC clients must discover their endpoints before serving requests. In your
existing [application composer](../application/lifecycle-and-ownership.md),
register startup with `application` from `arkoren.dev/core/application`:

```go
compose := func(composer *application.Composer) {
	composer.Startup(client.Start)
}
```

Before successful startup, OIDC handlers return `ErrNotStarted` (`503`). A
subsequent `Start` refreshes discovered data. Plain OAuth2 clients prepare their
configured endpoints at construction; their `Start` is a no-op. Signing keys
can still be retrieved during OIDC token verification.

Keep the returned `*Client`; do not copy its atomic state. It has no `Stop`
method and does not stop the shared encrypter or event bus. Your application
owns those services and any supplied native HTTP client; keep encryption
available until handlers have finished.

## Connect Browser Routes

Use an existing session-enabled, CSRF-protected browser group `web` and the
`login` from [Enable Login](session-login.md#enable-login), which already runs
`auth.Middleware(login.Authenticator())`. Import `auth` from
`arkoren.dev/core/auth`. The mapper `mapIdentity` below returns the local
account's subject:

```go
web.Get("/login", client.Login())
web.Get("/oauth/callback", client.Callback(login, mapIdentity))
web.Post("/logout", auth.Logout(login, func(logout *auth.LogoutBuilder) {
	logout.Redirect("/")
}))
```

`Login` redirects to the provider. A successful callback verifies the provider
identity, maps it to a subject, calls `login.Begin` with a federated factor,
rotates the session ID, and redirects to the saved local destination. Subsequent
requests use the normal authenticator. `auth.Logout` from core owns the logout route: it calls `login.Logout`, which also
ends a pending second-factor login, and redirects to `/`; it does not sign the
person out at the provider. Keep logout CSRF-protected. `client.Logout(login)`
still works but is deprecated in favor of `auth.Logout`.

If a user who is already logged in as the mapped subject signs in through the
provider (to connect it or to refresh their proof), `Begin` adds the federated
factor to their session and does not ask for a second factor again.

A nil login or mapper is a configuration error, not a rejected credential. A
request without a session returns `401` before state consumption or provider
exchange. If you use [custom session selection](session-login.md#select-sessions),
build the login with the same handle as the session middleware. Middleware still
owns [response-time persistence](../http-capabilities/sessions.md#save-changes);
successful callback mutation does not prove storage or old-ID retirement succeeded.

## Link Local Accounts

Identify an external account by the pair `Issuer` and `Subject`, not by email
or display name. Require an existing link for ordinary sign-in; create accounts
only under your explicit registration policy. To link another provider to an
existing account, require proof of control of that local account as a separate,
authorized action.

With standard-library `context`, this application mapper looks up an existing
link using `accounts.ByIdentity(ctx, issuer, subject)`, which returns the
application's `Account` or an error, and converts the account to its subject with
the user `driver` from [Session Login](session-login.md#load-accounts):

```go
mapIdentity := func(ctx context.Context, external oauth.Identity) (string, error) {
	if external.Issuer == "" || external.Subject == "" {
		return "", auth.ErrUnauthenticated
	}
	account, err := accounts.ByIdentity(ctx, external.Issuer, external.Subject)
	if err != nil {
		return "", err
	}

	return driver.Subject(account)
}
```

The repository owns uniqueness of the issuer/subject link and rejection of
unknown accounts. The callback never uses the provider's `Subject` as the
login subject: it is the provider's identifier, not your account's. The user
provider checks the returned subject on `Begin`: an unknown or disabled account
is `auth.ErrUnauthenticated` (`401`) and an invalid subject returns
`auth.ErrInvalidSubject`, so a disabled account needs no check in the mapper.
Mapper errors matching `auth.ErrNoCredentials` become `ErrInvalidCallback`
(`400`); other application errors pass through unchanged.

> [!WARNING]
> Never match or merge accounts by email alone. Some providers pass through
> unverified email claims, and even verified addresses can change. An automatic
> email match can give an attacker another person's existing local account.

`Identity.Email` is optional profile data. Google filters using boolean
`email_verified`, Microsoft using boolean `xms_edov`, and GitHub using its primary
verified email record. Generic OIDC and Apple pass email through without that
filter; generic OAuth2 uses whatever your user-info function returns. If an
action needs a verified address, enforce that policy separately. Treat `Name`
as untrusted display text and escape it when rendering.

For browser callbacks, `Identity.Scopes` comes from the token response. If the
response omits scopes, the client supplies an independent copy of the requested
scopes. These are provider grants, not your local account's resource permissions.

## Second Factors

If the `login` has a registered [second-factor check](two-factor.md#hold-a-login)
for the mapped account, `Begin` holds the login as pending and the request stays
anonymous. Set the page that asks for the second factor with
`ClientBuilder.ChallengeURL`, a local path:

```go
client, err := oauth.New(oauth.Google(), cipher, clientID,
	func(login *oauth.ClientBuilder) {
		login.ClientSecret(clientSecret)
		login.RedirectURL("https://app.example.com/oauth/callback")
		login.ChallengeURL("/login/challenge")
	},
)
```

The callback then redirects to that path without publishing `LoginSucceeded`; the
`return_to` target is not carried over, so your challenge page chooses the
destination once `login.Complete` succeeds. New rejects a challenge URL that is not
a safe local path. Without a challenge URL the callback ends the pending login and
fails with `ErrNoChallengeURL` (`500`); it never signs the person in without
the second factor. A federated login counts as one factor and never as
multi-factor on its own.

## Protect Login State

To return a person to an invoice after login, start the flow with a local path:

```text
/login?return_to=/invoices/42
```

The client validates this target before redirecting to the provider, seals it
with the login state, and uses a safe redirect after the callback. Unsafe targets
return `ErrInvalidLoginRequest` (`400`). Do not bypass this path by redirecting
to another unvalidated request parameter.

Each login gets random state, an OIDC nonce when applicable, and a PKCE verifier
when enabled. State binds the callback to the browser login; nonce binds the
ID token to that attempt. These values live in an encrypted, HttpOnly cookie
for ten minutes, not in the application session. The client keeps room for at
most five live login cookies, allowing parallel tabs.

Callbacks reject missing, tampered, expired, or mismatched state cookies. For
OIDC they also check the ID token's signature, expiry, issuer, positional
client-ID audience, nonempty final subject, and saved nonce before mapping.
Plain OAuth2 relies on the authenticated user API instead of an ID token.
Cookie deletion is browser cleanup, not a server-side replay registry: a copied
cookie can survive until expiry. The provider must enforce one-time authorization
codes.

Cookies use `Secure` and the `__Host-` prefix by default. Use HTTPS for login
and registered callbacks. `ClientBuilder.AllowInsecure(true)` removes both
protections for plain-HTTP development only; never enable it in production.
Apple's cross-site cookie requires HTTPS even during development.

## Google

Choose Google for Google account sign-in. Register a web client and the callback
URL in Google, then use `oauth.Google()` in the shared constructor with its
client ID and secret. Keep the startup hook for discovery and the ordinary GET
callback.

If only one Workspace domain may sign in, replace the provider argument with:

```go
provider := oauth.Google(func(google *oauth.GoogleBuilder) {
	google.HostedDomain("example.com")
})
```

Pass `provider` to `oauth.New`. This checks the verified `hd` claim as well as
sending the account-chooser hint; the default accepts any domain. Default scopes
are `openid`, `email`, and `profile`. The preset normalizes the issuer to
`https://accounts.google.com` and leaves email empty without boolean
`email_verified: true`. Keep issuer/subject linkage even with this filter.

See the [`Google` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#Google).

## Microsoft

Choose Microsoft for Entra ID or Microsoft-account sign-in. Register the callback
URL and obtain the application's client ID and secret. Replace the shared
provider argument with a deliberately chosen tenant policy:

```go
provider := oauth.Microsoft("organizations")
```

Pass it to `oauth.New`, keep `Start` for discovery, and use the shared browser
routes and mapper. `organizations` rejects the reserved personal-account tenant;
`consumers` accepts only that tenant (`9188040d-6c67-4c5b-b112-36a304b66dad`).
`common` accepts both classes. For single-tenant use, pass the tenant ID, matched
case-insensitively, not a domain name such as `contoso.onmicrosoft.com`.

Both callback and bearer checks enforce the verified `tid` policy and require
`oid`. The issuer must be `https://login.microsoftonline.com/<tid>/v2.0`, and
the final subject is `oid`. The check uses tenant context, not the home `idp`:
a personal account invited into an organization can pass `organizations` in
that organization's context. Add a narrower tenant allowlist in your mapper
if the selected preset is broader than your policy.

Default scopes are `openid`, `profile`, and `email`. Email remains empty unless
the optional `xms_edov` claim is boolean true; configure that claim in the app
registration if needed. It is still not a local-account key.

See the [`Microsoft` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#Microsoft).

## GitHub

Choose GitHub for plain OAuth2 sign-in. Register a GitHub OAuth application and
the callback URL, then pass its ID/secret to the shared constructor with:

```go
provider := oauth.GitHub()
```

Use the shared GET callback and local mapper. `Start` is a no-op because this
adapter has configured endpoints, not discovery. Default scopes are `read:user`
and `user:email`; PKCE remains enabled.

After code exchange, the preset calls the user and email APIs. Its issuer is
`https://github.com`, and subject is the positive numeric user ID, not the mutable
login name. Missing, zero, or negative IDs and user/email API failures return
`ErrProviderUnavailable` (`502`) before mapping. Email is the primary verified
address or empty. This preset cannot verify OIDC bearer tokens.

See the [`GitHub` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#GitHub).

## Apple

Choose Apple for Sign in with Apple. Register an HTTPS callback and a Services
ID, obtain your team ID/key ID, and load the Apple-issued `.p8` signing key with
`key.LoadSigning` from `arkoren.dev/foundation/key`. Do not generate an unrelated
local replacement for a key registered with Apple.

```go
appleSigning, err := key.LoadSigning("AuthKey_ABC123DEFG.p8")
```

Handle that error, then construct the provider:

```go
provider := oauth.Apple(teamID, keyID, appleSigning)
```

Use the Services ID as `oauth.New`'s client ID, omit `ClientSecret`, and configure
`RedirectURL("https://app.example.com/oauth/apple/callback")`. The constructor
rejects an invalid key. The provider signs a short-lived client secret per code
exchange, requires discovery, requests `openid`, `name`, and `email`, and forces
PKCE off.

Apple sends a cross-site POST callback. Mount only that callback outside CSRF
protection, while retaining session middleware; keep CSRF on logout and all
other state-changing browser routes. With `r` your session-enabled router,
the callback is:

```go
r.Post("/oauth/apple/callback", client.Callback(identity, mapIdentity))
```

Use an Apple-configured `client`, not the Google baseline. Its encrypted state
cookie protects this callback and uses `SameSite=None`; HTTPS is required.
Apple sends the name only on first authorization in an unsigned form field,
so store it as optional display text, not an authorization claim. Email can be
a private relay address and is not filtered for verification by this preset.

See the [`Apple` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#Apple).

## Generic OIDC

Choose this adapter for another OIDC service. Register a browser client and
callback with the service, then configure its trusted issuer URL:

```go
provider := oauth.OIDC("https://id.example.com/realms/invoices")
```

Pass the provider and registered credentials to the shared constructor; keep
discovery startup and the shared GET callback. Default scopes contain only
`openid`. If supported and needed, use `ClientBuilder.Scopes("openid", "profile",
"email")`. PKCE is enabled by default. No provider-specific tenant or email
policy is inferred: implement those checks before linking the local account.

See the [`OIDC` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#OIDC).

## Generic OAuth2

Choose this adapter only when the provider has OAuth2 but no OIDC. Register
your application/callback and obtain its trusted authorization and token URLs.
Supply your own authenticated user-info function:

```go
provider := oauth.OAuth2(oauth.Endpoint{
	AuthURL:  "https://id.example.com/authorize",
	TokenURL: "https://id.example.com/token",
}, loadIdentity)
```

`loadIdentity` is `oauth.UserInfoFunc`: it receives `context.Context` and an
authenticated `*http.Client` and returns `oauth.Identity`. It must call the
provider's user API with that context, check status, close the response body,
and validate a nonempty stable issuer/subject and any email policy. The generic
adapter does not impose a provider-specific subject schema.

Pass the provider and registered credentials to the shared constructor and
use its GET callback. There are no default scopes; configure `Scopes` for the
provider's user API. Endpoints are prepared without network I/O, and `Start`
is a no-op. PKCE starts enabled; disable it only if unsupported. User-info
failures become `502`, and `Verify` is unsupported because there is no OIDC
verifier.

See the [`OAuth2` and `UserInfoFunc` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#OAuth2).

## Handle Failures

Invalid state, failed ID-token checks, provider callback errors, and a token
endpoint's native `invalid_grant` rejection return `ErrInvalidCallback` (`400`).
Start a fresh login rather than replaying an invalid, expired, or consumed code.
Other exchange failures return `ErrProviderUnavailable` (`502`). Returned errors
retain native causes for explicit inspection; provider response text and claims
can be sensitive, so do not dump those chains into logs.

Discovery errors retain native causes and context cancellation/deadline identity
when applicable. The bearer-only [verification path](oidc-bearer.md#handle-verification)
has additional context checks; do not assume every browser callback or local
policy denial behaves the same way.

## Events

If you need outcome events, configure `ClientBuilder.Events` with your existing
[event bus](../events/overview.md). Nil publishes nothing. Completed logins
publish issuer and subject, which can identify a person; a login held for a second
factor publishes nothing until your challenge page completes it. `auth.Logout` publishes `auth.LoggedOut` with the subject when configured with
`LogoutBuilder.Events`; it reports local mutation, not provider logout or durable
session persistence.

`LoginFailedEvent.Err` contains `ErrInvalidCallback`, `ErrProviderUnavailable`,
or an opaque local-login classification, without the original cause.
`ProviderError` is an untrusted, allowlisted RFC 6749 callback hint; unknown
values and arbitrary descriptions are omitted. Configuration, not-started, and
missing-session exits before cookie consumption do not publish callback outcomes.
`TokenRejectedEvent.Err` is likewise a fixed opaque classification; unsupported
providers and missing audiences return before publication. Do not classify opaque
errors by matching their text. Returned errors retain their separate diagnostic
chains, and event context values are not sanitized by these payload limits.

Ordinary `fmt` formatting redacts owned client/draft configuration; `Config`
formatting redacts a nonempty client secret. This is not a general serialization
or logging guarantee: arbitrary claims, native objects, callback captures, and
explicitly unwrapped errors remain outside that boundary.

For all functions and options, see the
[`oauthauth` API reference](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth).
