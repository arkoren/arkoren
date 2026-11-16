---
title: Your Own Users
description: Replace the built-in users table with your own users and keep the access facade's email rules.
order: 80
summary:
  - Give the facade a user provider and the lookup, create and rotate functions it needs.
  - Know which routes and helpers disappear when the facade does not own the users.
  - Provide the verified-email functions that keep the unverified-account protections in force.
---

# Your Own Users

Without configuration the facade keeps its own `users` table and identifies users
by email. If your application already has users, with a different table or a
username to log in, give the facade your users instead. It then talks to them
only through a few functions, and never creates or deletes users on its own.

## Providing Users

Pass an `access.Users` value with the user provider your application built for
[core authentication](../authentication/overview.md) and the functions the
enabled features need:

```go
iam.Users(access.Users{
	Provider:   provider,
	Lookup:     lookup,
	Create:     create,
	Rotate:     rotate,
	Verified:   verified,
	ProveEmail: prove,
})
```

| Field | Purpose | Required |
| --- | --- | --- |
| `Provider` | The `*auth.UserProvider` over your users, shared by every feature. | Always |
| `Lookup` | Maps the identifier a user logs in with to their subject and the stored address to mail. | With passwords |
| `Rotate` | Bumps the user record's `Version`. | With passwords |
| `Create` | Adds a user for registration and for a first provider login. | When registration or provider sign-in creates users |
| `Verified` | Reports whether the user's email is verified. | With `Create`, together with `ProveEmail` |
| `ProveEmail` | Marks the email verified and reports whether it was unverified. | With `Create`, together with `Verified` |

`New` rejects a missing provider, passwords without `Lookup` and `Rotate`, `Verified`
without `ProveEmail` or the reverse, and `Create` without both.

## Required Functions

`Lookup` returns the subject of the user with an identifier, whatever your
application uses to log in, and the address to send mail to. Return the address as
stored with the user, never the typed identifier, so a request cannot redirect a
victim's link. An unknown user must return an error wrapping `auth.ErrUserNotFound`.

`Rotate` must make the user record's `Version` change. A password reset calls it,
and the new version ends every session, pending login and API token of the user.

`Create` receives the identifier and whether the email is verified. Registration
calls it with `false`. The first login with an external identity calls it with the
provider's email, which may be empty, and whether the provider verified it. Its
errors pass through to the client. It must fail on an existing identifier and never
return the existing user: connecting an identity to someone by email is how accounts
get taken over. For an identifier in use, return an error wrapping
`access.ErrIdentifierTaken`: registration then answers `202` like for a new user
and sends the owner an `access.KindRegistration` message, instead of revealing the
account. Leave it nil when users are created elsewhere. Then registration
and creating users from a provider login are unavailable, and an unknown provider
identity is refused.

> [!NOTE]
> `Mount` registers `POST /register` only when `Create` is set. Without it, the
> other password routes still mount and users are created elsewhere.

## Verified Emails

With the built-in users, an unverified user cannot connect a provider, enroll
two-factor or create tokens, and a password reset removes what a squatter attached.
With your own users the facade does the same through two functions, which you give
together:

```go
verified := func(ctx context.Context, subject string) (bool, error) {
	return users.EmailVerified(ctx, subject)
}

prove := func(ctx context.Context, subject string) (bool, error) {
	return users.MarkEmailVerified(ctx, subject) // reports whether it was unverified
}
```

`Verified` gates the connect, two-factor setup and token routes with `403`
`email-unverified`. `ProveEmail` runs inside a completed password reset because the
link reached the mailbox. If it reports that the email was unverified, the facade
deletes the user's connections, authenticator, recovery codes and tokens, and an
error from it rolls the reset back. Both are required when you set `Create`,
because users it makes start unverified.

Without them, for users created elsewhere, the facade can neither gate those routes
nor purge on reset, so enforce the same rules in your application. See
[Unverified Accounts](accounts.md#unverified-accounts) for `iam.OnEmailProven`,
which deletes credentials of modules the facade does not know.

## What Changes

No users table is migrated. These parts need the built-in users and are absent or
panic at composition when you use your own:

- `POST /email/verify` and `POST /email/verify/resend`, which are not mounted, and
  their handler methods, which panic.
- `DELETE /account`. Delete your users yourself and call `provider.Forget` in the
  same transaction, which removes every feature's rows.
- `iam.RequireVerified()`. Check your own verification state.

The other features work unchanged, because they only see subjects. Store subjects
in your own tables as bytewise text columns, because default database collations
compare case-insensitively.

For every function and option, see
[the `access` API reference](https://pkg.go.dev/arkoren.dev/ark/access).
