---
title: Passwords
description: Log in, confirm, change and reset passwords, optionally let users register, and verify emails, with the access facade.
order: 30
summary:
  - Enable passwords with a hasher, switch registration on together with reset, and read the login, confirm and change requests and their statuses.
  - Know the register-then-login limitation, since registration never logs in and does not tell a new address from a taken one.
  - Understand login throttling, the single invalid-credentials answer and what a change or reset ends.
  - Reset forgotten passwords with signed links, verify emails and require a verified email on selected routes.
---

# Passwords

Passwords give users a way to log in with an email and a secret only they know.
Enable them by giving the facade a [hasher](../hashing/overview.md):

```go
clientIP := ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8"))
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Password(hasher)
})
```

Login, confirm and change need nothing more. Forgotten-password reset also needs
a signer and your own functions to dispatch and deliver the email; see
[Reset](#reset).

Self-service registration is off by default: an open `POST /register` route is
an abuse surface that applications with invitations, an admin console or
provider sign-up never asked for. With it off, the route is not mounted, and you
add users yourself; see [Adding Users](accounts.md#adding-users). Switch it on
in the password settings:

```go
iam.Password(hasher, func(passwords *password.Builder) {
	passwords.Registration(true)
})
```

With your own users, `Registration(true)` needs `Users.Create`, and `New` fails
with `access.ErrInvalidConfig` without it. With the built-in users it needs
`Reset` as well: when someone registers an address that already belongs to a
user, the owner is sent a reset notice whose link also proves their mailbox, and
without `Reset` that owner would have no way to take the account back from
whoever registered it first. `New` fails with `access.ErrInvalidConfig` without it:

```go
iam.Password(hasher, func(passwords *password.Builder) {
	passwords.Registration(true).Reset(signer, dispatchReset, deliverReset)
})
```

With the built-in users, the email address is the identifier, and the requests
below take `email`. Applications that use [their own users](own-users.md) may
send `identifier` instead.

## Register and Log In

With registration on, `POST /register` creates the user and their password in one
transaction and answers `202` with no body. It does not log the user in: the
client calls `POST /login` next.

```json
{ "email": "ada@example.com", "password": "correct horse battery" }
```

The answer is the same `202` when the email already belongs to a user, so the
response alone does not say whether the address was free; see
[Register Then Log In](#register-then-log-in) for what a following login still
reveals. In that case nothing is created or changed, and the owner is told instead. The facade never links
accounts by email. Registrations are throttled per email and per client address,
and what they dispatch counts against the same limits, so the route cannot flood a
mailbox. A malformed email is `422`, and
a password outside the policy is `422`. The default policy
requires 8 to 72 bytes, because bcrypt hashers reject more. Replace it with
`password.Builder.Policy`:

```go
iam.Password(hasher, func(passwords *password.Builder) {
	passwords.Policy(func(ctx context.Context, value string) error {
		if len(value) < 12 {
			return fmt.Errorf("too short: %w", password.ErrInvalidPassword)
		}

		return nil
	})
})
```

A new user starts with an unverified email. What registration dispatches depends
on whether you also configured [email verification](#verify-emails), and it is the
same work on both paths so the answer and its timing do not tell a new address
from a taken one:

| Configured | New address | Taken address |
| --- | --- | --- |
| Reset and verification | Dispatches a verification for the new user | Dispatches a reset notice to the owner (`Notice` set) |
| Reset only | Dispatches nothing | Dispatches nothing |

A failing dispatch on registration is logged and the answer stays `202`. With your
own users the application proves emails itself, so the facade dispatches nothing
on registration.

### Register Then Log In

Registration never logs the user in, and the uniform `202` has a limit you should
know about. The answer does not say whether the address was free, but the next
call does: after `POST /register` with a password you chose, `POST /login` with
that password succeeds for a new address and fails for a taken one, because the
registrant's password is not the owner's. So registering and then logging in is an
oracle for whether an address has an account, and anyone can use it.

What bounds it: registration is throttled to 5 attempts per address as typed and
30 per client address in 15 minutes, and the login throttles apply to the probe
too. A probe of a taken address sends its owner a reset notice, so it is visible to
the person it is aimed at. A probe of a free address is not harmless: it creates an
unverified account that holds the address until its owner arrives. That account
can attach nothing until the address is verified, and a password reset or a
provider login that proves the mailbox takes it over (see
[Unverified Accounts](accounts.md#unverified-accounts)). The design hides the
answer from someone who only sees the `202`, not from someone willing to log in.
If hiding which addresses have accounts matters more than self-service sign-up,
leave registration off and add users yourself, or put a
[rate limit](../abuse-prevention/rate-limiting.md) and a
[CAPTCHA](../abuse-prevention/captcha.md) in front of the route.

For the same reason a sign-up form should not say "account created" on the `202`:
tell the user to check their email, log them in, and show a login failure as a
login failure. A user who mistypes their address, or registers one that is already
theirs, sees that failure. There is no option to log in on registration.

`POST /login` takes the same body and answers `204`, or `202` when the user has a
second factor to answer. Every failure is one `422` problem: an unknown email, a
wrong password, a user without a password and a disabled user look the same, and
an unknown email still costs one dummy hash, so response time does not reveal
which accounts exist.

## Throttling

Login is throttled before any hashing, and only failures count. In 15 minutes the
limits are 10 failures per identifier and client address together, 50 per
identifier across all addresses and 100 per client address. The next attempt is
`429` with `Retry-After`, and a successful login refunds its attempt.

The refund stops while the user is in a second-factor lockout. A correct password
then only parks a pending login that cannot finish, so the facade keeps the attempt
counted: someone who knows the password cannot start pending logins after every
lockout at no cost. The login asks the guard (`guard.LockedOut`) after a correct
password, so it needs no event bus and no setup. If the guard cannot read the
lockout, for example because the cache is down, the login fails with `500` and
the attempt stays counted.

Keying the first limit on the address too means someone failing from elsewhere
cannot lock the real user out. The looser 50 still stops a distributed guess at
one account, and that user waits for the window or resets the password. IPv6
clients are counted per /64, because one subscriber controls a whole /64.

`New` requires a function that finds the client address as its third argument,
because the throttles count by it. Behind a reverse proxy, name the proxies you
trust:

```go
clientIP := ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8"))
```

An application that faces the internet directly with no proxy passes
`ratelimit.TrustedProxyKey()` with no prefixes, which returns the host of
`r.RemoteAddr`. Doing that behind a proxy makes every user share
the proxy's address, and 100 failed attempts from anyone lock login for everyone. Registration, forgot-password and email resend are
throttled too (see below), per identifier as typed and per client address; still
put a [rate limit](../abuse-prevention/rate-limiting.md) in front of them.

## Confirm and Change

`POST /confirm` re-proves a logged-in user with a step-up method, which makes the
login fresh for [credential changes](accounts.md#credential-changes). For a
password:

```json
{ "method": "pwd", "password": "correct horse battery" }
```

A wrong password is core's `403`, and core counts the attempt.

`PUT /password` changes the password. It needs a browser session and a fresh
login:

```json
{ "current_password": "correct horse battery", "password": "a new longer password" }
```

The current password is required unless the user has none yet, such as someone
created by a provider login, who sets their first one without it. The current
password is checked with a per-user failure limit. A change stores the new
password, ends the user's other logins and keeps the caller's session, which also records the password as a fresh proof.
The facade also revokes the user's tokens, device and personal ones, because a
device has no session to end and a stolen password could have minted a personal
token; `iam.KeepPersonalTokens()` limits it to device tokens. A change does not rotate
the user. See [Credential Changes](accounts.md#credential-changes) for what each
change ends.

If a step after the change fails, the answer is `500` although the password is
already changed and the other logins ended. Repeat the change so the remaining
steps run again.

## Reset

A user who forgot their password asks for a link. Reset is off until you configure
it in the password settings, with the signer and the two functions described in
[Sending Email Links](overview.md#sending-email-links):

```go
iam.Password(hasher, func(passwords *password.Builder) {
	passwords.Reset(signer, dispatchReset, deliverReset)
})
```

`POST /password/forgot` takes `{"email": "..."}`, counts the request, calls your
`dispatch` function with the identifier exactly as typed and answers an empty
`202`, whether or not the user exists. Nothing is looked up in the request, so
neither the answer nor its timing reveals anything. If `dispatch` fails the work
was not stored, so the answer is `500` and the client can try again. At most 5
requests per identifier as typed and 30 per client address in 15 minutes are
accepted, then `429`, before anything is dispatched.

The worker then runs `iam.SendReset`: for a known user it signs a link bound to the
user's current password hash and calls your `deliver` function, addressed to the
email stored with the user, never to the text of the request: `bob@gmaİl.com` finds
`bob@gmail.com`, and the link goes to `bob@gmail.com`. An unknown identifier sends
nothing. The link expires after an hour; change that with `passwords.ResetTTL(d)`.

Your reset page reads the link from the fragment and posts it with the new
password to `POST /password/reset`:

```json
{ "token": "...", "password": "a new longer password" }
```

The password policy is checked first, so a password that fails it is `422` and
leaves the link usable. Then the link is verified against the user's current hash
and the new hash is stored by a conditional write inside one transaction: it
matches only the hash the link was signed for, so a link works once, and of two
concurrent submits exactly one succeeds. A user who had no password, such as one
created by a provider login, sets their first one the same way. A bad, forged,
expired or used link is `422` with the `invalid-token` problem.

A reset rotates the user, which ends every session, pending login and API token,
and it also invalidates every other reset link sent before, because the password
they were bound to is gone. It never logs the user in, so it cannot bypass a
second factor.

A reset proves the user controls the mailbox. When the email was not verified yet,
it also marks the email verified and removes what a squatter could have attached
to the account; see [Credential Changes](accounts.md#unverified-accounts).

## Verify Emails

Verification is off until you configure it on the built-in users, with the same
signer and your own `dispatch` and `deliver` functions
([Sending Email Links](overview.md#sending-email-links)):

```go
iam.Directory(func(accounts *account.Builder) {
	accounts.Verification(signer, dispatchVerification, deliverVerification)
})
```

Registration, and `POST /email/verify/resend`, dispatch an
`account.VerifyRequest` for the user; the worker runs `iam.SendVerification`,
which signs a link bound to the user's email and verified state and calls
`deliver`. A user who is already verified is sent nothing. The link lasts 24 hours.

`POST /email/verify` takes `{"token": "..."}` from the link, which your page reads
from the fragment, and answers `204`. The flow is: the user logs in, then your
verify page posts the token. It needs a browser session of the user the link
belongs to, so a page that receives the link while logged out should send the user
to log in first and then post the token. Without a session the answer is `401`,
and a session of another user is `403` (`account:verify-wrong-account`). Neither
changes anything. A second click on a link that already verified the email is
`422` with the `invalid-token` problem, and the `EmailVerified` event is published
only once.

This is deliberate. Otherwise an attacker could register your address with their
own password and let your click verify it. Only a password reset, which also
deletes whatever the registrant attached, proves ownership of such an account.

`POST /email/verify/resend` dispatches a new verification for the logged-in user
and answers an empty `202`, as forgot-password does, or `500` when `dispatch`
fails. It accepts at most 5 requests per user and 20 per client address in 15
minutes. Registration is limited to 5 per identifier as typed and 30 per client
address.

Verification never blocks login. Require it where it matters:

```go
posts := web.With(auth.Require(), iam.RequireVerified())
```

`RequireVerified` answers `403` to a user whose email is not verified. With the
built-in users it reads the claim of the user the guard loads for each request, so
a user who just verified passes on their next request, without logging in again.
A user created by a provider whose email the provider verified starts verified. Until an email is verified,
the account cannot connect a provider, enroll two-factor or create tokens (`403`
`email-unverified`). With [your own users](own-users.md), it asks your
`Users.Verified` function on every request instead, so the answer is as current as
your own data; it panics at composition only when you gave no `Users.Verified`.

For every function and option, see
[the `password` API reference](https://pkg.go.dev/arkoren.dev/ark/access/password).
