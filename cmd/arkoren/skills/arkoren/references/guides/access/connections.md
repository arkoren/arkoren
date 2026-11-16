---
title: Connections
description: Sign in with external providers, connect them to existing accounts and handle native mobile login, with the access facade.
order: 60
summary:
  - Add providers that satisfy the facade's interface, such as the OAuth module's clients.
  - Decide whether an unknown identity may create a user, why sign-up needs a proven email, why accounts are never linked by email and how connecting works.
  - Handle native ID-token login, disconnecting, Apple's form post and how a provider login interacts with step-up.
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

A known identity logs its user in, unless the user was deleted or disabled. What
happens to an unknown identity is your decision, and by default it is refused:
the callback answers `403` with the problem `federation:no-account` and creates
nothing. The identity is verified, but the application has not opened sign-up
with providers, so `403` fits better than `401`, which would invite retrying with
other credentials. Nothing differs between identities, so the answer does not
reveal who has an account.

To let a first provider login create a user, enable sign-up:

```go
iam.Provider(google, github)
iam.Federation(func(links *federation.Builder) {
	links.SignUp(true)
})
```

Sign-up needs an email the provider proved. A first login whose identity has no
verified email, or no email at all, creates nothing and is refused with `403` and
the problem `federation:email-unverified`: an account made from an unproven
address would be an unverified one that the address's real owner could not tell
from their own. A person in that position verifies the email with the provider or
signs in another way. The new user is identified by the provider's email and
starts verified. With [your own users](own-users.md), `Users.Create` receives the
provider's email and `SignUp(true)` without `Users.Create` makes `New` fail with
`access.ErrInvalidConfig`.

Sign-up does not affect the other two cases: an identity that is already
connected always logs its user in, and a logged-in user can always connect a
provider. With registration and sign-up both off, add users yourself; see
[Adding Users](accounts.md#adding-users).

Accounts are never linked by email, with one exception for unverified accounts
described under [Unverified Accounts](#unverified-accounts). If the email belongs
to an existing, verified user, the callback answers `409`: that user logs in and
connects the provider from their settings. Concurrent first logins of one
identity create one user when sign-up is on.

A login with a second factor enrolled is held pending like any other. The callback
redirects to the OAuth client's challenge URL, where the page posts to
`/login/challenge`. Set `ChallengeURL` on every OAuth client whose users may have
two-factor on; without it the callback fails. See
[Browser Login](../oauth/browser-login.md) for the routes, login state and failures
on the OAuth side.

## Unverified Accounts

Registering with a password creates an unverified user, so someone can register
an address that is not theirs. When the real owner later arrives with a provider
that verified that address, the owner takes the account over instead of being
turned away. In one transaction the facade:

1. deletes the stored password of the registered user;
2. deletes what that user attached: connections, authenticator, recovery codes and
   tokens;
3. marks the email verified;
4. rotates the user, which ends every session of the person who registered it;
5. connects the new identity and logs the owner in.

If any step fails, nothing changes. After the commit the facade also deletes the
login records of that user, so the registrant's logins disappear from the session
list as well as stopping to work, and it publishes `federation.AccountClaimed`
with the subject, next to `LinkCreated`. Listen for it to notify the owner or
audit the takeover. This is the same removal a
[password reset](passwords.md#verify-emails) performs when it proves the
mailbox, plus the password. It needs no sign-up, because no user is created, so
it also applies when sign-up is off. A disabled user is never taken over: the
callback answers `401` and nothing changes.

Only a verified email counts. The `EmailVerified` of the provider's identity must
be true and the existing user must be unverified; an unverified or empty email
goes through the usual rules and a verified user is never touched. The OAuth
presets set it from the provider's own evidence: Google's `email_verified`,
GitHub's primary verified address, Apple's `email_verified` and Microsoft's
`xms_edov` claim (the email domain is verified by its owner). A generic
`oauth.OIDC` client reports no email at all unless you opt in with
`TrustEmail(true)`, as [Providers](../oauth/providers.md#generic-oidc) explains, so
with it off such a provider can sign people in but can neither create a user nor
take an account over. Microsoft's plain
`email` claim is mutable and unverified, so the preset withholds the address
without `xms_edov`: add that optional claim in the app registration, or Microsoft
logins never carry an email. If you write your own provider, set `EmailVerified`
only on positive evidence from it, because this rule trusts it with an account.

## Connecting a Provider

A logged-in user connects a provider from their settings with
`POST /connections/{provider}`. It needs a browser session, a fresh login and a
verified email (`403` `email-unverified` otherwise), so whoever registered an
address first cannot attach a provider before its owner proves it. It stores a
single-use intent in the session for ten minutes and answers `303` to
`/auth/{provider}`. When that callback completes in the same session, the identity
is linked to the logged-in user, whether or not sign-up is on. An unknown provider name
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

## Apple Callback

Sign in with Apple answers with a cross-site `POST` form to
`/auth/apple/callback`. A browser sends that request with an `Origin` of
`appleid.apple.com`, which core's CSRF middleware refuses unless you allow this one
route. The sealed login cookie and the `state` value are the defense for this
route, so exempt only its pattern, on a protection you give the middleware:

```go
engine := http.NewCrossOriginProtection()
engine.AddInsecureBypassPattern("POST /auth/apple/callback")

web := routes.With(csrf.Middleware(func(protection *csrf.Builder) {
	protection.Protection(engine)
}), iam.Middleware())
iam.Mount(web)
```

Use the name of your Apple client in the pattern; the facade mounts it at
`/auth/{name}/callback`, and the Apple preset is named `apple`. Without the
exemption the callback is blocked and no Apple login completes. The same
route-level `POST` needs `SameSite=Lax` session cookies, which are the default;
see [Browser Login](../oauth/browser-login.md).

## Step-Up

A provider login proves who the user is, but it is not a step-up method. A user who
has a password or an authenticator is not made fresh by signing in again through a
provider, so changing a credential still needs `POST /confirm` with one of their
methods. A user who signs in only through providers has no step-up method and is
fresh right after logging in. The provider's `auth_time` claim can only make the
login older than the callback, never fresher; [Fresh Logins](freshness.md#provider-logins)
explains what that means for each provider.

Connections publish `federation.LinkCreated` and `federation.LinkDeleted` events.

For every function and option, see
[the `federation` API reference](https://pkg.go.dev/arkoren.dev/ark/access/federation).
