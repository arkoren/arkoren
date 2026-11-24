---
title: Custom User Stores
nav: Custom User Stores
description: Connect IAM to your existing users table, preserve email-verification and sign-up safeguards, compose your own /me endpoint, and register custom credentials.
order: 110
summary:
  - Replace IAM's built-in users table with identity.Users using your existing UserProvider, lookup, and version rotation.
  - Wire Verification and Create on UsersBuilder to keep credential-enrollment gates and sign-up safe.
  - Compose your own /me endpoint, own user deletion via Forget, and register custom credentials.
---

# Custom User Stores

If your application already has a `users` table, IAM does not need a second one. Pass your existing `*auth.UserProvider` to `identity.Users(...)` inside `iam.New`, and IAM skips its built-in users table while keeping passwords, two-factor secrets, tokens, external connections, and permissions stored under your existing subject strings.

Start with a working `*auth.UserProvider` over your users table (see [loading accounts](../authentication/session-login.md#load-accounts)). Its `Find` method must distinguish missing users (`auth.ErrUserNotFound`) from disabled users (`auth.ErrUserDisabled`) and return a `Version` that changes whenever old sessions and tokens must be invalidated.

## Replacing the Built-In Directory

If you only use external providers or API tokens with users managed elsewhere, passing `provider` alone is enough (`identity.Users(provider)`). For password login, also configure `Lookup` and `Rotate` in the `UsersBuilder` callback:

```go
identity.Users(provider, func(users *iam.UsersBuilder) {
	users.Lookup(store.SubjectAndEmail)
	users.Rotate(store.BumpVersion)
})
```

What each function must do:

- **`Lookup(ctx, identifier) (subject, address, err)`**: Maps a login identifier (email, username, etc.) to the user's subject and their **stored** email address. Wrap `auth.ErrUserNotFound` when missing. Never echo the raw typed identifier back as `address`, because case folding or look-alike characters could misdirect reset emails.
- **`Rotate(ctx, subject) error`**: Bumps the same `UserRecord.Version` that `Provider` returns, using `ctx` so it joins password-reset transactions.

You cannot combine `identity.Users(...)` with `identity.Directory(...)` (`iam.ErrInvalidConfig`). With custom users, `identity.Directory()` returns `nil`, requests may send `identifier` instead of `email`, and IAM's migrations omit the built-in `users` table.

> [!NOTE]
> The minimal adapter above includes no email-verification checks. Add `Verification` below before enabling credential enrollment unless your application enforces equivalent ownership rules itself.

## Preserving Email-Verification Protections

To keep IAM's built-in guards against unverified accounts (blocking provider connections, two-factor enrollment, and token creation with `403` `account:email-unverified`, and purging unverified credentials on password reset), wire `Verification` with both functions:

```go
identity.Users(provider, func(users *iam.UsersBuilder) {
	users.Lookup(store.SubjectAndEmail)
	users.Rotate(store.BumpVersion)
	users.Verification(store.EmailVerified, store.MarkEmailVerified)
})
```

- **`verified(ctx, subject) (bool, error)`**: Reports whether the user's email is currently verified. `identity.RequireVerified()` calls this on every request (and panics at startup if verification is not configured).
- **`proveEmail(ctx, subject) (wasUnverified bool, err error)`**: Atomically marks the email verified inside the supplied transaction context and returns `true` if it was previously unverified. When it returns `true` during a password reset, IAM purges any connections, two-factor enrollment, recovery codes, and tokens attached before the mailbox was proved.

Both functions must be non-nil. Because IAM does not own your users table, it does not mount `/email/verify` or `/email/verify/resend`; your application owns ordinary verification emails and UI.

## Enabling User Creation

To support password `Registration(true)` or external provider `SignUp(true)`, add `Create` alongside `Verification` (verification is required whenever `Create` is set):

```go
identity.Users(provider, func(users *iam.UsersBuilder) {
	users.Lookup(store.SubjectAndEmail)
	users.Rotate(store.BumpVersion)
	users.Verification(store.EmailVerified, store.MarkEmailVerified)
	users.Create(store.Create)
})
```

`Create(ctx, identifier, verified) (subject, err)` receives `verified = false` for password registration and `verified = true` with a provider-verified email for provider sign-up:

- **Never return an existing user**: Always fail on a duplicate identifier, wrapping `iam.ErrIdentifierTaken` so password registration returns `202` and triggers your taken-address hook rather than leaking existence.
- **Join the transaction**: Use `ctx` so user creation and initial credential writes commit or roll back together.
- **Own registration emails**: IAM does not automatically queue verification or taken-address emails for custom users; attach `Registered` and `Taken` hooks on `password.Builder` with comparable work on both branches.

Automatic provider takeover of unverified accounts is available only with the built-in directory, since IAM cannot assume your custom login identifier is an email address.

## Building Your Own `/me` Endpoint

IAM never serializes your application's user struct. `GET /account` only exposes `subject` and the `email`/`email_verified` claims from `auth.UserRecord.Claims` (with no built-in timestamps). To serve a richer profile, embed `iam.AccountView` in your own response type and call `identity.Account(r.Context())`:

```go
type Me struct {
	iam.AccountView
	Name string `json:"name"`
	Plan string `json:"plan"`
}

web.Get("/me", func(w http.ResponseWriter, r *http.Request) error {
	view, err := identity.Account(r.Context())
	if err != nil {
		return err
	}
	profile, err := profiles.Find(r.Context(), view.Subject)
	if err != nil {
		return err
	}

	return response.JSON(w, http.StatusOK, Me{AccountView: view, Name: profile.Name, Plan: profile.Plan})
})
```

`identity.Account` returns `auth.ErrUnauthenticated` (`401`) when no authenticated identity is present, and includes any optional fields enabled via `identity.Account(...)`.

## Owning User Deletion

With custom users, IAM does not mount `DELETE /account` (and calling `DeleteAccountHandler()` panics). Inside your own authorized deletion handler, call `identity.UserProvider().Forget(ctx, subject)` and delete your user row in the same database transaction:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := identity.UserProvider().Forget(ctx, subject); err != nil {
		return err
	}

	return directory.Delete(ctx, subject)
})
```

`Forget` runs every IAM and application forget hook in registration order, rolling back the SQL transaction on the first error. Store subjects in your own tables using bytewise, case-sensitive text columns (avoid case-insensitive default collations).

## Custom Credentials

If your application or another module provides an extra `auth.Credential` (such as passkeys) or an extra `auth.Authenticator`, register them on the IAM builder rather than directly on `identity.Guard` so duplicate method names are caught at startup:

```go
identity.SecondFactor(credential)
identity.StepUp(credential)
identity.OnEmailProven(removeCredentials)
```

- `SecondFactor` and `StepUp` append after IAM's built-in methods; `Authenticator` appends after sessions and IAM tokens.
- `identity.MustAccept(method)` asserts at startup that a required method was registered (panicking otherwise).
- `OnEmailProven` runs inside the transaction of a password reset or provider takeover that proves an **unverified** mailbox, after IAM's own purge. All registered hooks run in order and any error rolls back the SQL transaction. Also register an `OnForget` hook so your credential store cleans up on account deletion.

Finally, see [Routes & Errors](routes.md) for the complete HTTP route table, problem types, and custom path mounting.

For adapter fields and extension hooks, see the [`iam` API reference](https://pkg.go.dev/arkoren.dev/ark/iam).
