---
title: Passwords
description: Register, log in, confirm, change and reset passwords, and verify emails, with the access facade.
order: 30
summary:
  - Enable passwords with a hasher and read the register, login, confirm and change requests and their statuses.
  - Understand login throttling, the single invalid-credentials answer and what a change or reset ends.
  - Send reset links, verify emails and require a verified email on selected routes.
---

# Passwords

Passwords give users a way to register and log in with an email and a secret
only they know. Enable them by giving the facade a [hasher](../hashing/overview.md)
and a way to [deliver](overview.md#delivering-messages) reset and verification
links:

```go
iam, err := access.New(db, store, func(iam *access.Builder) {
	iam.Password(hasher)
	iam.Deliver(send)
})
```

With the built-in users, the email address is the identifier, and the requests
below take `email`. Applications that use [their own users](own-users.md) may
send `identifier` instead.

## Register and Log In

`POST /register` creates the user and their password in one transaction and
answers `202` with no body. It does not log the user in: the client calls
`POST /login` next.

```json
{ "email": "ada@example.com", "password": "correct horse battery" }
```

The answer is the same `202` when the email already belongs to a user, so the
route cannot be used to find out who has an account. In that case nothing is
created or changed, and the owner receives a message of kind
`access.KindRegistration` (see [Delivering Messages](overview.md#delivering-messages))
that says someone tried to register with their address, with a reset link in case
it was not them. The facade never links accounts by email. Registrations are
throttled per email and per client address, and the notices count against the
same limits, so the route cannot flood a mailbox. A malformed email is `422`, and
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

A new user starts with an unverified email. If `iam.Deliver` is set,
registration also sends the verification link, and a failing send is logged
without failing the signup.

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

Behind a reverse proxy, tell the facade how to find the client address:

```go
iam.ClientIP(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
```

Without it every user shares the proxy's address, and 100 failed attempts from
anyone lock login for everyone. Registration, forgot-password and email resend are
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
password, deletes the user's pending reset links, ends the user's other logins and
keeps the caller's session, which also records the password as a fresh proof.
It revokes the user's device tokens, because a device has no session to end, but
it does not rotate the user, so personal access tokens survive. See
[Credential Changes](accounts.md#credential-changes) to revoke those too.

If a step after the change fails, the answer is `500` although the password is
already changed and the other logins ended. Repeat the change so the remaining
steps run again.

## Reset

A user who forgot their password asks for a link. `POST /password/forgot` takes
`{"email": "..."}` and answers an empty `202` whether or not the user exists and
whether or not the mailer works. The email is sent in the background, so neither
the answer nor its timing reveals anything, and a delivery failure is logged,
never shown. Call `iam.Wait(ctx)` at shutdown so queued messages are not lost.

At most 5 requests per identifier as typed and 30 per client address in 15
minutes are accepted, then `429`. For a known user the facade hands a single-use
token to your [delivery function](overview.md#delivering-messages), addressed to
the email stored with the user, never to the text of the request: `bob@gmaİl.com`
finds `bob@gmail.com`, and the link goes to `bob@gmail.com`.

The token has 130 random bits, is stored only as a hash and expires after an hour.
A user keeps up to three live tokens, so asking again does not kill the link
already in their inbox, and the fourth request drops the oldest. To change the
lifetime, pass `passwords.ResetTTL(d)` in the `iam.Password` callback.

The link carries the token in the fragment (`#token=...`), so your reset page
reads it from `location.hash` and posts it with the new password to
`POST /password/reset`:

```json
{ "token": "...", "password": "a new longer password" }
```

A reset sets the password, deletes the user's other reset tokens and rotates the
user, which ends every session, pending login and API token. It never logs the
user in, so it cannot bypass a second factor. A bad, used or expired token is
`422`, and a password that fails the policy is `422` and leaves the token usable.
A completed reset also clears the failed-login count kept against the address
across clients.

A reset proves the user controls the mailbox. When the email was not verified yet,
it also marks the email verified and removes what a squatter could have attached
to the account; see [Credential Changes](accounts.md#unverified-accounts).

## Verify Emails

`POST /email/verify` takes `{"token": "..."}` from the link, which carries it in
the fragment (`#token=...`) for your page to read from `location.hash`, and
answers `204`. The
token lasts 24 hours, and a new link replaces an earlier one. The flow is: the
user logs in, then your verify page posts the token. It needs a browser session
of the user the link belongs to, so a page that receives the link while logged out
should send the user to log in first and then post the token. Without a session
the answer is `401`, and a session of another user is `403`
(`account:verify-wrong-account`). Neither consumes the token.

This is deliberate. Otherwise an attacker could register your address with their
own password and let your click verify it. Only a password reset, which also
deletes whatever the registrant attached, proves ownership of such an account.

`POST /email/verify/resend` sends a new link to the logged-in user's stored
address and answers an empty `202`, as forgot-password does. It sends nothing to
a user who is already verified, and it accepts at most 5 requests per user and 20
per client address in 15 minutes. Registration is limited to 5 per identifier as
typed and 30 per client address.

Verification never blocks login. Require it where it matters:

```go
posts := web.With(auth.Require(), iam.RequireVerified())
```

`RequireVerified` answers `403` to a user whose email is not verified. It reads
the claim recorded at login, so a user who verified since their last login passes
only after logging in again. A user created by a provider whose email the provider
verified starts verified. Until an email is verified, the account cannot connect
a provider, enroll two-factor or create tokens (`403` `email-unverified`). With
[your own users](own-users.md), `RequireVerified` panics at composition because
you own the verification state.

For every function and option, see
[the `password` API reference](https://pkg.go.dev/arkoren.dev/ark/access/password).
