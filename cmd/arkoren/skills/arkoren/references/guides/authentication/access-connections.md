---
title: Access Connections
description: Sign in with external providers, connect them to existing accounts and handle native mobile login, with the access facade.
order: 41
summary:
  - Add providers that satisfy the facade's interface, such as the OAuth module's clients.
  - Understand how an unknown identity creates a user, why accounts are never linked by email and how connecting works.
  - Handle native ID-token login, disconnecting and how a provider login interacts with step-up.
---

# Access Connections

Hand the facade the [OAuth](oauth.md) clients you started, and it mounts their
routes and maps the verified identities to users:

```go
iam.Provider(google, github) // *oauthauth.Client values
```

The facade declares a small `access.Provider` interface (`Name`, `LoginHandler`,
`CallbackHandler`) that `*oauthauth.Client` satisfies, so the two modules never
import each other. Each provider is mounted under its name, `GET /auth/google`
and `GET` or `POST /auth/google/callback`; names must be unique, and clients for
generic OIDC providers are all called `oauth` until you rename them. You write no
resolver: the facade uses its connection store.

## Sign In

A known identity (issuer and subject) logs its user in, unless the user was
deleted or disabled. An unknown one creates a user, with the built-in users by the
provider's email: the user starts verified only when the provider vouches for the
email (`auth.External.EmailVerified`). With [your own users](access.md#bring-your-own-users),
`Users.Create` receives the provider's email.

Accounts are never linked by email. If the email belongs to an existing user, the
callback answers `409`; that user logs in and connects the provider from their
settings. A first login whose email is missing cannot create a built-in user.
Concurrent first logins of one identity create one user.

A login with a second factor enrolled is held pending like any other: the callback
redirects to the OAuth client's challenge URL, where the page posts to
`/login/challenge`. Set `ChallengeURL` on every OAuth client whose users may have
two-factor on; without it the callback ends the pending login and fails.

## Connect a Provider

A logged-in user connects a provider from their settings with
`POST /connections/{provider}`, which needs a fresh login. It stores a
single-use intent in their session for ten minutes and answers `303` to
`/auth/{provider}`. When that callback completes in the same session, the identity
is linked to the logged-in user instead of creating one. Because connecting is a
POST, a cross-site request to the provider's login route alone can never link an
identity to a victim.

An identity that belongs to another user is `409`, and a user holds one
connection per issuer. `GET /connections` lists them as `{id, issuer, created_at,
last_used_at}`, and `DELETE /connections/{id}` removes one, needing a fresh
login. Removing the last way to log in is `409`: the user needs a password or
another connection first. No profile data is stored.

## Native Login

Providers that also implement `access.NativeProvider`, the Google and Apple
clients, get two more routes for mobile apps that sign in with the provider's SDK:

1. `GET /auth/{name}/nonce` returns `{"nonce": "..."}` and stores it in the
   session; the app passes it to the SDK.
2. `POST /auth/{name}/native` with `{"id_token": "..."}` verifies the token and
   answers like any login: `204`, or `202` with the second-factor methods.

The app keeps its cookie jar for these two calls, then exchanges the login for a
[device token](access-tokens.md#mobile-login). The OAuth client must list the
app's client IDs with `NativeAudiences`.

## Step-Up and Providers

A provider login proves who the user is, but it is not a step-up method. A user
who has a password or an authenticator is not made fresh by signing in again
through a provider, so changing a credential still needs `POST /confirm` with
one of their methods. A user who signs in only through providers has no step-up
method and is fresh right after logging in. The server's clock stamps the proof at
the callback; whatever the provider says about when the user authenticated is
ignored.

For every function and option, see
[the `federation` API reference](https://pkg.go.dev/arkoren.dev/ark/access/federation).
