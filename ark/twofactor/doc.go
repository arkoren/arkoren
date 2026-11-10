// Package twofactor adds TOTP two-factor authentication to a session login:
// enrollment with an authenticator app, a second step that completes a
// challenged login, single-use recovery codes, and cleanup of the data of
// deleted users.
//
// The ark never learns the application's user type. [New] takes the
// [auth.SessionIdentity] the application already uses and keeps only its
// [auth.Directory]. Every row is keyed by the user's subject, the text form of
// the identity's user ID (see [auth.SessionIdentity.Subject]), stored in a
// varchar(255) compared bytewise on PostgreSQL, MySQL and SQLite.
//
// # Tables
//
// The ark owns two tables, created by [TwoFactor.Migrations] and never by the
// ark itself: twofactor_secrets (the AES-encrypted TOTP key, the last accepted
// time step and the confirmation time) and twofactor_recovery_codes (one
// password hash per code and when it was spent). [Builder.Instance] prefixes
// both names and the migration group, so two realms can use separate tables.
// [TwoFactor.Ping] is a read-only readiness check that fails with
// [ErrNotFound], naming the migrations, when a table is missing.
//
// # Login flow
//
// After the first factor, ask [TwoFactor.Enrolled] whether the user has a
// second factor and call [auth.SessionIdentity.Challenge] or
// [auth.SessionIdentity.Login] accordingly. [TwoFactor.Verify] and
// [TwoFactor.Recover] then complete the pending login with [auth.MethodOTP] or
// [auth.MethodRecovery]. A TOTP code is accepted once: its time step must be
// later than the last accepted step, and the check is one conditional UPDATE,
// so concurrent submissions cannot both succeed (RFC 6238 section 5.2). Every
// client-caused failure is the same [ErrInvalidCode].
//
// Enrollment is [TwoFactor.Enroll] followed by [TwoFactor.Confirm], which
// returns the recovery codes once; [TwoFactor.Disable] removes both. Protect
// enrollment and removal with [auth.SessionIdentity.RequireFresh].
//
// # Deleting users
//
// Call [TwoFactor.Forget] in the transaction that deletes a user. It joins the
// transaction through the ark's database. [TwoFactor.Sweep] is a schedule entry
// that repairs a missed call: it forgets the subjects whose loader reports
// [auth.ErrNotFound] and keeps those with any other error, such as a suspended
// user. The ark keeps no external state, so deletion needs no deferred work.
//
// # Caveats
//
// The ark does not count failed attempts: rate-limit the verify and recover
// routes by session ID. It encrypts secrets with the supplied cipher, whose key
// must be kept stable, since losing it locks every enrolled user out. Recovery
// codes carry about 50 bits, so supply a deliberately slow hasher. Instance
// names must start with a letter or underscore and contain at most 30 bytes of
// [a-z0-9_].
//
// Example:
//
//	factors, err := twofactor.New(identity, db, cipher, hasher, func(ark *twofactor.Builder) {
//		ark.Issuer("Example")
//	})
//	if err != nil {
//		return err
//	}
//	if err := migrator.Add(factors.Migrations()...); err != nil {
//		return err
//	}
//	web.Post("/two-factor/verify", factors.Verify(identity))
package twofactor
