---
title: IAM Overview
nav: Overview
description: Build a minimal login system with IAM, prepare its storage, and mount its routes with session and CSRF protection.
order: 10
summary:
  - Find every IAM feature and its guide, and separate them from the resources and policies your application owns.
  - Construct a password-enabled facade, migrate its tables, and provision a known user.
  - Mount the authentication routes and protect application routes without opening registration.
---

# IAM Overview

If your application needs ordinary login and account settings, IAM supplies
the handlers and storage behind them. You enable passwords, authenticator apps,
API tokens, or external sign-in; the **facade**, an `*iam.IAM`, connects
those features to one user directory and one login system.

IAM identifies users; it does not decide which posts, projects, or other
resources they may use. Keep those decisions in your application's
[authorization rules](../authorization/overview.md). If you need a different
login flow rather than ready-made routes, use the lower-level
[authentication packages](../authentication/overview.md) instead.

> [!NOTE]
> IAM is not released yet. Installation is pending; these guides describe the
> current API without claiming that `arkoren.dev/ark/iam` is available to download.

## Features

Every feature is off until you enable it, except the user directory and
session management, which come with any facade:

- **[Accounts](#add-a-user)**: a built-in, email-based user directory, or
  [your own users](own-users.md) behind the same API.
- **[Passwords](passwords.md)**: login, confirmation and password changes,
  with abuse limits; [registration](registration.md) opens self-service
  sign-up when you choose.
- **[Email links](email-links.md)**: password reset and email verification
  through signed, single-use links your queue delivers.
- **[Two-factor](two-factor.md)**: authenticator-app codes and recovery codes
  that finish a pending login.
- **[Fresh logins](freshness.md)**: recent proof of identity before sensitive
  account changes.
- **[Sessions](accounts.md#manage-sessions)**: list a user's logins and log out
  other devices.
- **[API and device tokens](tokens.md)**: scoped personal tokens and
  revocable tokens for first-party apps, capped by what the user may hold.
- **[Federated sign-in](federated-sign-in.md)**: external providers, per-provider
  sign-up and user-managed connections; [SSO](sso.md) applies them to
  organizational sign-in.
- **[Permissions and roles](permissions.md)**: assign core `gate` roles and
  permissions to users, check them on sessions and tokens, and manage them
  through optional routes.
- **[Account deletion](accounts.md#delete-an-account)**: remove a user and every
  feature's rows together.

The [route reference](routes.md) lists the HTTP routes each feature mounts.

## Start Small

Assume your application already has a
[`*database.Database`](../database/overview.md) named `db`, a
[`*cache.Cache`](../cache/overview.md) named `store`, and a password
[`*hash.Hasher`](../hashing/overview.md) named `hasher`.
The database stores accounts and enabled credentials. The cache stores sessions,
login records, and throttling counters. Use a shared cache when requests can
reach different application instances; an in-memory cache is process-local.

Import `iam` from `arkoren.dev/ark/iam`. The examples are focused
fragments: handle each returned error before continuing in your application.
Construction examples are alternatives, not additional facades to create.
Combine the settings you need in your own `iam.New` callback.

By default IAM uses the host in `RemoteAddr` for abuse limits and ignores
forwarded headers. If a reverse proxy sits in front of IAM, configure
`identity.ClientIP` with a function that trusts only your actual proxy chain.
See [Client Addresses](../http-networking/client-addresses.md) before choosing
one: trusting arbitrary headers lets clients choose their own throttle key.

Now enable password login, leaving self-service registration closed:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher)
})
```

This builds the built-in email-based directory, password store, and **guard**:
the component that starts logins, checks sessions, and tracks authentication
proofs. It does not connect resources, create tables, or start workers.
Missing required dependencies or incompatible settings return an error wrapping
`iam.ErrInvalidConfig`.

Other login features stay off. Password registration and first-login provider
sign-up also stay off unless you explicitly enable them. You can therefore add
IAM to an invite-only application without exposing a public sign-up route.

## Prepare Storage

Before serving requests, add IAM's migrations to your application's
[migrator](../migrations/overview.md):

```go
err = migrator.Add(identity.Migrations()...)
```

The returned migrations include the built-in directory and only the enabled
credential stores. Adding them does not execute them. Your deployment then runs:

```go
err = migrator.Up(ctx)
```

After migration, use `identity.Ping(ctx)` as a read-only readiness check. A missing
table returns an error wrapping `iam.ErrNotMigrated` and names the table.
Your application still owns the database and cache lifecycles.

## Add a User

With registration closed, provision users from your application's invitation or
administration flow. Suppose Ada has already proved ownership of
`ada@example.com`, for example by accepting your invitation:

```go
subject, err := identity.Directory().CreateVerified(ctx, "ada@example.com")
```

The returned **subject** is the stable string that identifies Ada throughout
IAM. `CreateVerified` trusts your proof; it does not verify the address itself.
Use `Create` instead when ownership is not proved, then arrange
[email verification](email-links.md#verify-an-email).
An address already in use returns `account.ErrEmailTaken`; no account is merged.

For password login, set Ada's initial password through a secure provisioning
flow. With `initialPassword` supplied by that flow:

```go
err = identity.Verifier().Set(ctx, subject, initialPassword)
```

If account creation and password storage must succeed together, execute both
inside one [database transaction](../database/transactions.md), passing its
context to both calls. These direct Go methods are privileged application
operations; they are not substitutes for the guarded public account routes.

## Mount Routes

IAM's middleware loads sessions and identifies the caller. It does **not**
provide CSRF protection. Because the account routes accept session cookies,
install `csrf.Middleware()` from `arkoren.dev/core/http/middleware/csrf` before
exposing them:

```go
web := routes.With(csrf.Middleware(), identity.Middleware())
identity.Mount(web)
```

Here `routes` is your existing `*router.Router`. Mount once, at the root:
provider redirects use `/auth/{name}`. `Mount` registers only the routes your
configuration supports; for this facade, `/login` exists but `/register` does not.
The [route reference](routes.md) covers the full list and custom paths.

Your own routes are not automatically private. After the middleware, require an
authenticated caller with `auth` from `arkoren.dev/core/auth`:

```go
private := web.With(auth.Require())
private.Get("/posts", listPosts)
```

`listPosts` is your application handler. An anonymous request receives `401`;
after Ada [logs in](passwords.md#log-in), the request reaches the handler.
It must still authorize which posts Ada may read, for example with
[permissions](permissions.md) and a scoped query.

Every IAM JSON `POST`, `PUT`, or `PATCH` needs
`Content-Type: application/json`, even when no body is needed. A missing or
different media type returns `415`. Provider protocol routes are exempt.
This content-type check is not a replacement for CSRF protection.

### Local HTTP

Use HTTPS in production; session cookies are secure by default.
For local plain HTTP only, configure the exception through IAM's `Session`
callback, using `session` from `arkoren.dev/core/http/session`:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher)
	identity.Session(func(cookies *session.Builder) {
		cookies.AllowInsecure(true)
	})
})
```

Do not enable this in production: HTTP exposes the session credential in transit.
IAM already installs session middleware; do not add another copy.
The [session guide](../http-capabilities/sessions.md) explains cookie defaults
and storage behavior.

## Configure Features

The following pages add one capability at a time. Keep each feature's settings
together: a later `Password`, `TwoFactor`, `Tokens`, or `Permissions` call replaces that
feature's earlier configuration rather than extending it. Add each provider
through its own `Provider` call; provider names must be unique.

All feature stores use the same database and transaction context. If you need
separate table sets in that database, choose an instance during construction:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Instance("staff")
	identity.Password(hasher)
})
```

`staff` prefixes the tables and adds `:staff` to each migration group, whose
base name is `arkoren.dev:ark/access:<package>`. A nonempty instance name is
1–20 bytes of lowercase letters, digits, and underscores, starting with a letter.
This separates table names; it is not an authorization or complete session
isolation boundary.

Continue with [Passwords](passwords.md) to make the first login and understand
its response.

For the public entry points, see the
[`iam` API reference](https://pkg.go.dev/arkoren.dev/ark/iam).
