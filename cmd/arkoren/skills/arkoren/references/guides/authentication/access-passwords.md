---
title: Access Passwords
description: Register, log in, confirm, change and reset passwords, and verify emails, with the access facade.
order: 16
summary:
  - Enable passwords with a hasher and read the register, login, confirm and change requests and their statuses.
  - Understand login throttling, the one invalid-credentials answer and what a change or reset ends.
  - Send reset and verification links, and require a verified email on selected routes.
---

# Access Passwords

Enable passwords by giving the facade a [hasher](../hashing/overview.md):

```go
iam, err := access.New(db, store, func(iam *access.Builder) {
	iam.Password(hasher)
	iam.Deliver(send) // reset and verification links
})
```

With the built-in users, the email address is the identifier. The requests below
take `email`; applications that use [their own users](access.md#bring-your-own-users)
may send `identifier` instead.

## Register and Log In

`POST /register` creates the user and their password in one transaction, logs
them in and answers `201` with `{"subject": "..."}`:

```json
{ "email": "ada@example.com", "password": "correct horse battery" }
```

A taken email is a `409` (the facade never links accounts by email), a malformed
one a `422`, and a password outside the policy a `422`. The default policy
requires 8 to 72 bytes, because bcrypt hashers reject more; replace it with
`password.Builder.Policy`. If `iam.Deliver` is set, registration also sends the
email verification link.

`POST /login` takes the same body and answers `204`, or `202` when the user has
a second factor to answer. Every failure is one `422` problem: an unknown email,
a wrong password, a user without a password and a disabled user look the same,
and an unknown email still costs one dummy hash, so response time does not reveal
which accounts exist.

Login is throttled before any hashing, and only failures count: 10 per
identifier and client address together, 50 per identifier across all addresses
and 100 per client address in 15 minutes. The next attempt is `429` with
`Retry-After`; a successful login refunds its attempt. Keying the first limit
on the address too means someone failing from elsewhere cannot lock the real
user out; the looser 50 still stops a distributed guess at one account, and
that user waits for the window or resets the password. Behind a reverse proxy
set `iam.ClientIP(ratelimit.TrustedProxyKey(...))`: otherwise every user shares
the proxy's address, and 100 failed attempts from anyone lock login for
everyone. IPv6 clients are counted per /64, because one subscriber controls a
whole /64. `/register`, `/password/forgot` and `/email/verify/resend` are
throttled too (see below); still put a
[rate limit](../abuse-prevention/rate-limiting.md) in front of them.

## Confirm and Change

`POST /confirm` re-proves a logged-in user with a step-up method. For a password:

```json
{ "method": "pwd", "password": "correct horse battery" }
```

A wrong password is core's `403`, and core counts the attempt. `PUT /password`
changes the password:

```json
{ "current_password": "correct horse battery", "password": "a new longer password" }
```

It needs a browser session and a fresh login. The current password is required
unless the user has none yet, such as someone created by a provider login, who
sets their first one without it. A change stores the new password, ends the
user's other logins and keeps the caller's session, which also records the
password as a fresh proof. It also revokes the user's device tokens, because a
device has no session to end. It does not rotate the user, so personal access
tokens, which the user made on purpose for scripts, survive a change; call
`iam.RevokeTokensOnPasswordChange()` to revoke them too.

## Reset

`POST /password/forgot` takes `{"email": "..."}` and answers an empty `202`
whether or not the user exists, whether or not the mailer works. The email is
sent in the background, so neither the answer nor its timing reveals anything;
a delivery failure is logged, never shown. Call `iam.Wait(ctx)` at shutdown so
queued messages are not lost. At most 5 requests per identifier as typed and 30
per client address in 15 minutes (`429` after). For a known user it hands a
single-use token to `iam.Deliver`, addressed to the email stored with the user,
never to the text of the request (`bob@gmaİl.com` finds `bob@gmail.com` and
the link goes to `bob@gmail.com`):

```go
iam.Deliver(func(ctx context.Context, message access.Message) error {
	base := "https://app.example.com/verify-email"
	if message.Kind == access.KindReset {
		base = "https://app.example.com/reset-password"
	}

	return mailer.Send(ctx, message.Identifier, message.Link(base))
})
```

The token has 130 random bits, is stored only as a hash and expires after an
hour. A user keeps up to three live tokens: asking again does not kill the link
already in their inbox, and the fourth request drops the oldest. Your page
posts `{"token": "...", "password": "..."}` to `POST /password/reset`. A reset
sets the password, deletes the user's other reset tokens and rotates the user,
which ends every session, pending login and API token, and it never logs the
user in, so it cannot bypass a second factor. A bad, used or expired token is
`422`.

A completed reset proves the user controls the mailbox. If the email was not
verified yet, the reset marks it verified and also deletes every connected
provider, the authenticator, the recovery codes and all tokens: someone who
registered the victim's address before the victim did could have attached them
(the pre-registration hijack). Until an email is verified, the account cannot
connect a provider, enroll two-factor or create tokens (`403` email-unverified).
With your own users (`iam.Users`) the facade does this only if you give
`Users.Verified` (the gate) and `Users.ProveEmail` (the purge), together, and
both are required when you set `Users.Create`. The checks live in the connect,
token and two-factor handlers themselves, so they hold on custom paths too. A
completed reset also clears the failed-login count kept against the address
across clients. If `AfterChange` fails during a password change the answer is
`500` after the other logins already ended; repeat the change. `Users.Lookup`
returns the stored address to mail, and `Users.Create` receives whether the
provider verified the email and must fail on an existing identifier.

## Verify Emails

`POST /email/verify` takes `{"token": "..."}` from the link and answers `204`;
it needs a browser session of the user the link belongs to, so the user opens
the link while logged in, as with GitHub. A click while logged out, or as
another user, is `401` and consumes nothing: otherwise an attacker could
register your address with their own password and let your click verify it.
Only a password reset, which also deletes whatever the registrant attached,
proves ownership of such an account.
`POST /email/verify/resend` sends a new link to the logged-in user's stored
address: an empty `202` as above, at most 5 per user and 20 per client address
in 15 minutes. Registration is limited to 5 per identifier as typed and 30 per
client address, and a failing verification mail is logged without failing the
signup.

Verification never blocks login. Require it where it matters:

```go
posts := web.With(auth.Require(), iam.RequireVerified())
```

`RequireVerified` answers `403` to a user whose email is not verified. A user
created by a provider whose email the provider verified starts verified.

For every function and option, see
[the `password` API reference](https://pkg.go.dev/arkoren.dev/ark/access/password).
