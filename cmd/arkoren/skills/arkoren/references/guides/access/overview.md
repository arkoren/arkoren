---
title: Access Overview
nav: Overview
description: Build a minimal login system with Access, prepare its storage, and mount its routes with session and CSRF protection.
order: 10
summary:
  - Separate the login features Access supplies from the resources and policies your application owns.
  - Construct a password-enabled facade, migrate its tables, and provision a known user.
  - Mount the authentication routes and protect application routes without opening registration.
---

# Access Overview

If your application needs ordinary login and account settings, Access supplies
the handlers and storage behind them. You enable passwords, authenticator apps,
API tokens, or external sign-in; the **facade**, an `*access.Access`, connects
those features to one user directory and one login system.

Access identifies users; it does not decide which posts, projects, or other
resources they may use. Keep those decisions in your application's
[authorization rules](../authorization/overview.md). If you need a different
login flow rather than ready-made routes, use the lower-level
[authentication packages](../authentication/overview.md) instead.

> [!NOTE]
> Access is not released yet. Installation is pending; these guides describe the
> current API without claiming that `arkoren.dev/ark/access` is available to download.

## Start Small

Assume your application already has a
[`*database.Database`](../database/overview.md) named `db`, a
[`*cache.Cache`](../cache/overview.md) named `store`, and a password
[`*hash.Hasher`](../hashing/overview.md) named `hasher`.
The database stores accounts and enabled credentials. The cache stores sessions,
login records, and throttling counters. Use a shared cache when requests can
reach different application instances; an in-memory cache is process-local.

Import `access` from `arkoren.dev/ark/access`. The examples are focused
fragments: handle each returned error before continuing in your application.
Construction examples are alternatives, not additional facades to create.
Combine the settings you need in your own `access.New` callback.

Access also needs a trustworthy client-address function for its abuse limits.
For an application reached directly, without a reverse proxy, use `ratelimit`
from `arkoren.dev/core/http/middleware/ratelimit`:

```go
clientIP := ratelimit.TrustedProxyKey()
```

If requests arrive through a proxy, instead pass only that proxy's actual
network prefixes, using `net/netip`:

```go
clientIP := ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.20.0.0/24"))
```

Do not copy that example prefix without checking your deployment. Trusting
arbitrary forwarding headers lets an attacker choose their throttle key;
ignoring a real proxy makes its users share a counter. Both forms group IPv6
clients by `/64`.

Now enable password login, leaving self-service registration closed:

```go
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Password(hasher)
})
```

This builds the built-in email-based directory, password store, and **guard**:
the component that starts logins, checks sessions, and tracks authentication
proofs. It does not connect resources, create tables, or start workers.
Missing required dependencies or incompatible settings return an error wrapping
`access.ErrInvalidConfig`.

Other login features stay off. Password registration and first-login provider
sign-up also stay off unless you explicitly enable them. You can therefore add
Access to an invite-only application without exposing a public sign-up route.

## Prepare Storage

Before serving requests, add Access's migrations to your application's
[migrator](../migrations/overview.md):

```go
err = migrator.Add(iam.Migrations()...)
```

The returned migrations include the built-in directory and only the enabled
credential stores. Adding them does not execute them. Your deployment then runs:

```go
err = migrator.Up(ctx)
```

After migration, use `iam.Ping(ctx)` as a read-only readiness check. A missing
table returns an error wrapping `access.ErrNotMigrated` and names the table.
Your application still owns the database and cache lifecycles.

## Add a User

With registration closed, provision users from your application's invitation or
administration flow. Suppose Ada has already proved ownership of
`ada@example.com`, for example by accepting your invitation:

```go
subject, err := iam.Directory().CreateVerified(ctx, "ada@example.com")
```

The returned **subject** is the stable string that identifies Ada throughout
Access. `CreateVerified` trusts your proof; it does not verify the address itself.
Use `Create` instead when ownership is not proved, then arrange
[email verification](email-links.md#verify-an-email).
An address already in use returns `account.ErrEmailTaken`; no account is merged.

For password login, set Ada's initial password through a secure provisioning
flow. With `initialPassword` supplied by that flow:

```go
err = iam.Verifier().Set(ctx, subject, initialPassword)
```

If account creation and password storage must succeed together, execute both
inside one [database transaction](../database/transactions.md), passing its
context to both calls. These direct Go methods are privileged application
operations; they are not substitutes for the guarded public account routes.

## Mount Routes

Access's middleware loads sessions and identifies the caller. It does **not**
provide CSRF protection. Because the account routes accept session cookies,
install `csrf.Middleware()` from `arkoren.dev/core/http/middleware/csrf` before
exposing them:

```go
web := routes.With(csrf.Middleware(), iam.Middleware())
iam.Mount(web)
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
It must still authorize which posts Ada may read.

Every Access JSON `POST`, `PUT`, or `PATCH` needs
`Content-Type: application/json`, even when no body is needed. A missing or
different media type returns `415`. Provider protocol routes are exempt.
This content-type check is not a replacement for CSRF protection.

### Local HTTP

Use HTTPS in production; session cookies are secure by default.
For local plain HTTP only, configure the exception through Access's `Session`
callback, using `session` from `arkoren.dev/core/http/session`:

```go
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Password(hasher)
	iam.Session(func(cookies *session.Builder) {
		cookies.AllowInsecure(true)
	})
})
```

Do not enable this in production: HTTP exposes the session credential in transit.
Access already installs session middleware; do not add another copy.
The [session guide](../http-capabilities/sessions.md) explains cookie defaults
and storage behavior.

## Configure Features

The following pages add one capability at a time. Keep each feature's settings
together: a later `Password`, `TwoFactor`, or `Tokens` call replaces that
feature's earlier configuration rather than extending it. Add each provider
through its own `Provider` call; provider names must be unique.

All feature stores use the same database and transaction context. If you need
separate table sets in that database, choose an instance during construction:

```go
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Instance("staff")
	iam.Password(hasher)
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
[`access` API reference](https://pkg.go.dev/arkoren.dev/ark/access).
