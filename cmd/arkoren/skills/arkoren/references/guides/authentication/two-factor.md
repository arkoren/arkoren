---
title: Two-Factor Login
description: Hold a login until a second factor passes, require a level or recent proof for sensitive routes, and handle the step-up problem.
order: 25
summary:
  - Park a login after the first factor, verify the second, and complete it with both factors recorded.
  - Require a multi-factor level or a recent confirmation before sensitive routes.
  - Handle the 403 step-up problem and end other sessions when credentials change.
---

# Two-Factor Login

A password alone may not be enough to sign someone in, or to let them delete an
account. The session records which **factors** the user proved (password,
one-time code, security key) and when. A login that still needs a factor is held
**pending**, and step-up middleware checks the recorded factors before sensitive
routes. Arkoren checks none of the factors itself: you verify the password, code
or key, then tell `auth.Login` which factor succeeded.

The examples extend [Session Login](session-login.md): they use its shared
`login` (an `*auth.Login`), `Account` and router `web`, with session, CSRF and
authentication middleware installed. Import `auth` from `arkoren.dev/core/auth`.

## Record Factors

Every login method ends in `login.Begin` with the subject and the factor it
verified:

```go
factor := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}
complete, err := login.Begin(r, subject, factor)
```

A factor has a `Method` (how it was verified), a `Kind` (what it proves) and a
time, which `Begin` sets to now when you leave it zero. The provided methods are
`MethodPassword`, `MethodOTP`, `MethodHardwareKey`, `MethodSoftwareKey`,
`MethodRecovery` and `MethodFederated`; `pwd`, `otp`, `hwk` and `swk` are the RFC
8176 names, while `recovery` and `federated` are Arkoren's own. Kinds are
`KindKnowledge`, `KindPossession`, `KindInherence` and `KindFederated`.

## Choose Levels

An identity's level comes from its factors, never from a setting:

| Factors | Level |
| --- | --- |
| none | `auth.LevelNone` |
| one factor, or several of the same kind | `auth.LevelSingle` |
| two different kinds among knowledge, possession and inherence | `auth.LevelMulti` |

Two passwords stay `LevelSingle`. A federated login (OAuth, SAML) is
`LevelSingle` and never counts toward `LevelMulti`, even combined with a
password, because Arkoren cannot see which factors the provider required.

## Hold a Login

Tell the login which users need a second factor by registering checks. A check
is a plain function, so core never imports the package that knows the answer;
pass an enrollment lookup such as an ark's `Enrolled`:

```go
login, err := auth.NewLogin(provider, store, func(policy *auth.LoginBuilder) {
	policy.SecondFactor(enrolled) // func(ctx context.Context, subject string) (bool, error)
})
```

You may register several checks; one yes from any is enough, and the first error
fails the login. `Begin` then logs the user in and returns `true` when no check
says yes. Otherwise it parks a pending login, rotates the session ID and returns
`false`. Until the login completes, the request is anonymous: the authenticator
and `auth.Middleware` see no user, so `auth.Require` rejects it.

```go
if !complete {
	return response.Status(w, http.StatusAccepted)
}
```

A pending login expires after five minutes by default; set it with
`policy.ChallengeTTL(10 * time.Minute)`. Session renewal does not extend it.

## Verify the Second Factor

In the route that receives the code, `Pending` returns the waiting user so you
can look up their secret. After the code verifies, `Complete` signs them in with
the first factor plus the new one:

```go
user, err := login.Pending(r)
if err != nil {
	return err // auth.ErrUnauthenticated: no pending login, or it expired
}
if !verifyCode(user.Subject, in.Code) {
	if err := login.Fail(r); err != nil {
		return err
	}

	return auth.ErrUnauthenticated
}

otp := auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession}

return login.Complete(r, otp)
```

`verifyCode` is your check, for example with
[`foundation/totp`](https://pkg.go.dev/arkoren.dev/foundation/totp). Reject a
code whose time step you already accepted for that user, since a six-digit code
stays valid for its whole period.

`Fail` counts a wrong attempt; at five (`policy.MaxAttempts(n)`) the pending
login is discarded and the user must start again. The count lives in the browser
session, so also [rate limit](../abuse-prevention/rate-limiting.md) this route by
user or address. Use the same `auth.ErrUnauthenticated` for every failure so the
response does not reveal which part was wrong.

`Complete` is single use: it consumes the pending login and rotates the ID, so a
second call returns `ErrUnauthenticated`. A user deleted or disabled meanwhile
also returns it and discards the pending login, and so does a changed
`UserRecord.Version`: if the password changes while the user is on the second
step, they must start the login again. `Pending` applies the same check. Every `Begin`, `Complete`,
`Fail`, `Confirm` and `Logout` regenerates the session ID.

## Require a Level

`RequireLevel` admits a request only when its identity reaches the level:

```go
web.With(auth.RequireLevel(auth.LevelMulti)).Post("/payouts", payout)
```

Install it after `auth.Middleware`. It reads the identity from the request
context, so it also works for tokens that carry factors. A request without an
identity is rejected like `auth.Require` does; a weaker identity gets the step-up
problem below.

## Require Recent Proof

`RequireFresh` admits a request only when some factor was verified within the
given age:

```go
web.With(auth.RequireFresh(10 * time.Minute)).Delete("/account", destroy)
```

A factor with an unknown or future time is never fresh, and an age of zero or
less admits nothing. When it rejects, ask the user to re-enter a factor, verify
it, and call `Confirm`, which records it with the current time and rotates the
ID:

```go
password := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}
if err := login.Confirm(r, password); err != nil {
	return err
}
```

A factor with the same method replaces the old one, so each method appears once
with its latest time. `Confirm` verifies nothing and returns
`auth.ErrUnauthenticated` without a signed-in user. Use it also to add a second
factor to a single-factor session so `RequireLevel` then passes.

## Handle Step-Up

Both middleware return `auth.ErrStepUpRequired`, a `403` problem of type
`tag:arkoren.dev,2026-11:core:auth:step-up-required`. It is `403`, not `401`,
because the user is signed in and a `401` invites login redirects that would
discard the session. Have your client match the type, prompt for the factor and
retry.

## End Other Sessions

Set `UserRecord.Version` in your user provider to a value that changes with the
user's credentials, such as the time the password last changed. `Begin` stores a
hash of it and the authenticator compares it in constant time with the provider's
current value on every request. A mismatch makes the session anonymous,
including the device that made the change; that device logs in again, passing
the second factor again if the user is enrolled. There is no list of a user's
sessions to revoke individually.

For all functions and options, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/auth).
