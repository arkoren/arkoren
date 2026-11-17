---
title: Two-Factor Login
description: Hold a login until a second factor passes, require a level or recent proof for sensitive routes, and handle the step-up problem.
order: 25
summary:
  - Register credentials, park a login after the first factor and finish it through the core challenge route.
  - Require a multi-factor level or a recent confirmation, and confirm through the core step-up route.
  - Handle the 403 step-up problem and end other sessions when credentials change.
---

# Two-Factor Login

> [!NOTE]
> The [IAM](../iam/overview.md) ark ships this flow, with TOTP, recovery codes and
> their routes: see [IAM Two-Factor](../iam/two-factor.md). This page explains
> the guard seams it is built on, which you use directly for other factors.

A password alone may not be enough to sign someone in, or to let them delete an
account. The session records which **factors** the user proved (password,
one-time code, security key) and when. A login that still needs a factor is held
**pending**, and step-up middleware checks the recorded factors before sensitive
routes. Arkoren checks none of the factors itself: you verify the password, code
or key, then tell `auth.Guard` which factor succeeded.

The examples extend [Session Login](session-login.md): they use its shared
`guard` (an `*auth.Guard`), `Account` and router `web`, with session, CSRF and
authentication middleware installed. Import `auth` from `arkoren.dev/core/auth`.

## Record Factors

Every login method ends in `guard.Begin` with the subject and every factor it
verified, then answers with the outcome:

```go
factor := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}
outcome, err := guard.Begin(r, subject, factor)
if err != nil {
	return err
}

return outcome.Response(w) // 204, or 202 with the methods to choose from
```

A factor has a `Method` (how it was verified), a `Kind` (what it proves) and a
time. The time is the server's clock: the guard stamps it when it records the
factor. A login method may report an earlier time, which the guard keeps, but a
later or missing time becomes now, so a provider can make a login look older but
never fresher. Core names only the RFC 8176 methods `MethodPassword` (`pwd`),
`MethodOTP` (`otp`), `MethodHardwareKey` (`hwk`) and `MethodSoftwareKey` (`swk`).
A module that verifies something else, such as a recovery code or a federated
login, declares its own `auth.Method`. Kinds are `KindKnowledge`,
`KindPossession`, `KindInherence` and `KindFederated`. A new factor replaces an
older one with the same method and kind, so a passkey can prove possession and
inherence at once.

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

## Register Credentials

Tell the guard how a user can prove a second factor by registering an
`auth.Credential` for each method. A module that owns a method (a TOTP
authenticator, a passkey) exports its credential; core receives plain functions,
so it never imports that module:

```go
guard, err := auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
	policy.SecondFactor(authenticator.Credential()) // may finish a parked login
	policy.StepUp(password.Credential(), authenticator.Credential()) // may re-prove a signed-in user
})
```

A credential has a `Method`, a `Kind`, an `Enrolled` function that says whether a
user has it and a `Verify` function that checks the request input. Both registries
keep registration order, and `NewGuard` rejects a duplicate method within one, an
empty method and a nil `Enrolled`. Register the same credential in both when it
serves both purposes. A credential without `Verify` is a redirect method: it is
listed, but completed through its own route.

## Hold a Login

`Begin` asks every second-factor credential whether the user is enrolled. When
the factors it received already reach `LevelMulti`, or no credential says yes, it
logs the user in. Otherwise it parks a pending login, rotates the session ID and
returns an outcome that is not complete: `outcome.Response(w)` answers `202` with
the methods to choose from, in registration order:

```json
{"second_factor": {"methods": ["otp", "hwk"]}}
```

Until the login completes, the request is anonymous: the authenticator and
`auth.Middleware` see no user, so `auth.Require` rejects it. A failing `Enrolled`
fails the login with its error.

The second-factor rules do not run when the session is already logged in as that
subject: `Begin` then adds the factors to the existing login and keeps its login
record. A logged-in user who re-authenticates through a provider keeps their
factors and is not challenged again.

A pending login expires after five minutes by default; set it with
`policy.ChallengeTTL(10 * time.Minute)`. Session renewal does not extend it.

## Verify the Second Factor

Core owns the two routes that finish a pending login, so every module answers
alike:

```go
web.Get("/login/second-factor", guard.PendingHandler())
web.Post("/login/second-factor", guard.ChallengeHandler())
```

`PendingHandler` answers `200` with `{"methods": ["otp"]}`, the methods this user
can use. It spends nothing, so a page can call it freely; `guard.Pending(r)` is
the same read for Go code. The client then posts the chosen method and its input
to `ChallengeHandler`:

```json
{"method": "otp", "code": "123456"}
```

The handler finds the registered credential, passes the rest of the body to its
`Verify` and, when it succeeds, logs the user in with the first factor plus the
verified one and answers `204`. It never trusts the client's method name: an
unregistered method, a missing one and a redirect method answer
`auth.ErrUnknownMethod` (`422`) without costing an attempt, and a method the user
is not enrolled in answers `auth.ErrInvalidCredential` (`403`), exactly like a
wrong code, so a client cannot probe which methods a user has.

