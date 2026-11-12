package twofactor

import (
	"arkoren.dev/foundation/totp"
)

// defaultRecoveryCodes is how many recovery codes [TwoFactor.Confirm] issues
// when [Builder.RecoveryCodes] is not set.
const defaultRecoveryCodes = 10

// settings holds the optional construction settings that [New] validates and
// publishes into the [TwoFactor] value.
type settings struct {
	// instance prefixes table names and qualifies the migration group.
	instance string

	// issuer names the application in authenticator apps.
	issuer string

	// scheme is the TOTP scheme used to verify codes.
	scheme totp.Scheme

	// recoveryCodes is how many recovery codes confirmation issues.
	recoveryCodes int
}

// Builder is the construction draft that [New] passes to its callbacks. Each
// setting returns the same draft. Callbacks run directly once, in order, even
// when other inputs are invalid; nil entries panic and expanded nil or empty
// slices omit edits. [New] validates the completed draft and publishes
// detached settings, so retaining a draft cannot change a constructed value.
//
// Example:
//
//	factors, err := twofactor.New(identity, db, cipher, hasher, func(ark *twofactor.Builder) {
//		ark.Issuer("Example")
//		ark.RecoveryCodes(8)
//	})
//	if err != nil {
//		return err
//	}
//	_ = factors
type Builder struct {
	// config is the draft's private settings.
	config settings
}

// BuilderFunc edits one [New] construction draft. Policies run synchronously
// once in order; nil entries panic.
//
// Example:
//
//	var configure twofactor.BuilderFunc = func(ark *twofactor.Builder) {
//		ark.Issuer("Example")
//	}
//	factors, err := twofactor.New(identity, db, cipher, hasher, configure)
//	if err != nil {
//		return err
//	}
//	_ = factors
type BuilderFunc = func(*Builder)

// Issuer sets the service name that authenticator apps show beside the account
// and returns this draft. It is required: [New] fails with [ErrInvalidConfig]
// when it is empty or contains a colon, because the otpauth label uses the
// colon as its separator.
//
// Example:
//
//	ark.Issuer("Example")
func (ark *Builder) Issuer(issuer string) *Builder {
	ark.config.issuer = issuer

	return ark
}

// Scheme sets the TOTP scheme (algorithm, digits, period and verification
// window) and returns this draft. The default is the [totp.New] default:
// SHA-1, six digits, thirty seconds and a window of one step each side, which
// is what every common authenticator app supports. [New] fails with
// [ErrInvalidConfig] when the scheme reports an error.
//
// Example:
//
//	ark.Scheme(totp.New(func(scheme *totp.Builder) {
//		scheme.Window(0)
//	}))
func (ark *Builder) Scheme(scheme totp.Scheme) *Builder {
	ark.config.scheme = scheme

	return ark
}

// RecoveryCodes sets how many single-use recovery codes confirmation issues
// and returns this draft. Zero selects the default of ten; a negative number or
// one above one hundred makes [New] fail with [ErrInvalidConfig].
//
// Example:
//
//	ark.RecoveryCodes(8)
func (ark *Builder) RecoveryCodes(count int) *Builder {
	ark.config.recoveryCodes = count

	return ark
}

// Instance sets the instance identity and returns this draft. Empty selects the
// default tables and migration group. Otherwise [New] requires at most 30 bytes
// of [a-z0-9_] that do not start with a digit, because the table names derive
// from it (<instance>_twofactor_secrets) and the query builder rejects
// identifiers that start with a digit. Two instances keep separate tables, so
// two realms can share one ark module without sharing subjects; two instances
// without distinct names collide when their migrations are added to a migrator.
// Renaming an existing instance is unsupported because it changes table names.
//
// Example:
//
//	ark.Instance("admin")
func (ark *Builder) Instance(instance string) *Builder {
	ark.config.instance = instance

	return ark
}
