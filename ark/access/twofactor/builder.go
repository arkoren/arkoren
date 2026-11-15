package twofactor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/ark/access/internal/storage"
	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/foundation/totp"
)

// Builder configures [NewFactors] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	factors, err := twofactor.NewFactors(provider, db, store, cipher, "Example", func(factors *twofactor.Builder) {
//		factors.RecoveryCodes(8)
//	})
type Builder struct {
	instance string
	events   *event.Bus
	scheme   totp.Scheme
	codes    int
	window   time.Duration
	verified func(context.Context, string) (bool, error)
	enabled  func(context.Context, string) error
	extra    []auth.Credential
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure twofactor.BuilderFunc = func(factors *twofactor.Builder) {
//		factors.Instance("tenant_a")
//	}
//	factors, err := twofactor.NewFactors(provider, db, store, cipher, "Example", configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: tables are prefixed "<instance>_" and the
// migration group gains ":<instance>". The name is 1 to 20 bytes of [a-z0-9_]
// starting with a letter; the default is the empty instance.
//
// Example:
//
//	factors.Instance("tenant_a")
func (factors *Builder) Instance(name string) *Builder {
	factors.instance = name

	return factors
}

// Events publishes [Enabled], [Disabled], [Regenerated] and [RecoveryUsed], and
// the core [auth.CredentialEnrolled] event, on bus. A nil bus publishes nothing.
//
// Example:
//
//	factors.Events(events)
func (factors *Builder) Events(bus *event.Bus) *Builder {
	factors.events = bus

	return factors
}

// Scheme replaces the TOTP parameters (hash, digits, period, window). The
// default is [totp.New]: SHA-1, 6 digits, 30 seconds, one step of drift, which
// every authenticator app supports.
//
// Example:
//
//	factors.Scheme(totp.New(func(scheme *totp.Builder) {
//		scheme.Digits(8)
//	}))
func (factors *Builder) Scheme(scheme totp.Scheme) *Builder {
	factors.scheme = scheme

	return factors
}

// RecoveryCodes sets how many recovery codes enabling two-factor creates, 1 to
// 100; zero selects [DefaultRecoveryCodes].
//
// Example:
//
//	factors.RecoveryCodes(8)
func (factors *Builder) RecoveryCodes(count int) *Builder {
	factors.codes = count

	return factors
}

// StepUpWindow sets how recent the authenticator code (or recovery code) must
// be for [Factors.DisableHandler] and [Factors.RecoveryCodesHandler]; zero
// selects [DefaultStepUpWindow]. Core's freshness check also accepts a recent
// password, which is not enough to weaken two-factor, so these handlers look
// at the factors of the session themselves. The guard's own window is not
// readable, so when you change it with FreshFor, set the same value here.
//
// Example:
//
//	factors.StepUpWindow(5 * time.Minute)
func (factors *Builder) StepUpWindow(window time.Duration) *Builder {
	factors.window = window

	return factors
}

// Verified sets the check that tells whether a user's email is verified.
// [Factors.SetupHandler] and [Factors.EnableHandler] then answer 403 (the
// problem of account.ErrEmailUnverified) to an unverified user, so whoever
// registered an address first cannot enroll an authenticator before its owner
// proves the address. Without it the handlers admit everyone, which is only
// right when the application enforces the rule itself; the facade always sets
// it with the built-in users.
//
// Example:
//
//	factors.Verified(directory.Verified)
func (factors *Builder) Verified(check func(ctx context.Context, subject string) (bool, error)) *Builder {
	factors.verified = check

	return factors
}

// OnEnable sets a function that [Factors.EnableHandler] runs in the same
// transaction as the activation, after ending the user's other logins, for what
// must not outlive a new second factor (the facade revokes the user's device
// tokens). If it fails the transaction rolls back: two-factor stays off and no
// recovery code is shown.
//
// Example:
//
//	factors.OnEnable(issuer.RevokeDevices)
func (factors *Builder) OnEnable(hook func(ctx context.Context, subject string) error) *Builder {
	factors.enabled = hook

	return factors
}

// Methods registers second-factor credentials from other modules (a passkey
// package, say) whose recent proof also lets [Factors.DisableHandler] and
// [Factors.RecoveryCodesHandler] proceed, and which the 403 step-up answer
// offers when the user has them enrolled. Do not pass a password credential:
// a password is the one proof these handlers exist to look beyond.
//
// Example:
//
//	factors.Methods(passkeys.Credential())
func (factors *Builder) Methods(credentials ...auth.Credential) *Builder {
	factors.extra = append(factors.extra, credentials...)

	return factors
}

// validate checks the settings no setter can check alone.
func (factors *Builder) validate() error {
	if err := storage.ValidInstance(factors.instance); err != nil {
		return err
	}
	if err := factors.scheme.Err(); err != nil {
		return err
	}
	if factors.codes < 0 || factors.codes > maxRecoveryCodes {
		return fmt.Errorf("recovery codes must be 1 to %d", maxRecoveryCodes)
	}
	if factors.window < 0 {
		return errors.New("negative step-up window")
	}

	return nil
}
