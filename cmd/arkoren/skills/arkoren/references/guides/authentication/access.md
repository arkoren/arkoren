---
title: Access
description: Add registration, login, two-factor, tokens and social sign-in to an application with one facade and a default set of JSON routes.
order: 15
summary:
  - Compose the access facade with the features you enable, run its migrations and mount its default routes.
  - Read the route table, the 204 and 202 login responses and the problem statuses clients receive.
  - See why every credential change needs a browser session and a fresh login, and how to bring your own users.
---

# Access

The pages in this group show how to build login from the pieces: an
authenticator, `auth.Guard` and your own routes. The `access` module is the
same pieces, composed. One facade builds the guard, the stores for passwords,
authenticators, tokens and connections, and the JSON routes that use them, so
you enable the features you want instead of writing each handler.

> [!NOTE]
> `arkoren.dev/ark/access` is not released yet. This page describes the API
> that its first release ships; the install command is added with that release.

Use it when you want these flows working and tested. Keep the hand-built
approach of [Session Login](session-login.md) when your login is unusual enough
that you would replace most of the handlers anyway. The module has no
third-party dependencies and never imports the OAuth module: a provider joins
through a small interface that `oauthauth.Client` satisfies.

## Compose It

The application brings the database, the cache that holds sessions and counters,
a hasher for passwords and a cipher for two-factor secrets. Each feature is off
until you enable it:

```go
iam, err := access.New(db, store, func(iam *access.Builder) {
	iam.Password(hasher)
	iam.TwoFactor(cipher, "Example")
	iam.Tokens()
	iam.Provider(google, github)
	iam.Deliver(send) // reset and verification links
})
if err != nil {
	return err
}
```

`New` does no I/O. Add the migrations of the enabled features to your migrator,
then install the middleware and mount the routes:

```go
if err := migrator.Add(iam.Migrations()...); err != nil {
	return err
}

web := routes.With(iam.Middleware()) // session, guard and token authenticators
iam.Mount(web)

api := routes.With(iam.Middleware(), auth.Require())
api.Get("/me", me)
```

`iam.Middleware()` installs the session middleware and authenticates browser
sessions and, when tokens are enabled, bearer tokens. Protect your own routes
with `auth.Require()` as usual. `iam.Ping(ctx)` checks, read-only, that every
enabled table exists and names the migrations when one does not.

> [!WARNING]
> These routes change state with a session cookie. Install
> [CSRF protection](../browser-security/csrf-protection.md) on the router before `iam.Mount`,
> as in [Session Login](session-login.md); native clients that send no `Origin`
> header pass it, and bearer tokens need no cookie.

The facade owns both the session middleware and the guard and keeps them on one
lifetime: set it with `iam.SessionLifetime`, not on each. It uses the default
session handle, which the provider handlers require.

## Routes

`iam.Mount(r)` registers these routes. Only the routes of enabled features are
mounted, and each has an `iam.XHandler()` method when you need another path.
Mount at the root: the connect handler redirects to `/auth/{name}`, so a prefix
would break it. For a provider at another path, build the connect handler with
`iam.Links().ConnectHandler(iam.Guard(), targets)`.

| Area | Routes |
| --- | --- |
| Login | `POST /register`, `POST /login`, `POST /login/challenge`, `GET /login/pending`, `POST /logout` |
| Step-up | `POST /confirm` |
| Email | `POST /email/verify`, `POST /email/verify/resend` |
| Password | `POST /password/forgot`, `POST /password/reset`, `PUT /password` |
| Two-factor | `POST /two-factor/setup`, `POST /two-factor`, `DELETE /two-factor`, `POST /two-factor/recovery-codes` |
| Tokens | `GET /tokens`, `POST /tokens`, `DELETE /tokens/{id}`, `POST /device-tokens`, `DELETE /device-tokens/current` |
| Sessions | `GET /sessions`, `DELETE /sessions/{id}`, `DELETE /sessions` (all but this one) |
| Providers | `GET /auth/{name}`, `GET` or `POST /auth/{name}/callback`, `GET /auth/{name}/nonce`, `POST /auth/{name}/native` |
| Connections | `GET /connections`, `POST /connections/{provider}`, `DELETE /connections/{id}` |
| Account | `DELETE /account` |

