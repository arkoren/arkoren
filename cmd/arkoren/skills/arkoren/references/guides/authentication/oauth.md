---
title: Browser Login with OAuth and OpenID Connect
nav: OAuth Browser Login
description: Sign people in through an external provider, map issuer and subject to a local user, and keep them signed in with a session.
order: 40
---

# Browser Login with OAuth and OpenID Connect

## Introduction

Instead of managing passwords yourself, you can let people sign in with an
account they already have, at Google, Microsoft, GitHub, or your company's
identity provider. The provider checks who they are and sends them back to your
service with proof. **OpenID Connect** (OIDC) is the standard for this proof:
it adds signed identity tokens on top of **OAuth 2**, the protocol that sends
the browser to the provider and back. Some providers, GitHub among them, speak
only plain OAuth 2, where the service asks the provider's API who the user is.

The `oauthauth` package, in the `arkoren.dev/driver/oauth` module, handles
both. This page covers browser login. For programs presenting provider-issued
access tokens, follow [OIDC Bearer Verification](oidc-bearer.md).
It is not a driver of a contract: it plugs into the [`auth`
package](overview.md) as login handlers and as a bearer token verifier, so the
rest of your service sees an ordinary signed-in user.

## Compatibility matrix

Compare Arkoren's provider presets and generic adapters, not every authentication
feature offered by each identity service. The PKCE column describes the default
authorization-code challenge; it does not attest to provider-side enforcement.
All browser rows use the shared session identity shown below.

