---
title: Federated Sign-In
description: Add external sign-in, control first-login sign-up per provider, and let signed-in users connect or disconnect their own identities.
order: 50
summary:
  - Mount configured OAuth clients and decide what an unknown external identity may do.
  - Connect a provider only through an explicit session-bound intent, never by matching a verified account's email.
  - Understand unverified-account takeover, native mobile login, and Apple's callback requirements.
---

# Federated Sign-In

Users can sign in with an external account instead of another password.
IAM stores a **connection** from the provider's verified issuer and subject
to a local user subject. Later sign-ins use that connection, not the user's
email or mutable profile.

Set up and start your clients using [OAuth](../oauth/overview.md) first.
That module owns provider credentials, discovery, callback verification,
and redirects; IAM owns local users, connections, and login policy.
The [provider comparison](../oauth/providers.md#compatibility-matrix)
explains differences between Google, Microsoft, GitHub, Apple, and generic
adapters.

## Add Providers

Give each configured client its own call in the constructor:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Provider(google)
	identity.Provider(github)
})
```

The clients satisfy `iam.Provider`: IAM asks them for their name,
login handler, and callback handler. You do not write a resolver for mounted
providers. Apply the connection migrations before serving.

`GET /auth/google` starts Google login; both `GET` and `POST`
`/auth/google/callback` are mounted for its callback. Other clients follow the
same name-based pattern. Names must be unique safe path segments containing
only letters, digits, hyphens, and underscores, or `New` fails.
Generic OAuth clients default to `oauth`; rename them in their client settings
if you use more than one.

A connected identity logs its local user in unless that user is disabled or
deleted. If the user also has a second factor, the browser callback redirects
to the client's configured `ChallengeURL`. Configure a front-end challenge
page on every client whose users may need one; without it, such a callback
fails rather than bypassing two-factor. The page completes
[`POST /login/challenge`](two-factor.md#complete-a-login).

## First-Time Sign-In

An unknown external identity normally receives `403`
`federation:no-account`. The exception is a provider-verified email that
[claims an unverified built-in account](#unverified-accounts).
Successful authentication at the provider does not otherwise grant membership
in your application.

To let one provider create new users, enable sign-up for that provider:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Provider(google)
	identity.Provider(github, func(login *iam.ProviderBuilder) {
		login.SignUp(true)
	})
})
```

Here GitHub can create a user; Google cannot. Both can still sign in connected
identities or connect a provider from an existing user's settings.

Sign-up requires a nonempty email that the provider reports as verified;
otherwise it returns `403` `federation:email-unverified` and creates nothing.
New users start verified. With [your own users](own-users.md), IAM calls
`Users.Create` with that email and verified flag; enabling sign-up without
`Users.Create` is a construction error.

A matching email is not permission to attach a new identity to an existing
**verified** account. With sign-up enabled, such a collision is `409`
`account:email-taken`; with sign-up disabled, it remains `403`
`federation:no-account`. The owner logs in another way and connects the provider.
Concurrent first sign-ins of the same identity create one connected user.

If you need an additional sign-up rule, such as checking invitations, add
`login.Create(checkInvitation)` in that provider's callback.
`checkInvitation` is your
`func(context.Context, auth.External) (string, error)`: return an error to
refuse, `""` and nil to continue with normal user creation, or a newly created
subject to handle creation yourself. Never return an existing user found by
email. Multiple `Create` hooks run in order until one returns a subject;
errors reach the client, so use a problem value for a safe public refusal.
This hook applies to sign-up, not every later login.

## Unverified Accounts

There is one deliberate email-based exception. If someone registered Ada's
address without proving it, a provider identity with a verified matching email
can claim that **unverified built-in account**. Without this rule, the earlier
registrant could block the owner.

In one database transaction, IAM marks the address verified, deletes the old
password and attached connections, authenticator, recovery codes, and tokens,
rotates the credential version, and connects the owner's identity.
Database failure rolls back that takeover. Rotation invalidates the earlier
holder's sessions and pending logins; after commit, IAM also attempts
best-effort removal of their cached login records.

The takeover emits `federation.AccountClaimed` alongside `LinkCreated`.
It applies even with provider sign-up disabled, because it creates no user.
A disabled user cannot be claimed or re-enabled this way.
An already verified account is never taken over.

