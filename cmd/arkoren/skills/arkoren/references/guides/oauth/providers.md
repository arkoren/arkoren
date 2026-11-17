---
title: Providers
description: Compare the shipped provider adapters and set up Google, Microsoft, GitHub, Apple, generic OIDC and generic OAuth2.
order: 40
summary:
  - Compare what each preset supports and which identity checks it adds.
  - Register and configure Google, Microsoft, GitHub and Apple, including Apple's form_post callback.
  - Configure a generic OIDC or OAuth2 provider and own the identity checks that implies.
---

# Providers

A provider adapter tells the shared client where to send people, how to verify
what comes back and which identity checks to apply. Every adapter plugs into the
same `oauth.New` constructor and the same routes from
[Browser Login](browser-login.md). Use a preset when it matches your provider;
it includes identity checks that a generic adapter does not infer.

## Compatibility Matrix

The matrix describes the behavior Arkoren ships, not every feature a provider
offers. PKCE is the authorization-code challenge the client sends by default,
not a claim about provider-side enforcement. A Yes under verified-email filter
means the provider leaves the email empty unless it vouches for it. The presets
use the provider's own evidence, Apple accepts `email_verified` as the boolean or
the string `"true"`, and generic OIDC leaves the email empty unless you turn on
`TrustEmail`.