| Provider | Browser login | OIDC discovery | PKCE by default | Requires `key.Signing` | Verified-email filter |
| --- | --- | --- | --- | --- | --- |
| [Google](#google) | Yes | Yes | Yes | No | Yes |
| [Microsoft](#microsoft) | Yes — Enforces the selected tenant policy. | Yes | Yes | No | Yes — Email is empty unless xms_edov is boolean true. |
| [GitHub](#github) | Yes | No | Yes | No | Yes — Uses only the primary verified address. |
| [Apple](#apple) | Yes | Yes | No | Yes | No |
| [Generic OIDC](#generic-oidc) | Yes | Yes | Yes | No | No |
| [Generic OAuth2](#generic-oauth2) | Yes | No | Yes | No | No — The application-supplied UserInfoFunc owns email verification. |

All clients need an encrypter for browser login. Apple forces PKCE off and uses
a cross-site POST callback; follow its [callback setup](#apple) rather than
the ordinary GET route. Only OIDC-based adapters support the separate
[bearer-verification task](oidc-bearer.md), which requires an explicit API audience.
Even a filtered email is not a safe account-linking key: use issuer and subject.
Every OIDC row requires a nonempty final subject after provider-specific mapping;
GitHub requires a positive numeric user ID. The generic OAuth2 function owns its
issuer and subject checks. These checks do not establish an API token's purpose.

## Installation

Run this command from your application's module directory:

```sh
go get arkoren.dev/driver/oauth
```

Import the package under the name `oauth`:

```go
import oauth "arkoren.dev/driver/oauth/oauthauth"
```

The examples use standard-library `context` and `os`, `auth` from
`arkoren.dev/core/http/auth`, and `application` from `arkoren.dev/core/application` for the
development-environment check. `r` and `web` are
your existing router and CSRF-protected browser group; keep the session
middleware outside that group when also serving an Apple callback.

Focused construction snippets keep error results visible and assume success;
handle constructor errors before using clients. Identity-mapping and login
decisions retain their checks.

## Creating a client

Register your service with the provider first. It gives you a **client ID**
and usually a **client secret**, and it asks for the exact URL it should send
people back to, the **redirect URL**. You also need an
[encrypter](../encryption/overview.md), named `cipher` below, to seal the
short-lived login cookie. Use the same encryption key on every service instance:
a login can start on one instance and finish on another. Then create a client
with your registered values:

```go
client, err := oauth.New(
	oauth.Google(),
	cipher,
	os.Getenv("GOOGLE_CLIENT_ID"),
	func(client *oauth.ClientBuilder) {
		client.ClientSecret(os.Getenv("GOOGLE_CLIENT_SECRET"))
		client.RedirectURL("https://app.example.com/oauth/callback")
	},
)
```

The first argument selects the provider; the second supplies the encrypter;
the third is the registered client ID. For OIDC login, that client ID is the
required ID-token **audience**, the token's intended recipient: the token's
`aud` must contain that value. `ClientBuilder.Audiences` applies only to bearer
verification and does not change this callback check. Browser-only clients can
leave it unset; see [Protecting an API](#protecting-an-api) for combined use.

Configuration callbacks run once, in order, on a fresh draft before validation.
Do not pass a nil callback: it panics. Expanding a nil or empty callback slice
applies no changes. Builder methods return the same draft.

The client copies scope and audience slices when you supply them and when it
publishes its settings. Keeping a draft does not let you change the completed
client, but supplied services and captured values remain shared. PKCE is enabled
by default except for the Apple preset; explicitly setting `PKCE(false)` also
disables that protection. Disable it only for a provider that cannot support PKCE.
`Config` is a redacted diagnostic representation, not a constructor argument
or a settings getter.

`ClientBuilder.Name` defaults to `arkoren.oauth` and prefixes the login cookies.
`New` rejects a final prefix that cannot form a valid HTTP cookie name, including
the secure `__Host-` prefix when enabled. Invalid names fail construction rather
than allowing a redirect whose login cookie the browser cannot receive.

Use an existing `app *application.Application`
([lifecycle setup](../application/lifecycle-and-ownership.md)). OIDC clients
discover provider settings before serving; verification may fetch signing keys.
Register `Start` so an unreachable provider fails startup, not the first login:

```go
compose := func(composer *application.Composer) {
	composer.Startup(client.Start)
}
```

If native discovery or the native token verifier returns an error after its
context is canceled or its deadline expires, the returned chain retains that
native error and lets `errors.Is` identify `context.Canceled` or
`context.DeadlineExceeded`. Configuration exits and local claim/policy denials
are not automatically joined with context errors. This preserves error identity,
not a guarantee that every cached check performs I/O or fails on an ended context.
Native diagnostics can contain provider-controlled claims or response text;
inspect causes deliberately rather than logging them indiscriminately.

The client has no `Stop` hook and does not stop its shared encrypter or event bus.
Your application owns those services and any supplied native HTTP client. Stop
the encryption drivers after the handlers that use them have finished.

## Logging in with a browser

Use a `web` group with [session and CSRF middleware](session-login.md#setting-up).
Share one identity between auth middleware and the browser handlers;
`users.FromIdentity` is your mapping function, described below:

```go
identity := auth.NewSessionIdentity(users.Load)
web.Use(auth.Middleware(identity.Authenticator()))
web.Get("/login", client.Login())
web.Get("/oauth/callback", client.Callback(identity, users.FromIdentity))
web.Post("/logout", client.Logout(identity))
```

A link or button pointing to `/login` starts a login. `Login` redirects the
browser to the provider, where the person signs in and agrees to share their
details. The provider then sends the browser to your redirect URL, where
`Callback` checks the answer, asks you which local user it belongs to, and
signs that user in with [`identity.Login`](session-login.md#logging-in), exactly
as a password login would. From then on, `identity.Authenticator()` reads the user from
the session on every request.

`Logout` signs the user out of your service with `identity.Logout`, then
redirects to `/`. It does not sign them out at the provider, where they usually
want to stay signed in for other services.

Callback and logout resolve `identity.Session(ctx)` once, without default-slot
fallback. A missing session returns 401 before cookie consumption, provider
exchange, or mapping. Zero definitions, nil loaders, and nil mappers fail with
configuration errors before those effects; check `identity.Err()` during setup.
Optional [shared handles](session-login.md#custom-session-store) select
context slots, not cookies or storage. Session middleware still owns persistence.

### Sending people back where they were

A person who opens a private page while signed out should land on that page
after signing in. Pass the page as `return_to` when starting the login:

```text
/login?return_to=/invoices/7
```

The target is kept with the login and checked when it ends: only paths on your
own site are followed, so the parameter cannot be used to send people to
another site.

### What happens during a login

When a login starts, the client creates a random **state**, which ties the
provider's answer to this browser, so an
attacker cannot slip their own login into someone else's browser. The
**nonce**, created for OIDC providers, ties the identity token to this login,
so an ID token from another login is rejected. When PKCE is enabled, the
**PKCE verifier** makes an intercepted authorization code unusable without that
verifier. The Apple preset disables PKCE.

These values, and the `return_to` target, are kept in an encrypted, HttpOnly
cookie that lives for ten minutes and is deleted when the login completes.
Keeping them in a cookie, rather than in the session, means a login needs no
session until it ends, and that several logins, for example in two tabs, can
run side by side. A missing, modified, expired, or state-mismatched cookie fails
the callback with `400`. The browser deletes its cookie after the callback;
the client has no server-side used-state registry. Single-use authorization
codes are enforced by the provider, not by cookie deletion alone.

For an OIDC callback, the ID token must pass signature, expiry, issuer, and
client-ID audience checks, and its nonce must match the saved login nonce.
The final `Identity.Subject` must be nonempty after provider-specific mapping;
for Microsoft that is the checked `oid`, not necessarily the original `sub`.
These checks run before your mapping function. A missing ID token or failed
token check returns `ErrInvalidCallback` (400); no local user is signed in.

A token endpoint's native `invalid_grant` rejection also becomes
`ErrInvalidCallback` (400), retaining the `oauth2.RetrieveError` cause. This can
mean an invalid, expired, or already-consumed code: start a fresh login rather
than replaying it. Other exchange failures wrap `ErrProviderUnavailable` (502).
Cookie deletion does not revoke a copied cookie before its expiry.

## Mapping identities to users

The callback gives your function an **identity**: the provider's name for the
user, plus the details it shared, such as an email and a display name. Your
function finds the matching local user, or creates one, and returns the
identifier to store in the session. That identifier must have the same type
as the argument accepted by your identity's loader (here `int`). The shared
definition binds both types, so a mismatched mapper does not compile:

```go
func (users *Users) FromIdentity(ctx context.Context, id oauth.Identity) (int, error) {
	if id.Issuer == "" || id.Subject == "" {
		return 0, auth.ErrUnauthenticated
	}
	user, err := users.upsertByIdentity(ctx, id.Issuer, id.Subject, id.Email, id.Name)
	if err != nil {
		return 0, err
	}

	return user.ID, nil
}
```

Identify the local user by the pair of `Issuer`, the provider, and `Subject`,
the provider's ID for the person. Providers guarantee this pair is stable and
unique; an email address is neither, since people change it and accounts get
recycled.

> [!WARNING]
> Never link accounts by email alone. Providers differ in whether they check
> that the person owns the address. An attacker could register a victim's
> email at a provider that does not check, sign in with it, and take over the
> victim's account. A populated `Identity.Email` does not by itself prove that
> the address is verified.

Email handling depends on the selected provider:

| Provider | What populates `Identity.Email` |
| --- | --- |
| `Google` | The email claim only when `email_verified` is boolean `true`. |
| `Microsoft` | The email claim only when the optional `xms_edov` claim is boolean `true`; configure it in the app registration. |
| `GitHub` | The primary verified address returned by GitHub's email API. |
| `OIDC` and `Apple` | The email claim is passed through without an email-verification check. |
| `OAuth2` | Whatever your `UserInfoFunc` returns; your function owns verification. |

If an application action requires a verified address, enforce a provider-specific
verification policy or verify the address yourself. Do not treat provider login
as an automatic account-linking or email-verification policy.

For browser login, `Identity.Scopes` comes from the token response. If that
response supplies no scopes, the callback uses an independent copy of the
requested scopes. A mapper can edit its received slice without changing later
logins. This is scope handling, not authorization of your application's resources.

Return `auth.ErrUnauthenticated` to refuse login with `401`.
Mapper errors matching `auth.ErrNoCredentials`, including wrapped errors, become
`ErrInvalidCallback` (400) with the cause preserved. Other errors pass through;
nobody is signed in.

## Providers

Google, Microsoft, GitHub, and Apple have their own constructors, because each
needs small adjustments to the standard flow. These selections use the shared
`oauth` import and client wiring above; Apple's signing inputs are described in
its [provider setup](#apple):

```go
oauth.Google(func(provider *oauth.GoogleBuilder) {
	provider.HostedDomain("example.com") // One verified Workspace domain.
})
oauth.Microsoft("common")                           // or your tenant ID
oauth.GitHub()
oauth.Apple(teamID, keyID, appleSigning)
```

### Google

Choose Google for Google account or Workspace login. Register a web client with
your callback URL and obtain its client ID and secret before creating the client.

#### Installation

Use the shared [module installation and `oauth` import](#installation).

#### Connecting

Use the [client example](#creating-a-client); handle its constructor error and register
the `Start` hook. Construction performs no network I/O; startup discovers Google.

#### Configuration

The preset requests `openid`, `email`, and `profile` by default. To restrict
Workspace login, use the `HostedDomain("example.com")` callback shown above instead
of `oauth.Google()`.

Google callbacks follow the client's once-only publication rules.
`HostedDomain` checks the verified `hd` claim; it is not merely an account chooser
hint. Without that restriction, the preset does not restrict Workspace domains.

#### Signing in

Follow the shared [browser-login task](#logging-in-with-a-browser) to create a
local session from the Google identity.

#### Behavior and limits

The preset normalizes the issuer to `https://accounts.google.com`. Email is empty
unless `email_verified` is boolean `true`; keep the shared
[issuer-and-subject mapping policy](#mapping-identities-to-users).

#### API references

See the [`Google` and `GoogleBuilder` API](https://pkg.go.dev/arkoren.dev/driver/oauth/oauthauth).

### Microsoft

Choose Microsoft for Entra ID or Microsoft account login. Register an application
with your callback URL, obtain its client ID and secret, and choose the tenant
policy before accepting logins.

#### Installation

Use the shared [module installation and `oauth` import](#installation).

#### Connecting

In the [client example](#creating-a-client), replace `oauth.Google()` with
`oauth.Microsoft("common")` from the preset block above and use your Microsoft
client ID and secret. Handle the constructor error and retain the `Start` hook;
discovery happens at startup, not construction.

#### Configuration

Choose the tenant argument for the accounts your application accepts:

- `common` accepts both organizational and personal-account tenants.
- `organizations` accepts organizational tenant contexts and rejects Microsoft's
  reserved personal-account tenant.
- `consumers` accepts only the personal-account tenant,
  `9188040d-6c67-4c5b-b112-36a304b66dad`.
- An explicit tenant ID accepts only that ID, matched case-insensitively. Tenant
  domain names such as `contoso.onmicrosoft.com` are not supported.

These are verified-token restrictions, not only sign-in-page filters. Both
`Callback` and `Verify` enforce them. There is no optional provider builder.
Default scopes are `openid`, `profile`, and `email`.

#### Signing in

Use the shared [browser-login task](#logging-in-with-a-browser). If your application
allows only a subset of the tenants accepted by the preset, enforce that allowlist
in your [mapping function](#mapping-identities-to-users).

#### Behavior and limits

Tokens must have `tid` and `oid`, and their issuer must equal
`https://login.microsoftonline.com/<tid>/v2.0`; `Identity.Subject` uses `oid`.
Tenant policy checks the verified `tid`, not the home identity provider (`idp`).
A personal account acting as a guest in an organization therefore has an
organizational tenant context: it can pass `organizations` or that organization's
explicit tenant check, but not `consumers`.

Email is empty unless the optional `xms_edov` claim is boolean `true`; configure
that claim in the app registration if you need email values. Email remains
unsuitable for account linking.

#### API references

See the [`Microsoft` API](https://pkg.go.dev/arkoren.dev/driver/oauth/oauthauth).

### GitHub

Choose GitHub for GitHub account login through plain OAuth2. Register an OAuth
application with your callback URL and obtain its client ID and secret.

#### Installation

Use the shared [module installation and `oauth` import](#installation).

#### Connecting

In the [client example](#creating-a-client), replace `oauth.Google()` with
`oauth.GitHub()` from the preset block above and supply your GitHub credentials.
Handle the constructor error. `Start` is a no-op for this OAuth2 preset;
construction prepares the endpoints without contacting GitHub.

#### Configuration

The preset requests `read:user` and `user:email` by default. There is no optional
provider builder. Shared client settings still apply, including PKCE enabled by
default.

#### Signing in

Follow the shared [browser-login task](#logging-in-with-a-browser). After the
code exchange, the preset calls GitHub's user and email APIs before your mapping
function creates or selects the local user.

#### Behavior and limits

The issuer is `https://github.com` and the subject is the numeric user ID, not
the changeable login name. A missing, zero, or negative ID fails the callback
with `ErrProviderUnavailable` (502) before your mapper runs; the adapter never
turns a missing ID into subject `"0"`. Email is the primary verified address, or
empty if none exists. User or email API failures also fail with `502`. This
preset has no OIDC discovery and cannot be used with `Verify`.

#### API references

See the [`GitHub` API](https://pkg.go.dev/arkoren.dev/driver/oauth/oauthauth).

### Apple

Choose Apple for Sign in with Apple. Obtain a Services ID, registered HTTPS
callback, and the signing credentials below before constructing a client.

#### Installation

Use the shared [module installation and `oauth` import](#installation).

#### Connecting

In the [client example](#creating-a-client), replace `oauth.Google()` with
`oauth.Apple(teamID, keyID, appleSigning)` from the preset block. Use the Services
ID as the client ID and omit `ClientSecret`. Handle `oauth.New` errors, including
invalid signing keys, and retain the `Start` hook for discovery.

#### Configuration

Apple requires a Services ID, team ID, key ID, and a validated `key.Signing`
identity loaded from Apple's PKCS #8 `.p8` file. Import
`arkoren.dev/foundation/key`, then load it before constructing the provider:

```go
appleSigning, err := key.LoadSigning("AuthKey_ABC123DEFG.p8")
```

Do not generate a replacement locally: Apple's provisioning associates the key
with your account. The provider signs a short-lived client secret for each exchange;
do not configure `ClientSecret` for this preset. Its default scopes are
`openid`, `name`, and `email`. There is no optional provider builder, and the preset
forces PKCE off even if you enable it.
Keep HTTPS even during Apple development: its `SameSite=None` login cookie
requires `Secure` in modern browsers.

#### Signing in

Use the shared [login and logout handlers](#logging-in-with-a-browser), but replace
the GET callback with the POST route below. Set the client's `RedirectURL` to
`https://app.example.com/oauth/apple/callback` and register that URL with Apple.

> [!WARNING]
> Apple sends a cross-site `POST` callback, which CSRF middleware rejects.
> Mount only this callback outside CSRF protection, while retaining the session
> middleware; keep CSRF protection on logout and other state-changing routes.
> Its encrypted state cookie protects the callback instead.

With an `apple` client created and started using that preset, mount its callback
on a session-enabled router outside the CSRF-protected `web` group:

```go
r.Post("/oauth/apple/callback", apple.Callback(identity, users.FromIdentity))
```

Use that exact registered redirect URL. Apple sends the name only on first
authorization, in an unsigned form field; store it as display text, not as an
authorization claim. Later logins may have an empty name. Email can be a private
relay address.

#### Behavior and limits

Apple passes the email claim through without an email-verification filter; apply the
shared [identity-mapping policy](#mapping-identities-to-users) rather than linking
accounts by email.

#### API references

See the [`Apple` API](https://pkg.go.dev/arkoren.dev/driver/oauth/oauthauth).

### Generic OIDC

Choose the generic adapter for another OpenID Connect provider, such as Okta,
Auth0, Keycloak, GitLab, or Zitadel. Register a browser client and callback URL
with that service and obtain its issuer URL and client credentials.

#### Installation

Use the shared [module installation and `oauth` import](#installation).

#### Connecting

In the [client example](#creating-a-client), replace `oauth.Google()` with
`oauth.OIDC("https://id.example.com/realms/app")`, using your provider's issuer
URL and registered credentials. Handle the constructor error and retain the `Start`
hook: startup discovers endpoints and prepares token verification.

#### Configuration

There is no optional provider builder. With `Scopes` unset, this adapter requests
only `openid`. Use the shared `client.Scopes("openid", "profile", "email")`
configuration if your provider supports those scopes and you need those claims.
PKCE is enabled by default; disable it only for a provider that cannot support it.

#### Signing in

Follow the shared [browser-login task](#logging-in-with-a-browser). The callback
verifies the ID token and nonce before calling your mapping function.

#### Behavior and limits

Unlike Google and Microsoft, this adapter does not filter the email claim for
verification. Enforce your own email policy. To also accept provider-issued JWT
access tokens, add [API audience configuration](#protecting-an-api) and follow
the separate [OIDC bearer task](oidc-bearer.md).

#### API references

See the [`OIDC` API](https://pkg.go.dev/arkoren.dev/driver/oauth/oauthauth).

### Generic OAuth2

Choose this adapter when the provider supports plain OAuth2 without OIDC.
Register a client and callback URL, obtain its credentials and authorization/token
endpoints, and implement a user-info function for its authenticated user API.

#### Installation

Use the shared [module installation and `oauth` import](#installation).

#### Connecting

For a plain OAuth2 provider without OIDC discovery, configure both endpoints
and your application's `loadIdentity` (`oauth.UserInfoFunc`):

```go
provider := oauth.OAuth2(oauth.Endpoint{
	AuthURL:  "https://id.example.com/authorize",
	TokenURL: "https://id.example.com/token",
}, loadIdentity)
```

Pass `provider` to `oauth.New` using the shared [client setup](#creating-a-client)
and your registered credentials. Handle its constructor error. Construction prepares
the endpoints without network I/O; `Start` is a no-op.

#### Configuration

There is no optional provider builder and no default scope list. Set
`client.Scopes(...)` in the shared client callback to the scopes required by your
provider's user API. PKCE is enabled by default; disable it only if unsupported.

#### Signing in

Use the shared [browser-login task](#logging-in-with-a-browser). After the code
exchange, `loadIdentity` supplies the identity passed to your mapping function.

#### Behavior and limits

`loadIdentity` gets an authenticated `*http.Client` and must call the provider's user API with the
supplied context, check the HTTP status, close the response body, and return a
stable issuer and subject. It owns email-verification policy. It does not receive
an ID token, and this provider cannot be used with `Verify`.

An exchange rejected with native `invalid_grant` returns `400`. Other exchange
failures and user-info failures return `502` instead of signing the user in. The
generic adapter does not enforce a provider-specific subject schema: keep the
[issuer-and-subject mapping policy](#mapping-identities-to-users).

#### API references

See the [`OAuth2`, `Endpoint`, and `UserInfoFunc` API](https://pkg.go.dev/arkoren.dev/driver/oauth/oauthauth).

## Protecting an API

Browser login creates a local session; it does not turn your application into
an OAuth authorization server. To accept an existing provider-issued JWT access
token, follow [OIDC Bearer Verification](oidc-bearer.md). That task needs an API
audience and no browser routes or session middleware.

If the same OIDC client handles browser login and API verification, add the API
audience to the `*oauth.ClientBuilder` draft in the
[client setup](#creating-a-client):

```go
client.Audiences("https://api.example.com")
```

Keep the registered browser client ID as the third argument to `oauth.New`.
The callback still requires that client ID in the ID token's `aud`. An ID token
addressed only to `https://api.example.com` fails with `ErrInvalidCallback` (400),
even with a valid signature and matching nonce. `client.Verify` instead requires
at least one token audience to match a configured API audience. Without
`Audiences`, `Verify` returns `ErrNoAudiences`; browser login still works.

Keep the [browser handlers](#logging-in-with-a-browser) and pass the same started
client's `Verify` method to the [bearer setup](oidc-bearer.md#requiring-an-identity).
Audience separation does not by itself prove that a bearer token is an access
token or authorize an API action. `Verify` rejects a `nonce` claim or a payload
`typ` claim equal to `ID`, case-insensitively, as heuristics. An unmarked JWT
matching an accepted audience can still pass. These checks do not define a
universal access-token profile or require every provider to issue `at+jwt`.
Configure the audiences intended for your API, apply any additional
[provider-specific purpose verification](oidc-bearer.md#preparing-the-verifier),
and enforce your application's [authorization rules](../authorization/overview.md).

## Events

With `oauth.ClientBuilder.Events`, the client publishes an [event](../events/overview.md)
for successful and failed callbacks, logout, and token rejection during
verification. Nil publishes nothing. Successful-login payloads contain issuer
and subject, which can still identify a person; apply your privacy policy before
retaining them. Logout has no payload and reports local session mutation, not
provider logout or response-time persistence by session middleware.

`LoginFailedEvent.Err` is only `ErrInvalidCallback`, `ErrProviderUnavailable`, or
an opaque local-login failure classification. It has no native, mapper, or
session-error cause. `TokenRejectedEvent.Err` is a fixed opaque rejection without
token claims or native causes. Neither event error is the original error returned
by `Callback` or `Verify`. Those returned errors retain their causes for explicit
`errors.Is`/`errors.As` inspection; their native diagnostics can be sensitive.
Do not classify opaque event errors by comparing their text.

`LoginFailedEvent.ProviderError` is an untrusted callback hint, not an authenticated
provider diagnosis. It preserves only these RFC 6749 authorization-error codes:
`invalid_request`, `unauthorized_client`, `access_denied`,
`unsupported_response_type`, `invalid_scope`, `server_error`, and
`temporarily_unavailable`. Absent or unknown values become empty; arbitrary
request strings and descriptions are not published.

Startup failures and browser-handler not-started errors return before those
outcome events, as do missing selected sessions and invalid identity/mapper
configuration. For `Verify`, unsupported-provider
and missing-audience errors return before publication, but a not-started error
publishes a token-rejection event.

These bounds describe the emitted payloads. Listeners still receive the caller's
context; context values and custom listener logging are not sanitized by them.

## Diagnostic formatting and ownership

Keep the `*Client` returned by `New`; do not copy its atomic state. Its
`fmt.Formatter` method writes `[REDACTED OAuth client]` for ordinary `%v`, `%+v`,
and `%#v` diagnostics. A `ClientBuilder` value or pointer similarly writes
`[REDACTED OAuth draft]`. These hooks do not traverse secrets, shared services,
native clients, or callback captures. Separately, `Config.String`, `GoString`,
and `LogValue` replace a nonempty `ClientSecret` with `[REDACTED]`; other fields
are not recursively sanitized.

This is a formatting boundary, not a general serialization or logging guarantee.
It does not promise sanitization through arbitrary serialization, reflection,
invalid `fmt` verbs, explicitly unwrapped errors, or formatting native/caller-owned
objects themselves. Keep credential exports and application data out of diagnostics.

## Local development

The login cookie uses the `__Host-` prefix and the `Secure` flag, so browsers
only accept it over HTTPS. Chrome and Firefox make an exception for
`localhost`, so local development usually just works. Safari does not, and no
browser makes one for `.test` domains or LAN addresses. For those, allow the
cookie over plain HTTP in the development environment only:

```go
configure := func(client *oauth.ClientBuilder) {
	client.AllowInsecure(app.Environment() == application.Development)
}
```

Pass `configure` to `oauth.New` with your provider, cipher, and client ID.

> [!WARNING]
> Never enable `ClientBuilder.AllowInsecure` in production. Without the `Secure` flag,
> the login cookie can travel over unencrypted connections.

Most providers accept `http://localhost` as a redirect URL for development;
register it as a second redirect URL next to your production one.

## Limits

The client covers signing in and verifying tokens. It does not refresh tokens
on behalf of your service, sign people out at the provider, or check opaque
tokens by asking the provider. Each client talks to one provider; to offer
several, create one client per provider, each with its own name, login route,
and callback route.

For every function and option, see the
[`oauthauth` API reference](https://pkg.go.dev/arkoren.dev/driver/oauth/oauthauth).