This rule trusts the provider's email evidence with account ownership.
Google, GitHub, Apple, and Microsoft presets derive it from their own
verified-email checks; Microsoft requires `xms_edov`.
Generic OIDC reports no email by default. Enable its `TrustEmail(true)` only
after reviewing the provider's address-verification policy.
See [provider email handling](../oauth/providers.md) before enabling sign-up
or using pre-created unverified accounts.

For application-owned credentials, register
[`OnEmailProven`](own-users.md#additional-credentials) so a takeover or recovery
also removes credentials IAM does not know about. Automatic provider
takeover is unavailable with a custom user store; it cannot assume that your
login identifier is an email.

## Connect a Provider

A user who already has an account connects a provider from account settings.
`POST /connections/{provider}` requires a session, a
[fresh proof](freshness.md), and a verified email.
It stores a single-use intent in that session for ten minutes and returns
`303` to the provider's login URL. The callback must complete in the same session.

Do not use a plain link or HTML form: a GET to provider login must not link an
identity, and the connect POST requires the JSON media type.
In a same-origin front end, first store the intent:

```js
const response = await fetch(`/connections/${provider}`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  redirect: "manual",
});
```

Here `provider` is an enabled provider name selected by your UI.
With manual redirects, a successful `303` appears as an opaque redirect.
Check that result before navigating:

```js
if (response.type !== "opaqueredirect") {
  throw new Error(`Connect failed with status ${response.status}`);
}
location.assign(`/auth/${provider}`);
```

Failures such as `401`, `403`, `404`, or `415` remain ordinary responses;
display the problem and, if needed, confirm identity before trying again.
An unknown provider is `404`. Provider sign-up may stay disabled throughout
this connection flow.

The callback returns `409` if the identity belongs to someone else, or the user
already has another connection for that issuer. Each user can have one
connection per issuer.

## List and Disconnect

`GET /connections` needs a session and returns connection metadata:
`id`, `issuer`, `created_at`, and `last_used_at`. IAM stores no provider
email or profile data in the connection.

`DELETE /connections/{id}` also needs a fresh proof and returns `204`.
Removing the last login method returns `409` `federation:last-link`;
the user must first have a password or another connection. An unknown or
unowned connection is `404`.

Connections publish `federation.LinkCreated` and `LinkDeleted` for
[notifications](accounts.md#security-notifications).
Provider login does not replace a local step-up proof; see
[Fresh Logins](freshness.md#provider-logins).

## Native Login

Google and Apple also support native ID-token login through their mobile SDKs.
IAM mounts nonce and native-login routes for clients implementing
`iam.NativeProvider`. The shared OAuth client has those methods for every
preset, but presets without native support return `oauthauth.ErrUnsupported`;
route presence is not proof of support.

For a supported, audience-configured client:

1. `GET /auth/{name}/nonce` returns a nonce and stores it in the temporary
   session. Pass it to the provider SDK as required by that provider.
2. Submit the SDK's ID token to `POST /auth/{name}/native`:

```json
{
  "id_token": "..."
}
```

The result is `204`, or `202` when a second-factor challenge remains.
Keep the same cookie jar for the nonce, native login, and any challenge;
then [exchange the login for a device token](tokens.md#exchange-a-mobile-login).
Configure the accepted app client IDs with `NativeAudiences`.
[Native Login](../oauth/native-login.md) covers Google's raw nonce and Apple's
hashed nonce requirements.

## Apple Callback

Apple sends a cross-site form POST. Keep CSRF protection on account routes,
but exempt exactly Apple's callback pattern. Its sealed OAuth state and
single-use callback handoff protect the protocol route instead.

With standard-library `net/http`, create the narrowly scoped exception:

```go
engine := http.NewCrossOriginProtection()
engine.AddInsecureBypassPattern("POST /auth/apple/callback")
```

Pass that protection to `csrf` from
`arkoren.dev/core/http/middleware/csrf` when mounting IAM:

```go
web := routes.With(csrf.Middleware(func(protection *csrf.Builder) {
	protection.Protection(engine)
}), identity.Middleware())
identity.Mount(web)
```

Use your client's actual name if it is not `apple`.
HTTPS is required. Keep the default `SameSite=Lax` session policy:
Apple's POST verifies the identity and redirects to a GET on the same callback;
that GET must receive the existing session to connect the right account.
`Mount` registers both methods. Do not add a second callback or disable CSRF
for the whole authentication area.

Next, use [Sessions and Accounts](accounts.md) to manage the local access
these login methods create, or apply this flow to
[SSO](sso.md).

For connections and resolver hooks, see the
[`federation` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/federation).