Registration, login and password routes need `iam.Password`; the challenge and
pending routes need a second factor; email verification and account deletion
need the built-in users and, for email, `iam.Deliver`. Enabling a password
without `iam.Deliver` makes `Mount` panic, because the forgot route cannot send
its link. Building a handler for a feature you did not enable also panics, at
composition and not on the first request. `iam.MustAccept(auth.MethodOTP)` does
the same for a credential method you expect to be registered.

## Read Responses

Bodies and responses are JSON. A login that is complete answers `204`. When the
user must also answer a second factor it answers `202`:

```json
{ "second_factor": { "methods": ["otp", "recovery"] } }
```

The client then posts `{"method": "otp", "code": "123456"}` to
`/login/challenge`, and `GET /login/pending` lists the methods again. Methods
appear in registration order and the user picks any enrolled one.

Problems carry a stable `type` of the form
`tag:arkoren.dev,2026-11:ark/access:<package>:<name>`, so clients can switch on
it:

| Status | Meaning |
| --- | --- |
| 422 | A wrong secret or invalid input, such as a wrong password or a weak one. |
| 401 | No login, or no pending login. |
| 403 | Step-up needed: the problem lists the methods the user can confirm with. |
| 409 | A conflict: an email in use, two-factor already on, the last way to log in. |
| 429 | Throttled, with `Retry-After` in seconds. |

Core's own challenge and confirm routes answer a wrong code with `403`, not
`422`, because the attempt counter lives in core.

## Change Credentials Safely

Every handler that adds, changes or removes a credential requires a browser
session and a fresh login. A bearer token, even a valid one, is rejected with
`401`, so a stolen token can never enroll a second factor or change a password.

A login is **fresh** when the user proved a factor of one of their step-up
methods (`pwd` or `otp` by default) recently. A user who has no step-up method,
such as someone who only signs in with a provider, is fresh right after any login,
so a first enrollment needs nothing special. Otherwise the handler answers `403`
and the client confirms with `POST /confirm`, for example
`{"method": "pwd", "password": "..."}`, then retries. A provider login is not a
step-up method: it never makes a user who has a password or an authenticator
fresh again.

## Bring Your Own Users

Without configuration the facade keeps its own `users` table and identifies users
by email. To use your own, give it a provider and three functions:

```go
iam.Users(access.Users{
	Provider: provider, // *auth.UserProvider over your users
	Lookup:   lookup,   // identifier to subject and the stored address to mail
	Create:   create,   // optional: (ctx, identifier, verified) for registration and first provider login
	Rotate:   rotate,   // bump UserRecord.Version
	Verified: verified, // optional: gate connect, two-factor setup and tokens on a verified email
	ProveEmail: prove,  // optional: a reset proves the mailbox; purge what an earlier holder attached
})
```

`Create` must fail on an existing identifier and never return the existing user.
Without `Verified` and `ProveEmail` the facade cannot tell whether an email is
verified, so it neither gates those routes nor purges on reset: enforce the same
rules in your application.

No users table is migrated. A password reset calls `Rotate`, so make the user's
`Version` change: that ends every session, pending login and API token of the
user. Email verification and `DELETE /account` need the built-in users and are
absent; delete your users yourself and call `provider.Forget` to remove every
package's rows. Subjects you store in your own tables should be bytewise text
columns, because default database collations compare case-insensitively.

## Deliver and Observe

Messages that carry a secret, the verification and reset links, go to the
function you give `iam.Deliver`. It receives the kind, who to write to, the
token and its expiry; build the link with `message.Link(base)` and send it with
your mailer. Never log the token.

Everything else the application may want to tell a user (a changed password,
two-factor turned off, a new token) is an event on `iam.Events(bus)`. Events carry
subjects only, never tokens, codes or email addresses, so listeners cannot leak
them. Core's `auth.CredentialEnrolled` is published when two-factor is enabled.

## Storage

The packages never run DDL. `iam.Migrations()` returns only the enabled
features' tables, each created strictly, in groups named
`arkoren.dev:ark/access:<package>`. A named instance (`iam.Instance("tenant_a")`,
up to 20 lowercase letters, digits and underscores, starting with a letter)
prefixes every table and group, so two facades can share one database. Every
package assumes the one `*database.Database` you pass to `New`: writes that span
packages, such as registering a user and storing their password, or deleting an
account, join the transaction carried by the context.

## Next Steps

[Access Passwords](access-passwords.md), [Access Two-Factor](access-two-factor.md),
[Access Tokens](access-tokens.md) and [Access Connections](access-connections.md)
cover each feature. For every function and option, see
[the `access` API reference](https://pkg.go.dev/arkoren.dev/ark/access).
