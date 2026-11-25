---
title: IAM Overview
nav: Overview
description: Understand how Arkoren IAM works, wire it into your application, run its migrations, and protect your first route.
order: 10
summary:
  - Understand what IAM owns versus the domain data and authorization rules your application keeps.
  - Install IAM, build a password-enabled facade, and run its database migrations.
  - Provision a first user, mount the authentication routes with CSRF protection, and guard a private endpoint.
---

# IAM Overview

Most applications need the same identity plumbing: signing users in, managing sessions, recovering forgotten passwords, enrolling authenticators, and issuing API tokens. Arkoren IAM gives you ready-made handlers, storage, and security defaults for that entire layer behind a single **facade**, `*iam.IAM`.

IAM answers *who* is making a request and manages their credentials. Your application still owns its business data and decides *what* each user may access using [authorization rules](../authorization/overview.md). If you want to build a custom login protocol from scratch rather than mount ready-made routes, use the lower-level [authentication packages](../authentication/overview.md) directly.

## How IAM Fits Together

Every IAM setup shares three core pieces:

- **Users**: A built-in, email-based directory (`identity.Directory()`) that identifies each person by a stable string called a **subject**, or [your own users table](own-users.md) behind the same interface.
- **The guard**: The session and authentication engine (`identity.Guard()`) that verifies credentials, tracks active logins in your cache, and enforces step-up checks for sensitive actions.
- **Opt-in capabilities**: Passwords, two-factor authentication, external providers, roles and permissions, and API tokens. Everything starts off until you explicitly enable it in `iam.New`.

## Installation

Add the IAM module to your application:

```sh
go get arkoren.dev/ark/iam
```

IAM requires `arkoren.dev/core` v0.14.0 or later. It does not bundle a database driver, cache driver, hasher, or encrypter: you pass your existing `*database.Database`, `*cache.Cache`, and `*hash.Hasher` into `iam.New`. IAM never runs DDL on its own; you register its migrations with your application's migrator.

## Building the Facade

The examples throughout this guide assume you already have a configured [`*database.Database`](../database/overview.md) named `db`, a [`*cache.Cache`](../cache/overview.md) named `store`, and a [`*hash.Hasher`](../hashing/overview.md) named `hasher`. The database stores accounts and credentials; the cache stores sessions, login records, and rate-limit counters.

> [!NOTE]
> Use a shared cache such as Redis when more than one instance serves traffic. An in-memory cache is process-local, so sessions and throttle counters will not follow requests across instances.

