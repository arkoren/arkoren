---
title: Your Own Users
description: Connect an existing user store to IAM while preserving credential versioning, email ownership checks, and transactional cleanup.
order: 110
summary:
  - Replace the built-in directory with a provider and only the adapters your enabled features need.
  - Preserve secure creation, stored-recipient lookup, and atomic email-proof behavior.
  - Own user deletion and extend credential cleanup without relying on best-effort events.
---

# Your Own Users

If your application already has users, IAM does not need a second users
table. Give it your provider and the operations it needs to use those users.
Passwords, authenticators, tokens, and connections still store their data
under the same stable subject strings.

Start from a working `*auth.UserProvider` over your store, following
[user-provider setup](../authentication/session-login.md#load-accounts). Its `Find` operation
must distinguish missing users (`auth.ErrUserNotFound`) from disabled users
(`auth.ErrUserDisabled`) and return a credential `Version` that changes
when access derived from that version must end.

## Replace the Directory

For password login with users created elsewhere, provide the provider,
identifier lookup, and version-rotation function:

```go
users := iam.Users{
	Provider: provider,
	Lookup:   lookup,
	Rotate:   rotate,
}
```

`lookup` is your
`func(context.Context, string) (subject string, address string, err error)`.
It maps a login identifier to a subject and the **stored** email address.
Return an error wrapping `auth.ErrUserNotFound` for an unknown identifier.
Do not return the typed identifier as the recipient: case folding or
look-alike characters can otherwise redirect a victim's reset link.

`rotate` is your `func(context.Context, string) error`.
It must change the same `UserRecord.Version` returned by the provider.
A reset calls it to invalidate every previous session, pending login, and
API token. Use the supplied context so it joins the reset's database
transaction.

Pass this value while enabling passwords:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Users(users)
	identity.Password(true, hasher)
})
```

With the usual `Register`, this enables password login and change without
registration or email recovery.
You can configure `passwords.Reset` using the same
[email-link handoff](email-links.md); IAM then mails the address returned by
your lookup. Requests can use `identifier` instead of `email`.

The minimal adapter above supplies no email-verification checks.
Use it only when your application enforces the corresponding ownership rules;
otherwise add the pair below before enabling credential enrollment.

## Verified Emails

To preserve IAM's built-in email protections, supply `Verified` and
`ProveEmail` together. `Verified` reads the current state:

```go
verified := func(ctx context.Context, subject string) (bool, error) {
	return directory.EmailVerified(ctx, subject)
}
```

Here `directory` is your application repository, not IAM's built-in
directory. A false result prevents provider connection, two-factor enrollment,
and token creation with `403` `account:email-unverified`.

`ProveEmail` marks the email verified and reports whether it was unverified
before the call:

```go
prove := func(ctx context.Context, subject string) (bool, error) {
	return directory.MarkEmailVerified(ctx, subject)
}
```

Implement that test-and-update atomically using the supplied transaction
context. A completed password reset calls it because the mailbox has just
been proved. If it returns true, IAM purges connected identities,
authenticator enrollment, recovery codes, and tokens that an earlier
registrant might have attached. A failure rolls the reset's SQL writes back.

Include the pair in the adapter:

```go
users := iam.Users{
	Provider:   provider,
	Lookup:     lookup,
	Rotate:     rotate,
	Verified:   verified,
	ProveEmail: prove,
}
```

Setting just one function is invalid.
`identity.RequireVerified()` calls `Verified` on every request; without it, that
middleware constructor panics. Without the pair, IAM cannot gate enrollment
or purge unverified credentials on reset; your application owns both rules.

Your application also owns ordinary email-verification messages and UI.
IAM's `/email/verify` and `/email/verify/resend` routes are not registered
with custom users, and requesting their handlers directly panics.

## Let Users Join

If IAM may request user creation, add `Create` to the adapter:

```go
users := iam.Users{
	Provider:   provider,
	Lookup:     lookup,
	Rotate:     rotate,
	Verified:   verified,
	ProveEmail: prove,
	Create:     create,
}
```

`create` is your
`func(context.Context, string, bool) (string, error)`. The arguments are the
identifier and its verified status. Password registration supplies `false`.
Provider sign-up supplies a nonempty provider-verified email and `true`;
the resolver rejects unproved emails before creation.

Creation must fail for an existing identifier, never return its subject.
Otherwise an external identity could attach itself to another person's user.
For a taken identifier, wrap `iam.ErrIdentifierTaken` so registration can
return the same `202` as for a new account.
Other errors reach the client; use safe problem values for public refusals.
Creation and credential writes must use the same database transaction context.

`Create` requires both verification functions, even if users can also be
created elsewhere. It does not open sign-up by itself:
enable password `Registration(true)` or a provider's `login.SignUp(true)`
explicitly. Either setting without `Create` is ignored with a warning: no
route accepts registrations and the provider only signs in known identities.

With custom users, IAM does not automatically send registration verification
or taken-address notices. Supply your own `Registered` and `Taken` password
hooks with comparable work on both branches. Hook failures are logged without
changing the accepted registration response.

Automatic provider takeover by matching an unverified email is built-in-directory
behavior only. Your lookup may accept usernames or other identifiers, so IAM
does not assume it can use the provider's email to find an existing account.

## Show Your Users

`GET /me` answers with the subject and the `email` and `email_verified` claims of
your user record. IAM never encodes the raw record value: your driver's struct
often holds a credentials stamp or a disabled flag. To publish your user, give
`Me` a projection from your driver's type to the type clients may see. The type
arguments are inferred from the function:

```go
type Account struct {
	ID               int
	Name, Email      string
	Plan             string
	Disabled         bool
	CredentialsStamp string
}

