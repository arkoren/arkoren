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

Login is throttled before any hashing: 10 attempts per identifier and 10 per
client address in 15 minutes. The next attempt is `429` with `Retry-After`; a
successful login clears the identifier's count. Behind a reverse proxy set
`iam.ClientIP(ratelimit.TrustedProxyKey(...))`, or every user shares the proxy's
address. Also put a [rate limit](../abuse-prevention/rate-limiting.md) in front
of `/register`, `/password/forgot` and the other routes that can send email.

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
password as a fresh proof. It does not rotate the user, so API tokens survive a
change.

## Reset

`POST /password/forgot` takes `{"email": "..."}` and answers `202` whether or not
the user exists; for a known user it hands a single-use token to `iam.Deliver`:

```go
iam.Deliver(func(ctx context.Context, message access.Message) error {
	base := "https://app.example.com/verify-email"
	if message.Kind == access.KindReset {
		base = "https://app.example.com/reset-password"
	}

	return mailer.Send(ctx, message.Identifier, message.Link(base))
})
```

The token has 130 random bits, is stored only as a hash, expires after an hour
and is replaced by the next request. Your page posts `{"token": "...",
"password": "..."}` to `POST /password/reset`. A reset sets the password and
rotates the user, which ends every session, pending login and API token, and it
never logs the user in, so it cannot bypass a second factor. A bad, used or
expired token is `422`.

## Verify Emails

`POST /email/verify` takes `{"token": "..."}` from the link and answers `204`;
it needs no login, because the link is usually opened on another device.
`POST /email/verify/resend` sends a new link to the logged-in user.

Verification never blocks login. Require it where it matters:

```go
posts := web.With(auth.Require(), iam.RequireVerified())
```

`RequireVerified` answers `403` to a user whose email is not verified. A user
created by a provider whose email the provider verified starts verified.

For every function and option, see
[the `password` API reference](https://pkg.go.dev/arkoren.dev/ark/access/password).