Import `iam` from `arkoren.dev/ark/iam` and construct the facade with password login enabled:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher)
})
```

`iam.New` performs no network or database I/O. It wires the built-in directory, password verifier, and guard, and returns an error wrapping `iam.ErrInvalidConfig` if a required dependency is missing or a setting is invalid.

The snippets across these guides keep error results visible while omitting routine error propagation; handle every returned error in your application. Construction examples show the settings relevant to that topic; combine the options you need in your single `iam.New` call.

### Opting In to Capabilities

A capability is active only when you call its builder method: `Password`, `TwoFactor`, `Tokens`, `Permissions`, `Provider`, or `Account`. Because unused methods never run, you do not need to supply dependencies for features you leave off. To gate a capability behind a configuration flag, wrap the call in an `if`:

```go
if cfg.TwoFactor {
	identity.TwoFactor(scheme, cipher)
}
```

Keep each capability's settings in one call: calling `Password`, `TwoFactor`, `Tokens`, `Permissions`, or `Account` a second time replaces the earlier call rather than extending it. `Provider` is the exception: each call registers an additional external provider with a unique name.

Self-service sign-up (both password registration and first-login provider sign-up) stays closed by default, so you can deploy IAM for an invite-only application without exposing public registration. If you configure a setting whose prerequisite is off, such as password registration without password recovery, IAM ignores that setting and logs one warning through `slog.Default` naming the setting and why it was skipped.

### Behind a Reverse Proxy

By default, IAM identifies clients using the host part of `r.RemoteAddr` and ignores forwarding headers. Behind a load balancer or reverse proxy, `RemoteAddr` is the proxy's address, which makes all users share one rate-limit counter.

Configure `identity.ClientIP` with a resolver from [Client Addresses](../http-networking/client-addresses.md) that trusts only your proxy chain. Never trust arbitrary `X-Forwarded-For` headers, or callers can pick their own throttle key.

## Preparing Storage

Add IAM's migrations to your application's [migrator](../migrations/overview.md) before serving traffic:

```go
err = migrator.Add(identity.Migrations()...)
```

`identity.Migrations()` returns the migration groups for every IAM table (except the built-in users table when you bring [your own users](own-users.md)). Because all tables are created upfront, enabling two-factor or tokens later requires no migrator change. Groups are append-only: future releases only add migrations and never rewrite shipped ones.

Run your migrator as part of deployment:

```go
err = migrator.Up(ctx)
```

To verify at startup or in a readiness probe that every table required by your enabled features exists, call `identity.Ping(ctx)`. A missing table returns an error wrapping `iam.ErrNotMigrated` and names the table.

## Provisioning a First User

With self-service registration closed, your application creates users from an invitation or admin flow. Suppose Ada has already proved ownership of `ada@example.com` by accepting an invite:

```go
subject, err := identity.Directory().CreateVerified(ctx, "ada@example.com")
```

The returned `subject` is Ada's permanent identifier throughout IAM. `CreateVerified` trusts your verification; use `Create` instead when the address has not been proved yet, then send a [verification link](email-links.md#verifying-an-email). If the address already exists, `Create` and `CreateVerified` return `account.ErrEmailTaken` without merging accounts.

Next, set Ada's initial password:

```go
err = identity.Verifier().Set(ctx, subject, initialPassword)
```

If account creation and password storage must succeed together, wrap both calls in one [database transaction](../database/transactions.md) and pass its context to each call.

## Mounting Routes and Protecting Endpoints

IAM's middleware loads the session and authenticates incoming requests, but it does **not** enforce CSRF protection. Because browser sessions rely on cookies, always install `csrf.Middleware()` from `arkoren.dev/core/http/middleware/csrf` alongside `identity.Middleware()`:

```go
web := routes.With(csrf.Middleware(), identity.Middleware())
web.Group("/auth", identity.Register)
```

Here `routes` is your `*router.Router`. `identity.Register` mounts only the routes your configuration supports: with passwords enabled and registration off, `POST /auth/login` is mounted while `POST /auth/register` is not. All paths, including external providers, are relative to the group prefix (`/auth` here, or `"/"` for root paths).

Install `identity.Middleware()` once on the shared router that carries both IAM's routes and your own routes so every handler shares a single authentication step. To serve IAM endpoints at different paths, mount the [individual handlers](routes.md#custom-paths) instead of calling `Register`.

> [!NOTE]
> Every IAM JSON `POST`, `PUT`, or `PATCH` endpoint requires `Content-Type: application/json`, even when the request has no body (such as `POST /auth/logout`). Missing or non-JSON media types return `415`. This check complements CSRF protection; it does not replace it.

Mounting IAM's middleware identifies callers but does not block anonymous traffic on your own routes. Require an authenticated user with `auth.Require()` from `arkoren.dev/core/auth`:

```go
private := web.With(auth.Require())
private.Get("/posts", listPosts)
```

Anonymous requests to `/posts` receive `401`. Once Ada [signs in](passwords.md#signing-in), the request reaches `listPosts`, where your application still enforces which posts Ada may read.

### Local HTTP Development

Session cookies are `Secure` by default and require HTTPS in production. For local plain-HTTP development only, relax the cookie flag through `identity.Session` using `session` from `arkoren.dev/core/http/session`:

```go
identity.Session(func(cookies *session.Builder) {
	cookies.AllowInsecure(true)
})
```

> [!WARNING]
> Never enable `AllowInsecure(true)` in production. Plain HTTP exposes session cookies in transit.

IAM installs session middleware automatically inside `identity.Middleware()`; do not register a second copy of `session.Middleware()` on the same router.

### Sharing One Database Across Instances

If two separate IAM facades share one database, give each facade a distinct instance name during construction:

```go
identity.Instance("staff")
```

An instance name must be 1–20 bytes of lowercase letters, digits, and underscores, starting with a letter. It prefixes every table with `staff_` and appends `:staff` to each migration group (`arkoren.dev:ark/access:<package>`). Instance names separate table names; they do not by themselves isolate sessions or authorization.

Continue to [Passwords & Registration](passwords.md) to handle sign-in, password changes, and optional self-service sign-up.

For every builder method and handler, see the [`iam` API reference](https://pkg.go.dev/arkoren.dev/ark/iam).
