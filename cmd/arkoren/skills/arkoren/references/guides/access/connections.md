---
title: Connections
description: Sign in with external providers, connect them to existing accounts and handle native mobile login, with the access facade.
order: 60
summary:
  - Add providers that satisfy the facade's interface, such as the OAuth module's clients.
  - Understand how an unknown identity creates a user, why accounts are never linked by email and how connecting works.
  - Handle native ID-token login, disconnecting and how a provider login interacts with step-up.
---

# Connections

Many users prefer to sign in with an account they already have. A connection links
one external identity, an issuer and a subject that a provider verified, to one
local user. Hand the facade the [OAuth](../oauth/overview.md) clients you set up,
and it mounts their routes and maps the verified identities to users:

```go
iam.Provider(google, github) // *oauthauth.Client values
```

The facade declares a small `access.Provider` interface (`Name`, `LoginHandler`
and `CallbackHandler`) that `*oauthauth.Client` satisfies, so the two modules never
import each other. Each provider is mounted under its name, `GET /auth/google` and
`GET` or `POST /auth/google/callback`. Names must be unique, and `New` rejects a
duplicate. Clients for generic OIDC providers are all called `oauth` until you
rename them; see [Providers](../oauth/providers.md). You write no resolver: the
facade uses its connection store.

## Signing In

A known identity logs its user in, unless the user was deleted or disabled. An
unknown identity creates a user. With the built-in users, the user is identified
by the provider's email and starts verified only when the provider vouches for the
email. A provider that sends no email cannot create a built-in user. With
[your own users](own-users.md), `Users.Create` receives the provider's email and
whether it was verified. Without `Users.Create`, an unknown identity is refused
with `401` (`federation:not-linked`).

Accounts are never linked by email. If the email belongs to an existing user, the
callback answers `409`: that user logs in and connects the provider from their
settings. Concurrent first logins of one identity create one user.

A login with a second factor enrolled is held pending like any other. The callback
redirects to the OAuth client's challenge URL, where the page posts to
`/login/challenge`. Set `ChallengeURL` on every OAuth client whose users may have
two-factor on; without it the callback fails. See
[Browser Login](../oauth/browser-login.md) for the routes, login state and failures
on the OAuth side.

## Connecting a Provider

A logged-in user connects a provider from their settings with
`POST /connections/{provider}`. It needs a browser session, a fresh login and a
verified email (`403` `email-unverified` otherwise), so whoever registered an
address first cannot attach a provider before its owner proves it. It stores a
single-use intent in the session for ten minutes and answers `303` to
`/auth/{provider}`. When that callback completes in the same session, the identity
is linked to the logged-in user instead of creating one. An unknown provider name
is `404`.

Because connecting is a POST, a cross-site request to the provider's login route
alone can never link an identity to a victim.

An identity that belongs to another user is `409`, and a user holds one connection
per issuer. `GET /connections` lists them as `id`, `issuer`, `created_at` and
`last_used_at`, and needs a browser session. `DELETE /connections/{id}` removes one
and needs a fresh login. Removing the user's last way to log in is `409`: the user
needs a password or another connection first. No email or profile data is stored.

## Native Login

Providers that also implement `access.NativeProvider`, the Google and Apple
clients, get two more routes for mobile apps that sign in with the provider's SDK:

1. `GET /auth/{name}/nonce` returns `{"nonce": "..."}` and stores it in the
   session. The app passes it to the SDK.
2. `POST /auth/{name}/native` with `{"id_token": "..."}` verifies the token and
   answers like any login: `204`, or `202` with the second-factor methods.

The app keeps its cookie jar for these two calls, then exchanges the login for a
[device token](tokens.md#mobile-login). The OAuth client must list the app's client
IDs with `NativeAudiences`; see [Native Login](../oauth/native-login.md).

## Step-Up

A provider login proves who the user is, but it is not a step-up method. A user who
has a password or an authenticator is not made fresh by signing in again through a
provider, so changing a credential still needs `POST /confirm` with one of their
methods. A user who signs in only through providers has no step-up method and is
fresh right after logging in. The server's clock stamps the proof at the callback;
whatever the provider says about when the user authenticated is ignored.

Connections publish `federation.LinkCreated` and `federation.LinkDeleted` events.

For every function and option, see
[the `federation` API reference](https://pkg.go.dev/arkoren.dev/ark/access/federation).
