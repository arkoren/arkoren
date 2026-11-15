---
title: Access Overview
nav: Overview
description: Compose registration, login, two-factor, tokens and provider sign-in into one facade, then install its middleware and mount its routes.
order: 10
summary:
  - Decide when the access facade fits better than the hand-built login of core authentication.
  - Install the module, enable the features you need and install its middleware and routes.
  - Run its migrations, check readiness, send reset and verification links and listen to its events.
---

# Access Overview

The [core authentication pages](../authentication/overview.md) show how to build
login from pieces: an authenticator, `auth.Guard` and your own routes. The
`access` module is the same pieces, composed. One facade builds the guard, the
stores for passwords, authenticators, tokens and connections, and the JSON routes
that use them, so you enable the features you want instead of writing each
handler.

Use it when you want registration, login, two-factor, API tokens and provider
sign-in working and tested. Keep the hand-built approach of
[Session Login](../authentication/session-login.md) when your login is unusual
enough that you would replace most of the handlers anyway. The module depends
only on core, Arkoren's foundation libraries and the standard library, and it
never imports the OAuth module: a provider joins through a small interface that
the [OAuth](../oauth/overview.md) clients satisfy.

## Installation

Run this from your application's module directory:

```sh
go get arkoren.dev/ark/access
```

## Composing

The application brings the database, the cache that holds sessions and counters,
a [hasher](../hashing/overview.md) for passwords and a
[cipher](../encryption/overview.md) for two-factor secrets. Each feature is off
until you enable it:

```go
iam, err := access.New(db, store, func(iam *access.Builder) {
	iam.Password(hasher)
	iam.TwoFactor(cipher, "Example")
	iam.Tokens()
	iam.Provider(google, github)
	iam.Deliver(send)
})
if err != nil {
	return err
}
```

`New` does no I/O. It returns an error wrapping `access.ErrInvalidConfig` when a
dependency is missing, a provider name is used twice or a package rejects its
settings.