The handler reserves one attempt per user in the guard's cache store before it
verifies anything, so a new login, a new session or parallel requests on one
pending cookie do not reset it, and racing requests cannot check more codes than
the limit. One count covers every method. At most five attempts are allowed
within the challenge window (`policy.MaxAttempts(n)`, window
`policy.ChallengeTTL(d)`); after that the pending login is discarded and that
user cannot pass the second factor, in any session, for one window. Lockouts
escalate: every wrong second-factor code also counts against a 24 hour budget per user
(a wrong step-up code counts only when it uses up a window's attempts, so
occasional typos at `ConfirmHandler` never add up), and
each time the budget grows by `MaxAttempts` (or a window's attempts run out) a
lockout starts that is twice as long as the last (window x 2^n, capped at 24
hours, or at the window when that is longer). Staying just under the limit in
every window therefore does not avoid it. The budget expires 24 hours after its
first failure, not after the last; a successful challenge or `guard.Unlock` clears it. A wrong
code (`Verify` returns an error wrapping `auth.ErrInvalidCredential`) keeps its
attempt; any other `Verify` error is an infrastructure failure, so the attempt
is refunded and the error passes through. A successful challenge clears the
count. The lockout lets anyone who knows a subject delay that user, so also
[rate limit](../abuse-prevention/rate-limiting.md) the routes by address.

Each lockout publishes `auth.SecondFactorLocked` with the subject and the end of
the lockout, once, when the guard has a bus (`policy.Events(bus)`). Subscribe to
it to notify the user or your operators. The login route's throttle must also
count a lockout as a failed login, otherwise an attacker who knows the first
factor can keep starting pending logins after every lockout at no cost.

Events are notifications, not control flow: a listener error is logged and the
remaining listeners are skipped, so a throttle fed by the event would fail open.
When a decision depends on a lockout, ask the guard instead.
`guard.LockedOut(ctx, subject)` reports whether the subject is locked out right
now. It only reads the lockout, so it reserves no attempt and is safe on every
request. A cache failure comes back as an error; fail closed on it. After a
correct password, a login route keeps the attempt counted while the user is
locked out:

```go
locked, err := guard.LockedOut(r.Context(), subject)
if err != nil {
	return err
}
if locked {
	// This password only parks a pending login: do not refund the attempt.
}
```

Lockouts chain, up to 24 hours each, and they also block step-up
(`ConfirmHandler`). Anyone who holds the password can start pending logins and
spend the attempts, so a lockout means the password is compromised: reset it and
call `guard.Unlock(ctx, subject)`, which clears the lockout, the budget and the
current window's attempts. Call it from a password reset or an admin action.

The challenge is single use: it consumes the pending login and rotates the ID, so
a second call returns `ErrUnauthenticated`. A user deleted or disabled meanwhile
also returns it and discards the pending login, and so does a changed
`UserRecord.Version`: if the password changes while the user is on the second
step, they must start the login again. Every `Begin`, challenge, confirmation
and `Logout` regenerates the session ID.

A module that cannot work unless the guard accepts its method calls
`guard.MustAccept(method)` while composing the application. It panics at startup
when the method is in neither registry.

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

`guard.RequireFresh` admits a request only when the user proved themselves
recently enough:

```go
web.With(guard.RequireFresh(10 * time.Minute)).Delete("/account", destroy)
```

Inside a handler, `guard.Fresh(r)` makes the same check with the window of
`policy.FreshFor(d)`, 15 minutes by default. The rule depends on how the user can
step up. If they are enrolled in a registered step-up method, fresh means a factor
of one of those methods was verified within the window; a silent re-login through
a provider adds a factor that is not a step-up method, so it never refreshes
them. If they are enrolled in none, any factor within the window counts, so a user
who only signs in through a provider is fresh right after signing in. A factor
with an unknown or future time is never fresh, and a window of zero or less admits
nothing. A failing `Enrolled` returns its error instead of relaxing the rule.

When the user is not fresh, ask them to prove themselves again and post the
method and input to the confirm route:

```go
web.With(auth.RequireSession()).Post("/confirm", guard.ConfirmHandler())
```

`ConfirmHandler` works like the challenge against the step-up credentials, with
the same body and attempt count, and requires a signed-in browser session. On
success it records the factor with the current time, rotates the ID and answers
`204`. A user who used up their attempts gets `auth.ErrInvalidCredential`, like a
wrong code. A factor with the same method and kind replaces the old one, so each
pair appears once with its latest time, and confirming a second kind lifts a
single-factor session so `RequireLevel` then passes.

`auth.RequireSession` rejects identities that did not come from
`guard.Authenticator()`. Mount every route that enrolls, changes or confirms a
credential behind it: `identity.Session()` is true only for a browser session, so
a bearer token can never change credentials.

## Handle Step-Up

`RequireLevel` and the freshness check return `auth.ErrStepUpRequired`, a `403`
problem of type `tag:arkoren.dev,2026-11:core:auth:step-up-required`. The freshness
check adds a `methods` member listing the user's enrolled step-up methods in
registration order, which the client offers and posts to the confirm route. When
the user has no step-up method and is not fresh, it returns
`auth.ErrNoStepUpMethod` (`403`, type
`tag:arkoren.dev,2026-11:core:auth:no-step-up-method`): the only way forward is
to log in again. Both are `403`, not `401`, because the user is signed in and a
`401` invites login redirects that would discard the session. Have your client
match the type and retry.

To require a new login rather than freshness, for example before exchanging a
session for a long-lived device token, call `guard.RecentLogin(r, 5*time.Minute)`.
It checks when the session's login was created: a silent re-login by the same
user keeps that login, so it cannot extend the window, and a stale login returns
`auth.ErrNoStepUpMethod`.

After a user enrolls a credential, a module can publish `auth.CredentialEnrolled`
with the subject and method so the application can notify them.

## End Other Sessions

Set `UserRecord.Version` in your user provider to a value that changes with the
user's credentials, such as the time the password last changed. `Begin` stores a
hash of it and the authenticator compares it in constant time with the provider's
current value on every request. A mismatch makes the session anonymous,
including the device that made the change; that device logs in again, passing
the second factor again if the user is enrolled. There is no list of a user's
sessions to revoke individually. To keep the device that changed the password,
leave `Version` unchanged and call `guard.RevokeOthers(r)`.

For all functions and options, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/auth).
