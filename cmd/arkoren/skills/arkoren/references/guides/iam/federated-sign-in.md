---
title: External Sign-In
nav: External Sign-In
description: Let users sign in with Google, GitHub, Apple, or OIDC providers, control first-login sign-up, connect accounts from settings, and support mobile native login.
order: 60
summary:
  - Configure and start OAuth clients, then register them with IAM to mount their login and callback routes.
  - Control first-time sign-up per provider, add custom creation rules, and understand unverified-account takeover.
  - Let signed-in users connect or disconnect providers, store optional profile claims, and handle native mobile login and Apple callbacks.
---

# External Sign-In

Users often prefer signing in with an existing Google, GitHub, or Apple account instead of creating another password. IAM handles external sign-in by storing a **connection** that maps the provider's verified issuer and subject to your local user subject. Subsequent logins match that immutable connection, never a mutable email address or profile name.

Responsibilities are cleanly split between two arks: [OAuth](../oauth/overview.md) handles provider credentials, OIDC discovery, token verification, and redirects; IAM handles local users, connections, sign-up policy, and sessions. See the [provider comparison](../oauth/providers.md#compatibility-matrix) for differences across Google, Microsoft, GitHub, Apple, and generic OIDC.

## Configuring OAuth Clients

Register your application with each provider and whitelist the exact callback URLs you plan to serve (for example, `https://app.example.com/auth/google/callback`). Both clients below use your existing `*crypto.Encrypter` (`cipher`) with a stable key shared across all instances that may receive callbacks; see [Encryption Keys](../encryption/keys.md).

Import `oauthauth` from `arkoren.dev/ark/oauth/oauthauth` and construct each client:

```go
google, err := oauthauth.New(oauthauth.Google(), cipher, googleID,
	func(client *oauthauth.ClientBuilder) {
		client.ClientSecret(googleSecret)
		client.RedirectURL("https://app.example.com/auth/google/callback")
		client.ChallengeURL("/login/challenge")
	},
)
```

```go
github, err := oauthauth.New(oauthauth.GitHub(), cipher, githubID,
	func(client *oauthauth.ClientBuilder) {
		client.ClientSecret(githubSecret)
		client.RedirectURL("https://app.example.com/auth/github/callback")
		client.ChallengeURL("/login/challenge")
	},
)
```

Each preset gives the client a default name (`google`, `github`) that IAM uses in its route paths. Generic OIDC clients default to `oauth`, so rename them with `client.Name(...)` if you mount more than one. Configure `ChallengeURL` whenever users may have local two-factor enabled; without it, a callback for a two-factor user fails rather than bypassing the challenge.

Start each client before serving requests (or wire `Start` into your [application startup lifecycle](../oauth/overview.md#start-discovery)):

```go
err = google.Start(ctx)
err = github.Start(ctx)
```

Google uses OIDC discovery and returns `503` until `Start` succeeds; GitHub uses plain OAuth2 and its `Start` is a no-op.

## Registering Providers with IAM

Pass each client to `identity.Provider(...)` inside `iam.New`:

```go
identity.Provider(google)
identity.Provider(github)
```

Each `*oauthauth.Client` satisfies `iam.Provider`, so you do not write a custom resolver. Provider names must be unique URL-safe segments (letters, digits, hyphens, underscores).

When you mount IAM at `/auth` with `web.Group("/auth", identity.Register)`, IAM automatically mounts `GET /auth/{name}` and both `GET` and `POST /auth/{name}/callback` for every registered provider. Add a link on your login page to start the browser flow:

```html
<a href="/auth/google?return_to=/projects">Continue with Google</a>
```

`return_to` must be a safe local path (defaulting to `/`). After a connected user authenticates at the provider, the callback establishes a local session and redirects to `return_to`, or to `ChallengeURL` if the user has a local second factor enrolled (without forwarding `return_to`). Disabled or deleted users cannot sign in.

## First-Time Sign-In

By default, external providers only sign in users who are **already connected** to a local account. An unknown identity returns `403` `federation:no-account`: authenticating at Google or GitHub does not automatically grant membership in your app.

To allow new users to join automatically through a specific provider, enable `SignUp(true)` on that provider alone:

```go
identity.Provider(google)
identity.Provider(github, func(login *iam.ProviderBuilder) {
	login.SignUp(true)
})
```

Here GitHub creates accounts on first sign-in while Google remains invite- or connect-only. This setting is independent of password registration.

Sign-up rules:

- **Verified email required**: The provider must supply a nonempty, verified email; otherwise IAM returns `403` `federation:email-unverified` and creates nothing. New users start with a verified email.
- **No silent merging**: If the email already belongs to a **verified** local account, IAM never links them automatically. With sign-up on, it returns `409` `account:email-taken`; with sign-up off, `403` `federation:no-account`. The owner must sign in another way and connect the provider from settings.
- **Race-safe**: Concurrent first logins for the same external identity create only one local user.

### Custom Sign-Up Rules

To enforce an extra condition on first-time sign-up, such as checking an invitation list, attach a `Create` hook to the provider:

```go
identity.Provider(github, func(login *iam.ProviderBuilder) {
	login.SignUp(true)
	login.Create(checkInvitation)
})
```

`checkInvitation` has the signature `func(context.Context, auth.External) (string, error)`:

- Return a **problem error** to refuse sign-up with a safe client-facing message.
- Return `"", nil` to let IAM create the user normally.
- Return a **newly created subject** to handle user creation yourself (never return an existing user matched by email).

Multiple `Create` hooks run in order until one returns a subject. This hook runs only during new-user sign-up, not on subsequent logins.

## Unverified Account Takeover

There is one deliberate email-based exception. If someone previously registered `ada@example.com` via password sign-up without verifying the mailbox, a provider login with a **verified** matching email claims that **unverified built-in account** so an unverified registrant cannot squat on the real owner's address.

In a single database transaction, IAM marks the address verified, deletes the old password, connections, two-factor enrollment, recovery codes, and tokens, rotates the credential version (invalidating prior sessions), and links the owner's external identity. It emits `federation.AccountClaimed` alongside `federation.LinkCreated`.

> [!WARNING]
> Because this rule trusts the provider's verified-email claim with account ownership, enable `TrustEmail(true)` on generic OIDC providers only after verifying their email-verification policy. Google, GitHub, Apple, and Microsoft presets derive verification from their own provider checks (Microsoft requires `xms_edov`).

This takeover applies even when `SignUp` is disabled (since no new user is created), never affects already verified or disabled accounts, and is available only with the built-in directory. If your application stores its own credentials outside IAM, register [`OnEmailProven`](own-users.md#custom-credentials) so they are purged in the same transaction.

## Connecting and Disconnecting Accounts

An existing signed-in user can link a provider from their account settings. `POST /connections/{provider}` requires a browser session, a [fresh proof](freshness.md), and a verified email. It stores a single-use 10-minute intent in the session and responds with `303` to the provider's login route.

Do not link accounts with a plain `<a>` tag or HTML form: the connect endpoint requires `POST` with `Content-Type: application/json`. From your same-origin front end, store the intent with manual redirect handling, then navigate to the provider:

```js
const response = await fetch(`/connections/${provider}`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  redirect: "manual",
});
if (response.type !== "opaqueredirect") {
  throw new Error(`Connect failed with status ${response.status}`);
}
location.assign(`/auth/${provider}`);
```

If the callback completes in the same session, the provider is linked even if `SignUp` is off. Each user can hold one connection per issuer; linking an identity owned by someone else or duplicating an issuer returns `409`.

To display and remove connections in settings:

- `GET /connections` (session required) returns each connection's `id`, `issuer`, `created_at`, `last_used_at`, and any kept `claims`.
- `DELETE /connections/{id}` (session + fresh proof) removes a connection and returns `204`. Removing the user's last login method returns `409` `federation:last-link` unless they have a password or another connection.

## Keeping Provider Profile Claims

By default, IAM stores no profile data on connections. If you want to prefill a profile form with fields like `name` or `picture`, allowlist them per provider:

```go
identity.Provider(google, func(login *iam.ProviderBuilder) {
	login.KeepClaims("name", "picture")
})
```

IAM captures those claims at sign-up, connection, and every subsequent login, updating claims present in the latest login while preserving older ones (so Apple's first-login-only `name` is retained). Read them via `identity.Links().List(ctx, subject)` or `GET /connections`.

Only strings, booleans, numbers, and string slices are stored, up to 8 KiB per connection. Unsupported types are dropped with a warning, oversized updates are skipped without failing login, and protocol claims (`iss`, `sub`, `aud`, `exp`, `iat`, `nbf`, `nonce`, `at_hash`, `auth_time`, `scope`, `azp`, `acr`, `amr`) or empty names fail `iam.New` with `iam.ErrInvalidConfig`.

> [!WARNING]
> Kept claims are unverified profile hints from the provider and can change on any login. Use them only to prefill user-confirmed forms, never for authorization, identity, or account matching.

## Mobile Native Login

Google and Apple also support native ID-token login from mobile SDKs. When a provider implements `iam.NativeProvider`, IAM mounts two additional endpoints under `/auth/{name}`:

1. `GET /auth/{name}/nonce` returns a nonce and binds it to a temporary session. Pass it to the mobile SDK (raw for Google, hashed for Apple; see [Native Login](../oauth/native-login.md)).
2. `POST /auth/{name}/native` accepts `{"id_token": "..."}` and responds with `204` (or `202` when a second-factor challenge remains).

Keep the same cookie jar across the nonce, native login, and any challenge, then [exchange the login for a device token](tokens.md#mobile-device-tokens). Configure accepted mobile client IDs with `NativeAudiences` on the OAuth client. Because `*oauthauth.Client` implements `NativeProvider` structurally, presets without native support still mount these paths and return `oauthauth.ErrUnsupported`.

## Apple's Cross-Site Callback

Sign in with Apple delivers its callback as a cross-site form `POST`. Keep CSRF protection enabled across your app, and add a narrow bypass for Apple's callback path alone; its sealed OAuth state and single-use callback handoff protect that route instead:

```go
engine := http.NewCrossOriginProtection()
engine.AddInsecureBypassPattern("POST /auth/apple/callback")
```

Pass `engine` into `csrf.Middleware`:

```go
web := routes.With(csrf.Middleware(func(protection *csrf.Builder) {
	protection.Protection(engine)
}), identity.Middleware())
web.Group("/auth", identity.Register)
```

Substitute your client's actual name if it is not `apple`. Keep HTTPS and the default `SameSite=Lax` session cookie so Apple's `POST`-to-`GET` handoff carries the existing session when connecting an account.

Next, see [Organization SSO](sso.md) to apply external sign-in to enterprise workspaces and corporate identity providers.

For connection stores and resolvers, see the [`federation` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/federation).