| Feature | Builder method | Guide |
| --- | --- | --- |
| Registration, login, reset | `iam.Password(hasher)` | [Passwords](passwords.md) |
| Authenticator app and recovery codes | `iam.TwoFactor(cipher, issuer)` | [Two-Factor](two-factor.md) |
| Personal and device tokens | `iam.Tokens()` | [API Tokens](tokens.md) |
| Sign-in with external providers | `iam.Provider(clients...)` | [Connections](connections.md) |
| Reset and verification links | `iam.Deliver(send)` | [Delivering Messages](#delivering-messages) |

The facade also owns the built-in `users` table, which identifies users by
email. To keep your own users instead, see [Your Own Users](own-users.md).

## Protecting Routes

Install `iam.Middleware()` on every router that identifies users, then mount the
routes. The middleware runs the session middleware and authenticates browser
sessions and, when tokens are enabled, bearer tokens:

```go
web := routes.With(iam.Middleware())
iam.Mount(web)

api := routes.With(iam.Middleware(), auth.Require())
api.Get("/me", me)
```

Protect your own routes with `auth.Require()` as usual. The facade owns both the
session middleware and the guard and keeps them on one lifetime: set it with
`iam.SessionLifetime` (24 hours by default), not on each. It uses the default
session handle, which the OAuth provider handlers require. [Routes](routes.md)
lists what `Mount` registers.

> [!WARNING]
> The routes change state with a session cookie. Install
> [CSRF protection](../browser-security/csrf-protection.md) on the router before
> `iam.Mount`. Native clients that send no `Origin` header pass it, and bearer
> tokens need no cookie.

## Migrations and Readiness

The facade never runs DDL. `iam.Migrations()` returns the migrations of the
enabled features only, so add them to your migrator and run it:

```go
if err := migrator.Add(iam.Migrations()...); err != nil {
	return err
}
```

`iam.Ping(ctx)` checks, read-only, that every enabled table exists. When one is
missing it returns an error wrapping `access.ErrNotMigrated` that names the
table, so it works as a readiness check.

## Delivering Messages

A user who asks for a password reset or an email verification must receive a
secret link, and sending mail is your application's job. `iam.Deliver` takes the
function the facade calls with the kind of message, who to write to, the token
and its expiry:

```go
iam.Deliver(func(ctx context.Context, message access.Message) error {
	base := "https://app.example.com/verify-email"
	if message.Kind == access.KindReset {
		base = "https://app.example.com/reset-password"
	}

	return mailer.Send(ctx, message.Identifier, message.Link(base))
})
```

`message.Link(base)` adds the token as the `token` query parameter. The token is
a credential until it is used or expires, so never log it. `Identifier` is the
address stored with the user, never the text of a request.

Forgot-password and resend requests answer before they send. The facade delivers
in the background, so call `iam.Wait(ctx)` during shutdown, after the server stops
accepting requests, to let queued messages finish. Tests call it before asserting
on what was delivered.

If you enable passwords without `iam.Deliver`, `Mount` panics, because the forgot
route cannot send its link.

## Events

Pass your event bus with `iam.Events(bus)` and listen like any other
[event](../events/listeners.md). Events carry subjects only, never tokens, codes
or email addresses, so listeners cannot leak them. Use them for notifications
such as "your password changed" or "two-factor was turned off".

| Package | Events |
| --- | --- |
| `account` | `UserCreated`, `EmailVerified`, `UserDeleted` |
| `password` | `Changed`, `WasReset` |
| `twofactor` | `Enabled`, `Disabled`, `Regenerated`, `RecoveryUsed` |
| `apitoken` | `Issued`, `Revoked` |
| `federation` | `LinkCreated`, `LinkDeleted` |

Core's `auth.CredentialEnrolled` is published when two-factor is enabled, and
core's `auth.UserForgotten` when the built-in users are removed with their rows.

## Storage and Instances

Each enabled feature keeps its own tables and migration group, named
`arkoren.dev:ark/access:<package>`. Every package assumes the one
`*database.Database` you pass to `New`, so writes that span packages, such as
registering a user and storing their password, or deleting an account, join the
transaction carried by the context.

A named instance prefixes every table and group, so two facades can share one
database:

```go
iam.Instance("tenant_a")
```

The name is 1 to 20 bytes of lowercase letters, digits and underscores, and it
starts with a letter.

## Shared Settings

Four builder methods configure what the facade shares between features:

- `iam.SessionLifetime(d)` sets the absolute lifetime of a login for the session
  middleware and the guard together.
- `iam.ClientIP(find)` sets how the client address is found for login records and
  throttles. The default is the host of `r.RemoteAddr`, which behind a reverse
  proxy is the proxy itself, so pass `ratelimit.TrustedProxyKey(...)` there.
- `iam.Guard(configure)` customizes the guard, such as the step-up window with
  `FreshFor`. The facade applies its own settings after your callback, so the
  session lifetime, the session handle, the event bus and the client address you
  set with `iam.ClientIP` always win. Register credentials with `iam.SecondFactor`
  and `iam.StepUp`, not there: the registries only append.
- `iam.Session(configure)` customizes the session cookie, such as its name. The
  facade applies the maximum lifetime and the default handle after your callback,
  so they cannot be overridden.

`iam.SecondFactor`, `iam.StepUp` and `iam.Authenticator` add credentials and
authenticators from other modules, such as a passkey package, after the built-in
ones. `iam.MustAccept(auth.MethodOTP)` panics at composition unless a method you
expect is registered, so a typo fails at startup.

For code that logs users in itself, the facade exposes what it built:
`iam.Guard()`, `iam.UserProvider()`, `iam.Directory()`, `iam.Verifier()`,
`iam.Factors()`, `iam.Issuer()` and `iam.Links()`. Each returns nil when its
feature is off.

For every function and option, see
[the `access` API reference](https://pkg.go.dev/arkoren.dev/ark/access).
