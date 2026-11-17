// Package twofactor adds a time-based one-time password (TOTP) authenticator
// and single-use recovery codes to any user store. It owns the secrets and
// the codes; it never sees how users are stored, only their subjects.
//
// # Model
//
// A user has at most one authenticator. Enrollment is two steps: [Factors.Enroll]
// stores a pending secret and returns it with an otpauth:// URI for a QR code,
// and [Factors.Activate] confirms it with the first code, which also creates the
// recovery codes and returns them once. Users pick any enrolled method at
// login; the guard lists methods in registration order, so register
// [Factors.Credential] (method "otp") before [Factors.RecoveryCredential]
// (method [MethodRecovery]).
//
// # Rules
//
//   - Secrets are encrypted with the application's [crypto.Encrypter]; use a key
//     reserved for them. A secret decrypted with a previous key is re-encrypted
//     with the current one on first use.
//   - A TOTP code is accepted at most once: the matched time step must be
//     greater than the last accepted one, advanced by an atomic conditional
//     update, so exactly one of two concurrent identical codes wins. The step
//     is spent before the guard completes the login, so if completing fails the
//     user waits for the next code.
//   - Recovery codes carry 80 random bits, are shown as xxxx-xxxx-xxxx-xxxx,
//     and are stored as SHA-256 hex. A code works once; the case and dashes the
//     user types do not matter.
//   - One [ErrInvalidCode] covers a wrong code, a replay, a spent code and a
//     user who is not enrolled. Attempts at login and confirmation are counted
//     by core; the wrong codes of [Factors.Activate] are counted here (10 per
//     15 minutes per user).
//   - Every credential-changing handler requires a browser session and a fresh
//     login ([auth.Guard.Fresh]); an API token can never enroll or remove a
//     factor. Enabling publishes [Enabled] and the core
//     [auth.CredentialEnrolled] event, and ends the user's other logins.
//   - Weakening two-factor needs more than freshness, which a password alone
//     satisfies: [Factors.DisableHandler] and [Factors.RecoveryCodesHandler]
//     require an authenticator or recovery code this session verified within
//     [Builder.StepUpWindow] and answer [auth.ErrStepUpRequired] with methods
//     ["otp"] otherwise. A recovery code in that window also counts as the fresh
//     login they ask for, so a user who lost the authenticator can log in with a
//     code and turn two-factor off.
//
// # Storage
//
// The package never runs DDL. [Factors.Migrations] describes the tables
// "totp_secrets" and "recovery_codes" (prefixed "<instance>_" for a named
// [Builder.Instance]) in the group "arkoren.dev:ark/access:twofactor", and the
// application's migrator executes them. Every package of the module assumes the
// same [database.Database]; writes that span packages join the transaction
// carried by the context.
//
// Example:
//
//	factors, err := twofactor.NewFactors(provider, db, store, totp.New("Acme"), cipher)
//	if err != nil {
//		return err
//	}
//	guard, err := auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
//		policy.SecondFactor(factors.Credential(), factors.RecoveryCredential())
//		policy.StepUp(factors.Credential())
//	})
//	web.Post("/two-factor/setup", factors.SetupHandler(guard))
package twofactor