type PublicAccount struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Plan  string `json:"plan"`
}

identity.Me(true, func(me *iam.MeBuilder) {
	me.User(func(account Account) PublicAccount {
		return PublicAccount{ID: account.ID, Name: account.Name, Email: account.Email, Plan: account.Plan}
	})
})
```

The request then answers `{"subject": "42", "email": "ada@example.com",
"email_verified": true, "user": {"id": 42, "name": "Ada Lovelace", "email":
"ada@example.com", "plan": "pro"}}`. Without `User` there is no `user` member.
A record without a value adds none either. If the value is not an `Account`, the
request fails with `500` and the log names both types. With the built-in users
`User` is ignored with a warning, because they always present `account.Account`.
See [Routes](routes.md#whoami) for the other sections.

## Delete Your Users

No built-in users table or `DELETE /account` route is supplied.
`identity.Directory()` returns nil, `DeleteAccountHandler()` panics, and combining
`identity.Users` with `identity.Directory` configuration makes construction fail.
The enabled credential stores and their migrations remain available.

Inside your application's authorized deletion flow, forget credentials and
delete the user in one transaction:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := identity.UserProvider().Forget(ctx, subject); err != nil {
		return err
	}

	return directory.Delete(ctx, subject)
})
```

`directory.Delete` is your application's deletion method.
Forget hooks run in registration order; the first error stops the operation.
Their SQL work must use that context and database to roll back together.
The [account-deletion guidance](accounts.md#delete-an-account) explains the
separate boundary for cached login records and external side effects.

Subjects are bytewise, case-sensitive identifiers. Store them in your related
tables as bytewise text columns, not under a case-insensitive default collation.

## Additional Credentials

If your application or another module supplies a credential, register it through
IAM's `SecondFactor` or `StepUp` builder methods. They append after the
built-in methods. `Authenticator` similarly appends request authenticators
after sessions and IAM tokens.

For example, with an application-provided `auth.Credential` named `credential`
and a transactional removal function named `removeCredentials`:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.SecondFactor(credential)
	identity.StepUp(credential)
	identity.OnEmailProven(removeCredentials)
})
```

This illustrates the extension points, not a shipped passkey implementation.
Retain your user adapter and login features in the real constructor.
Use the facade registration methods rather than putting credentials directly
in `identity.Guard`: duplicate method names make construction fail.
After construction, `identity.MustAccept(method)` can assert a required method
at startup; it panics if that method was not registered.

`OnEmailProven` runs after IAM's own purge inside a reset or provider takeover
that proves an **unverified** mailbox. It does not run for an already verified
account. Multiple hooks run in order, all are attempted, and their errors are
joined; a failure rolls back the SQL transaction.
Use this hook for required credential removal, not an event listener.
Register a provider `OnForget` hook too if your credential store must be cleaned
up on account deletion.

For exact route availability and custom registration, use the [Routes](routes.md)
reference.

For adapter fields and extension hooks, see the
[`iam` API reference](https://pkg.go.dev/arkoren.dev/ark/iam).