| Provider | Name | Browser login | Native ID token | OIDC discovery | PKCE default | Verified-email filter | JWT verification |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [Google](#google) | `google` | Yes | Yes — See Native Login. | Yes | Yes | Yes | Yes — Requires explicit API audiences. |
| [Microsoft](#microsoft) | `microsoft` | Yes — Enforces the selected tenant policy. | No | Yes | Yes | Yes — Requires boolean xms_edov. | Yes — Enforces the selected tenant policy. |
| [GitHub](#github) | `github` | Yes | No | No | Yes | Yes — Selects the primary verified address. | No |
| [Apple](#apple) | `apple` | Yes — Requires HTTPS, a callback mounted for POST and GET, and a key.Signing. | Yes — See Native Login. | Yes | No | No | Yes — Requires explicit API audiences. |
| [Generic OIDC](#generic-oidc) | `oauth` | Yes | No | Yes | Yes | Yes — Reports no email unless TrustEmail is on. | Yes — Requires explicit API audiences. |
| [Generic OAuth2](#generic-oauth2) | `oauth` | Yes | No | No | Yes | Partial — The application owns user-info validation. | No |

[Native Login](native-login.md) describes the native ID-token flow. All clients
require an encrypter. JWT verification requires OIDC, explicit API audiences and
your provider's token-purpose policy, and it is not interchangeable with browser
ID-token verification; see [OIDC Tokens](bearer-tokens.md). A filtered email is
still not an account key.

## Google

Choose Google for Google account sign-in. Register a web client and the callback
URL in Google, then use `oauth.Google()` with its client ID and secret in the
shared constructor. Keep the startup hook for discovery and the ordinary GET
callback.

If only one Workspace domain may sign in, replace the provider argument with:

```go
provider := oauth.Google(func(google *oauth.GoogleBuilder) {
	google.HostedDomain("example.com")
})
```

Pass `provider` to `oauth.New`. This checks the verified `hd` claim as well as
sending the account-chooser hint; the default accepts any domain. Default scopes
are `openid`, `email` and `profile`. The preset normalizes the issuer to
`https://accounts.google.com`, and leaves the email empty without boolean
`email_verified: true`. Keep the issuer and subject link even with this filter.
Google also supports [native login](native-login.md) for mobile apps.

See the [`Google` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#Google).

## Microsoft

Choose Microsoft for Entra ID or Microsoft-account sign-in. Register the
callback URL and obtain the application's client ID and secret. Replace the
provider argument with a deliberately chosen tenant policy:

```go
provider := oauth.Microsoft("organizations")
```

Pass it to `oauth.New`, keep `Start` for discovery, and use the shared browser
routes and mapper. `organizations` rejects the reserved personal-account tenant;
`consumers` accepts only that tenant (`9188040d-6c67-4c5b-b112-36a304b66dad`);
`common` accepts both classes. For a single tenant, pass the tenant ID, matched
case-insensitively, not a domain name such as `contoso.onmicrosoft.com`.

Both the callback and bearer checks enforce the verified `tid` policy and
require `oid`. The issuer must be `https://login.microsoftonline.com/<tid>/v2.0`,
and the final subject is `oid`. The check uses tenant context, not the home
`idp`: a personal account invited into an organization can pass `organizations`
in that organization's context. Add a narrower tenant allowlist in your mapper
if the selected preset is broader than your policy.

Default scopes are `openid`, `profile` and `email`. The email stays empty unless
the optional `xms_edov` claim is boolean true, so configure that claim in the app
registration if you need it. It is still not a local-account key.

For IAM invitations based on provider-verified email, configure `xms_edov` as an
optional **ID-token** claim. Also configure the `auth_time` ID-token claim if
you need the earlier provider authentication time for freshness checks.
Merge the entries into `optionalClaims.idToken` without removing other claims:

```json
{
  "optionalClaims": {
    "idToken": [
      { "name": "xms_edov" },
      { "name": "auth_time" }
    ]
  }
}
```

See Microsoft's [optional-claim configuration](https://learn.microsoft.com/en-us/entra/identity-platform/optional-claims).
Newly issued tokens contain the configured claims; existing tokens do not
change. Check claims in a controlled development tenant without logging or
sharing complete tokens. A plain mutable `email` claim alone is not sufficient
for this preset. A tenant ID includes guests admitted into that tenant; it is
not itself a local membership decision. See [SSO](../iam/sso.md).

See the [`Microsoft` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#Microsoft).

## GitHub

Choose GitHub for plain OAuth2 sign-in. Register a GitHub OAuth application and
the callback URL, then pass its ID and secret to the shared constructor with:

```go
provider := oauth.GitHub()
```

Use the shared GET callback and your mapper. `Start` does nothing because this
adapter has configured endpoints rather than discovery. Default scopes are
`read:user` and `user:email`, and PKCE stays enabled.

After the code exchange, the preset calls the user and email APIs. Its issuer is
`https://github.com`, and the subject is the positive numeric user ID, not the
mutable login name. A missing, zero or negative ID, and user or email API
failures, return `ErrProviderUnavailable` (`502`) before mapping. The email is
the primary verified address, or empty. This preset cannot verify OIDC bearer
tokens.

See the [`GitHub` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#GitHub).

## Apple

Choose Apple for Sign in with Apple. Register an HTTPS callback and a Services
ID, obtain your team ID and key ID, and load the Apple-issued `.p8` signing key
with `key.LoadSigning` from `arkoren.dev/foundation/key`. Do not generate an
unrelated local replacement for a key registered with Apple.

```go
appleSigning, err := key.LoadSigning("AuthKey_ABC123DEFG.p8")
```

Handle that error, then construct the provider:

```go
provider := oauth.Apple(teamID, keyID, appleSigning)
```

Use the Services ID as `oauth.New`'s client ID, omit `ClientSecret`, and
configure `RedirectURL("https://app.example.com/oauth/apple/callback")`. The
constructor rejects an invalid key, and a `RedirectURL` that is not absolute
with a path. The provider signs a short-lived client secret for each code
exchange, requires discovery, requests `openid`, `name` and `email`, and forces
PKCE off.

Apple sends the callback as a cross-site POST (`form_post`). Mount only that
callback outside CSRF protection, while keeping the session middleware, and keep
CSRF on logout and every other state-changing browser route. Mount the same
handler for GET too, because the GET finishes the login. With `r` your
session-enabled router:

```go
r.Post("/oauth/apple/callback", client.CallbackHandler(guard, mapIdentity))
r.Get("/oauth/apple/callback", client.CallbackHandler(guard, mapIdentity))
```

The encrypted state cookie protects this callback and uses `SameSite=None`, so
HTTPS is required. The browser does not send your `SameSite=Lax` session cookie
with Apple's cross-site POST, so the POST only verifies the identity, keeps it
in a 30-second sealed cookie and answers `303` to the same path. The top-level
GET navigation that follows carries the session and runs your mapper, so
connecting Apple to a logged-in user links the identity to the session present
at that GET. The session cookie must be `SameSite=Lax` (the default); with
`Strict`, connecting fails because the cookie is not sent. A GET that carries a
code is rejected, and an identity too large for one cookie (about 3800 bytes)
fails the POST.

Apple sends the name only on first authorization, in an unsigned form field. It
is added as `Claims["name"]` on that login, so store it then, as optional
display text and not an authorization claim. The email can be a private relay
address, and `EmailVerified` follows the token's `email_verified` claim. Apple
also supports [native login](native-login.md).

### Upgrading

Mounting only `r.Post` for Apple no longer works: add the `r.Get` route for the
same path, as above. This is a breaking change for `form_post` providers. The
[IAM](../iam/overview.md) ark already mounts both methods.

See the [`Apple` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#Apple).

## Generic OIDC

Choose this adapter for another OIDC service. Register a browser client and
callback with the service, then configure its trusted issuer URL:

```go
provider := oauth.OIDC("https://id.example.com/realms/invoices")
```

Pass the provider and registered credentials to the shared constructor, and keep
discovery startup and the GET callback. The default scopes contain only `openid`;
if the service supports them and you need them, use
`ClientBuilder.Scopes("openid", "profile", "email")`. PKCE is enabled by
default. No provider-specific tenant policy is inferred, so implement those
checks before linking a local account.

The service's `email_verified` claim is not trusted by default. With the default,
`External.Email` is empty and `EmailVerified` false whatever the token says,
because many OIDC services let users set an address they never proved, and
trusting it would let such a user take over a local account with the same email
(the [IAM facade](../iam/federated-sign-in.md#unverified-accounts) does exactly
that for a verified email). Turn trust on only for a service you operate or whose
address verification you have reviewed:

```go
provider := oauth.OIDC("https://id.example.com/realms/invoices", func(provider *oauth.OIDCBuilder) {
	provider.TrustEmail(true)
})
```

With trust on, the email is reported, and `EmailVerified` is true, only when the
token carries `email_verified` as the boolean `true`; a string, `false` or a
missing claim still leaves the email empty. Match users on the issuer and subject
either way. A browser login also requires the ID token's `azp` claim to equal your
client ID when the token names more than one audience.

See the [`OIDC` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#OIDC).

## Generic OAuth2

Choose this adapter only when the provider has OAuth2 but no OIDC. Register your
application and callback, and obtain the provider's trusted authorization and
token URLs. Supply your own authenticated user-info function:

```go
provider := oauth.OAuth2(oauth.Endpoint{
	AuthURL:  "https://id.example.com/authorize",
	TokenURL: "https://id.example.com/token",
}, loadIdentity)
```

`loadIdentity` is an `oauth.UserInfoFunc`: it receives a `context.Context` and
an authenticated `*http.Client` and returns an `auth.External`. Put the display
name in `Claims` and set `EmailVerified` only on positive evidence. It must call
the provider's user API with that context, check the status, close the response
body, and validate a nonempty, stable issuer and subject and any email policy.
The generic adapter imposes no provider-specific subject schema.

Pass the provider and registered credentials to the shared constructor and use
its GET callback. There are no default scopes, so configure `Scopes` for the
provider's user API. Endpoints are prepared without network I/O, and `Start`
does nothing. PKCE starts enabled; disable it only if the provider cannot
support it. User-info failures become `502`, and `Verify` is unsupported because
there is no OIDC verifier.

See the [`OAuth2` and `UserInfoFunc` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#OAuth2).
