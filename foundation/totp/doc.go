// Package totp generates and verifies time-based one-time passwords as defined
// by RFC 6238, using only the standard library.
//
// A [Scheme] is an immutable set of parameters (hash algorithm, digit count,
// period and verification window) built once with [New]. Its methods take the
// shared secret and the current time as arguments: the package has no clock
// option and no storage, so callers control time in tests and own persistence
// of secrets and of the last accepted time step.
//
// Verification returns the matched time step. A code stays valid for the whole
// period plus the window, so a verifier that must reject replays stores the
// highest step it accepted per user and refuses any step less than or equal to
// it. The package cannot do that itself because it keeps no state.
//
// The window is limited to one step on each side of the current step, which
// tolerates clock drift without turning a six-digit code into a long-lived
// credential. Callers should also limit failed attempts; six digits can be
// guessed quickly without rate limiting.
//
// Example:
//
//	scheme := totp.New()
//	secret, err := scheme.NewSecret()
//	if err != nil {
//		return err
//	}
//	uri, err := scheme.URI("Example", "alice@example.com", secret)
//	if err != nil {
//		return err
//	}
//	// Show uri as a QR code, then on login:
//	step, err := scheme.Verify(secret, submitted, time.Now())
//	if errors.Is(err, totp.ErrInvalidCode) {
//		// Wrong or expired code.
//	}
//	_ = step // Persist it and reject any later code with step <= the stored value.
package totp
