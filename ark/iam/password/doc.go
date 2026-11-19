// Package password registers, logs in and recovers users by password. It works
// with any user store: the application hands it three functions of
// identifiers and subjects ([Users]) and a [hash.Hasher], and the package owns
// only the password hashes. Reset links are signed values, not rows.
//
// # What it owns
//
// A [Verifier] stores one hash per user (the hasher's output, base64 encoded).
// It offers the Go
// operations ([Verifier.Authenticate], [Verifier.Set], [Verifier.Reset]),
// the [auth.Credential] that lets a user re-prove themselves with their
// password ([Verifier.Credential]), and the JSON handlers behind
// /register, /login, PUT /password, /password/forgot and /password/reset.
// Registration is off until [Builder.Registration] enables it, and forgot and
// reset are off until [Builder.Reset] configures them; the other handlers need no
// switch.
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
//     ([auth.Guard.LockedOut]) a correct password is not refunded either, so
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
//   - A reset link is a value signed by a core link.Signer ([Builder.Reset]) for
//     the path "/password/reset" and the subject, bound to the user's current
//     stored password hash ([link.Bind]) and expiring after [Builder.ResetTTL].
//     Changing the password by any means invalidates every link sent before, so
//     no token is stored. [Verifier.Reset] checks the password policy first (a
//     weak password does not use up the link), verifies the signature against
//     the current hash, then stores the new hash with a conditional write in one
//     transaction: an update matching only the hash the link was signed for, or,
//     for a user without a password, an insert that does nothing on conflict.
//     Zero affected rows means the link was already used, so of two concurrent
//     submits exactly one wins, and ownership proof and rotation run only for
//     that one. Every invalid, forged, expired or used link is [ErrInvalidToken].
//   - The forgot handler calls the dispatch function of [Builder.Reset] with a
//     [ResetRequest] holding the identifier as typed, whether or not the user
//     exists, and answers an empty 202; nothing is looked up in the request. The
//     worker runs [Verifier.SendReset], which looks the user up and calls deliver
//     with a [ResetMessage] addressed to the address [Users.Lookup] returned,
//     never to the text of the request. deliver may run more than once for one
//     request, because its error makes the queue retry.
//
// # Storage
//
// The package never runs DDL. [Verifier.Migrations] describes the table
// "password_hashes" (prefixed "<instance>_" for a named
// [Builder.Instance]) in the group "arkoren.dev:ark/access:password", and the
// application's migrator executes them. Every package of the module assumes the
// same [database.Database]; writes that span packages join the transaction
// carried by the context.
//
// Example:
//
//	clientIP := ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8"))
//	verifier, err := password.NewVerifier(provider, db, store, clientIP, hasher, password.Users{
//		Lookup: directory.Lookup,
//		Create: directory.Create,
//		Rotate: directory.Rotate,
//	}, func(passwords *password.Builder) {
//		passwords.Registration(true).Reset(signer, dispatch, deliver)
//	})
//	if err != nil {
//		return err
//	}
//	guard, err := auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
//		policy.StepUp(verifier.Credential())
//	})
//	web.Post("/register", verifier.RegisterHandler())
//	web.Post("/login", verifier.LoginHandler(guard))
package password
