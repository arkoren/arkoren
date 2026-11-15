// Package password registers, logs in and recovers users by password. It works
// with any user store: the application hands it three functions of
// identifiers and subjects ([Users]) and a [hash.Hasher], and the package owns
// only the password hashes and the reset tokens.
//
// # What it owns
//
// A [Verifier] stores one hash per user (the hasher's output, base64 encoded)
// and one pending reset token per user (SHA-256 hex). It offers the Go
// operations ([Verifier.Authenticate], [Verifier.Set], [Verifier.Reset]),
// the [auth.Credential] that lets a user re-prove themselves with their
// password ([Verifier.Credential]), and the JSON handlers behind
// /register, /login, PUT /password, /password/forgot and /password/reset.
//
// # Rules
//
//   - Login answers one [ErrInvalidCredentials] (422) for an unknown user, a
//     wrong password, a user without a password and a disabled user, and runs a
//     dummy hash for the unknown ones so response times match.
//   - [Verifier.LoginHandler] reserves an attempt before hashing and refunds it
//     on success, so only failures count: 10 per identifier and client address
//     together, 50 per identifier across addresses and 100 per client address in
//     15 minutes. Past a limit it answers [ErrThrottled] (429) with Retry-After.
//     [Verifier.Authenticate] itself does not throttle: it is for code that has
//     its own limits. While the user is in a second-factor lockout
//     ([Verifier.CountLockout]) a correct password is not refunded either, so
//     starting pending logins that cannot finish keeps costing throttle budget.
//     Register and forgot are counted too, per identifier as
//     typed and per client address, before any lookup.
//   - A password change keeps the caller's session, ends the user's other
//     logins and runs [Builder.AfterChange]. A reset calls [Users.ProveEmail]
//     and [Users.Rotate], which ends every session, pending login and API token
//     of the user, and never logs the user in.
//   - Registration and password changes are credential changes: the handlers
//     require a browser session where they act on a logged-in user and call
//     [auth.Guard.Fresh] themselves.
//   - Reset tokens are 130 random bits, stored as SHA-256 hex and single use. A
//     user keeps up to three live tokens (the fourth request drops the oldest),
//     and a completed reset deletes the rest. The email goes to the address
//     [Users.Lookup] returned, never to the text of the request. The forgot
//     handler answers an empty 202 whether or not the user exists or the mailer
//     fails, and sends in the background ([Verifier.Wait]).
//
// # Storage
//
// The package never runs DDL. [Verifier.Migrations] describes the tables
// "password_hashes" and "password_resets" (prefixed "<instance>_" for a named
// [Builder.Instance]) in the group "arkoren.dev:ark/access:password", and the
// application's migrator executes them. Every package of the module assumes the
// same [database.Database]; writes that span packages join the transaction
// carried by the context.
//
// Example:
//
//	verifier, err := password.NewVerifier(provider, db, store, hasher, password.Users{
//		Lookup: directory.Lookup,
//		Create: directory.Create,
//		Rotate: directory.Rotate,
//	}, func(passwords *password.Builder) {
//		passwords.Deliver(send)
//	})
//	if err != nil {
//		return err
//	}
//	guard, err := auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
//		policy.StepUp(verifier.Credential())
//	})
//	web.Post("/login", verifier.LoginHandler(guard))
package password
