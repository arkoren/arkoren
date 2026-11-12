---
title: Two-Factor Login
description: Hold a login until a second factor passes, require recent or specific proof for sensitive routes, and end other sessions.
order: 25
summary:
  - Challenge a login after the first factor, verify the second, and complete it with both methods recorded.
  - Require specific methods or a recent confirmation before sensitive routes, and handle the 403 step-up problem.
  - End a user's other sessions when credentials change, and keep independent logins apart with realms.
---

# Two-Factor Login

A password alone may not be enough to sign someone in, or to let them delete an
account. The session records which **methods** the user proved (password,
one-time code, security key) and when. A **challenge** holds a login after the
first factor, and step-up middleware checks the record before sensitive routes.
Arkoren checks none of the factors itself: you verify the password, code or key
and tell the identity which methods succeeded.

The examples extend [Session Login](session-login.md): they use its shared
`identity`, `User` and `current` session, and assume a router `web` with session,
CSRF and authentication middleware installed. Import `auth` from
`arkoren.dev/core/http/auth`.

## Record Methods

Pass every method you verified to `Login`:

```go
err := identity.Login(current, account.User, auth.MethodPassword)
```

The session stores each method once, with the time it was last proved.
`auth.MethodPassword`, `MethodOTP`, `MethodHardwareKey`, `MethodSoftwareKey`,
`MethodFederated` and `MethodRecovery` are the provided values; the first four
use the RFC 8176 names. OAuth login records `MethodFederated` for you. A session
created without methods, or before methods existed, stays logged in but fails
every check below.

## Challenge Login

For an account with a second factor, call `Challenge` instead of `Login` once
the password checks out:

```go
if err := identity.Challenge(current, account.User, auth.MethodPassword); err != nil {
	return err
}

return response.Status(w, http.StatusAccepted)
```

`Challenge` removes any signed-in user from the session, stores the pending user
and the methods so far with a deadline, and rotates the session ID. Until the
challenge completes, the session is anonymous: `Authenticator` and `Middleware`
see no user, so `auth.Require` rejects it. The deadline defaults to five minutes
and is set with `ChallengeTTL`; it is not extended by session renewal.

```go
identity := auth.NewSessionIdentity(loadUser, func(binding *auth.SessionBuilder) {
	binding.ChallengeTTL(10 * time.Minute)
})
```

## Verify Second Factor

In the route that receives the code, `Pending` returns the waiting user so you
can look up their secret. After the code verifies, `Complete` signs them in with
the methods from the challenge plus the new one:

```go
user, err := identity.Pending(r.Context(), current)
if err != nil {
	return err // auth.ErrUnauthenticated: no challenge, or it expired
}
if !verifyCode(user, in.Code) {
	return auth.ErrUnauthenticated
}

return identity.Complete(r.Context(), current, auth.MethodOTP)
```

`verifyCode` is your check, for example with
[`foundation/totp`](https://pkg.go.dev/arkoren.dev/foundation/totp). Reject a
code whose time step you already accepted for that user, since a six-digit code
stays valid for its whole period. `Pending` does not consume the challenge, so
a wrong code can be retried; apply a [rate limit](../abuse-prevention/rate-limiting.md)
to this route, because a pending login is a login in progress. Use the same
`ErrUnauthenticated` for every failure so responses do not reveal which part
was wrong.

`Complete` is single use: it consumes the challenge, rotates the ID and stores
the factors, so a second call returns `ErrUnauthenticated`. If the user was
deleted meanwhile, the loader's error is returned and nobody is logged in.
`Login` also discards a pending challenge.

## Require Methods

`RequireMethods` admits a request only if the session proved every listed method:

```go
web.With(identity.RequireMethods(auth.MethodOTP)).Post("/payouts", payout)
```

Install it after `auth.Middleware`. Everything it cannot establish fails closed
with the step-up problem described below: a request authenticated by a bearer
token (it has no session factors), a session with no recorded methods, or a
session whose user differs from the request's user. A request with no user at
all returns `auth.ErrUnauthenticated`.

## Require Recent Proof

`RequireFresh` admits a request only if some factor was proved within the given
age, measured from the most recent one:

```go
web.With(identity.RequireFresh(10 * time.Minute)).Delete("/account", destroy)
```

When it rejects, ask the user to re-enter a factor, verify it, and call
`Confirm`, which refreshes that method's time (or adds it) and rotates the ID:

```go
if err := identity.Confirm(current, auth.MethodPassword); err != nil {
	return err
}
```

`Confirm` verifies nothing and requires a signed-in user of the identity. Use it
also to add a factor to a single-factor session so `RequireMethods` then passes.
A zero or negative age admits nothing.

## Handle Step-Up

Both middleware return `auth.ErrStepUpRequired`, a `403` problem of type
`tag:arkoren.dev,2026-11:core:http/auth:step-up-required`. It is `403`, not
`401`, because the user is signed in and a `401` invites login redirects that
would discard the session. Have your client match the type, prompt for the
factor and retry. Read the session's record directly with `identity.State`,
which returns a copy of the methods and times.

## End Other Sessions

Implement `Versioned` on the user type to end all of a user's other sessions
when their credentials change:

```go
func (user User) AuthVersion() string {
	return strconv.FormatInt(user.PasswordChangedAt.UnixNano(), 10)
}
```

`Login` stores a hash of the value, and the authenticator compares it with the
freshly loaded user on every request. A mismatch, or a session with no stored
version, is anonymous. After a password change, update the stored value and call
`Login` again on the current session with the reloaded user and its verified
methods, so this device keeps a session while the others end. `Confirm` keeps
the stored version, so it cannot do this. Turning this on for an existing
application ends sessions created before it. There is no list of
a user's sessions to revoke individually.

## Separate Realms

Two independent logins, such as an admin area next to a customer area, can share
one cookie by using different realms and different user types:

```go
admins := auth.NewSessionIdentity(loadStaff, func(binding *auth.SessionBuilder) {
	binding.Realm("admin")
})
```

The realm suffixes the session keys (`arkoren:auth:user:admin`), so each realm
has its own user, factors and challenge, and logging out of one leaves the other.
The default empty realm keeps the original keys. Changing the realm of a deployed
identity logs its sessions out.

For all functions and options, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth).
