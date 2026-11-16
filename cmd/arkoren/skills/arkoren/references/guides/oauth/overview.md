---
title: OAuth Overview
nav: Overview
description: Install the OAuth ark, configure a provider client, start discovery, and see how it plugs into core authentication.
order: 10
summary:
  - Understand what the OAuth ark owns and what your application still owns.
  - Install the ark, build a provider client from registered credentials, and start OIDC discovery.
  - Pick a provider and see how the ark relates to the core guard and the Access ark.
---

# OAuth Overview

The OAuth ark lets a provider check a person's credentials, then creates a
local login for the matching account. OpenID Connect (OIDC) adds verified ID
tokens to the OAuth 2 authorization flow; plain OAuth2 providers instead supply
identity through an authenticated user API. The `oauthauth` package supports
both, for browser sign-in, for mobile apps that sign in with a provider SDK, and
for APIs that accept provider-issued JWTs.

You still own local accounts and the link between a local account and its
external identity. The ark completes the provider flow, calls your mapper, and
logs the account subject the mapper returns in through the core `auth.Guard`
from [Session Login](../authentication/session-login.md), so a registered
[second factor](../authentication/two-factor.md) still applies. It is not an
authorization server, and it does not acquire API tokens for arbitrary clients
or refresh tokens for your service.

## Relation to Core

The ark builds on core authentication and adds no tables of its own. A browser
or native login ends in `guard.Begin` with a federated factor, so everything
the guard does for other logins (session rotation, second factors, step-up)
applies. An API call ends in `auth.Bearer`, as in
[API Tokens](../authentication/api-tokens.md). Use this ark when you write the
account mapping yourself.

If you want registration, login, two-factor, tokens and provider sign-in as
ready-made routes, use the [Access](../access/overview.md) ark: it mounts these
clients for you and links identities through its
[connection store](../access/connections.md), so you write no mapper.

## Configure Clients

Register an application and its exact callback URL with your provider, then
obtain the client ID and any credentials the provider requires. Install the ark
in your module:

```sh
go get arkoren.dev/ark/oauth
```

Import it as `oauth`. The example uses an existing `cipher *crypto.Encrypter`
from [Encryption](../encryption/overview.md), plus `clientID` and `clientSecret`
loaded from your configuration or secret provider:

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
inputs but performs no network I/O. The cipher seals the login cookies, so use a
stable encryption key shared by every instance that can receive a callback; key
provisioning belongs in [Encryption Keys](../encryption/keys.md), not in each
request handler.

For OIDC, the positional `clientID` is the required browser ID-token audience,
the token's intended recipient. `ClientBuilder.Audiences` configures only
[API verification](bearer-tokens.md#prepare-verification), never this callback
check. PKCE starts enabled; disable it only for a provider that cannot support
it. Apple forces it off independently of this setting.

Each client has a name, returned by `client.Name()`: the preset's name from the
[provider matrix](providers.md#compatibility-matrix), or `oauth` for generic
providers. Set `ClientBuilder.Name` to override it. The name prefixes the login
cookies, so give each provider client of one application a distinct name;
an invalid cookie name fails construction. Applications that mount several
providers can use the name in the route, for example `/auth/{name}`. `Name`
replaced the cookie-prefix setting of the same name, whose default was
`arkoren.oauth`.

Configuration callbacks run once, in order, and a nil callback entry panics. The
published client copies its settings and its scope and audience slices, so a
retained builder cannot change it; services and captured application state stay
shared.

## Start Discovery

OIDC clients must discover their endpoints before serving requests. In your
existing [application composer](../application/lifecycle-and-ownership.md),
register startup with `application` from `arkoren.dev/core/application`:

```go
compose := func(composer *application.Composer) {
	composer.Startup(client.Start)
}
```

Before startup succeeds, OIDC handlers return `ErrNotStarted` (`503`). A later
`Start` refreshes the discovered data. Plain OAuth2 clients prepare their
configured endpoints at construction, so their `Start` does nothing. Signing
keys can still be fetched during OIDC token verification.

Keep the returned `*Client` and do not copy it: it holds atomic state. It has no
`Stop` method and does not stop the shared encrypter or event bus; your
application owns those services and any native HTTP client you supply. Keep
encryption available until handlers have finished.

## Choose Providers

Use a preset when it matches your provider, because presets include identity
checks that a generic adapter cannot infer. [Providers](providers.md) compares
Google, Microsoft, GitHub, Apple, generic OIDC and generic OAuth2, and explains
the registration each one needs. Then continue with the task you need:

- [Browser Login](browser-login.md) connects the login and callback routes and
  maps the provider identity to a local account.
- [Native Login](native-login.md) logs in mobile apps that hold a Google or
  Apple ID token.
- [OIDC Tokens](bearer-tokens.md) verifies provider-issued JWTs at an API.

For all functions and options, see the
[`oauthauth` API reference](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth).
