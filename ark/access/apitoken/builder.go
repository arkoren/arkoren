package apitoken

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"arkoren.dev/core/event"

	"arkoren.dev/ark/access/internal/storage"
)

// prefixPattern accepts the token prefixes: 2 to 16 lowercase letters or digits.
var prefixPattern = regexp.MustCompile(`^[a-z0-9]{2,16}$`)

// Builder configures [NewIssuer] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	issuer, err := apitoken.NewIssuer(provider, db, func(tokens *apitoken.Builder) {
//		tokens.Scopes("posts:read", "posts:write").MaxTTL(90 * 24 * time.Hour)
//	})
type Builder struct {
	instance string
	events   *event.Bus
	scopes   []string
	prefix   string
	maxTTL   time.Duration

	// deviceIdleTTL is [Builder.DeviceIdleTTL] before its default is applied.
	deviceIdleTTL time.Duration
	verified      func(context.Context, string) (bool, error)
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure apitoken.BuilderFunc = func(tokens *apitoken.Builder) {
//		tokens.Scopes("posts:read")
//	}
//	issuer, err := apitoken.NewIssuer(provider, db, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: the table is prefixed "<instance>_" and the
// migration group gains ":<instance>". The name is 1 to 20 bytes of [a-z0-9_]
// starting with a letter; the default is the empty instance.
//
// Example:
//
//	tokens.Instance("tenant_a")
func (tokens *Builder) Instance(name string) *Builder {
	tokens.instance = name

	return tokens
}

// Events publishes [Issued] and [Revoked] on bus. A nil bus publishes nothing.
//
// Example:
//
//	tokens.Events(events)
func (tokens *Builder) Events(bus *event.Bus) *Builder {
	tokens.events = bus

	return tokens
}

// Scopes declares the catalog of scopes the application knows. Personal tokens
// may carry any subset; device tokens carry all of them. Without a catalog
// tokens carry no scopes. The names are copied.
//
// Example:
//
//	tokens.Scopes("posts:read", "posts:write")
func (tokens *Builder) Scopes(scopes ...string) *Builder {
	tokens.scopes = slices.Clone(scopes)

	return tokens
}

// Prefix replaces the token prefix, "ark" by default: 2 to 16 lowercase letters
// or digits. Changing it invalidates no stored token, but tokens issued under
// the old prefix are no longer recognized.
//
// Example:
//
//	tokens.Prefix("acme")
func (tokens *Builder) Prefix(prefix string) *Builder {
	tokens.prefix = prefix

	return tokens
}

// MaxTTL caps the lifetime of tokens: a longer expiry of a personal token is
// refused, and a personal or device token without one expires after ttl. Zero
// (the default) means no cap and applies to neither: a device token is then
// limited only by [Builder.DeviceIdleTTL] and by revocation at logout.
//
// Example:
//
//	tokens.MaxTTL(90 * 24 * time.Hour)
func (tokens *Builder) MaxTTL(ttl time.Duration) *Builder {
	tokens.maxTTL = ttl

	return tokens
}

// DeviceIdleTTL sets how long a device token may go unused before it stops
// working: its last use (or its creation, when it was never used) plus ttl is
// the deadline, and every use moves it. Last use is written at most once an hour,
// so the deadline can be early by up to an hour. The default is
// [DefaultDeviceIdleTTL], 90 days; zero selects it, and a negative value turns
// the limit off. A device token that is only revoked at logout otherwise lives
// forever on a phone nobody uses. It does not apply to personal tokens, which
// have [Builder.MaxTTL] and their own expiry.
//
// Example:
//
//	tokens.DeviceIdleTTL(30 * 24 * time.Hour)
func (tokens *Builder) DeviceIdleTTL(ttl time.Duration) *Builder {
	tokens.deviceIdleTTL = ttl

	return tokens
}

// deviceIdle returns the idle limit with its default applied: zero for no
// limit.
func (tokens *Builder) deviceIdle() time.Duration {
	switch {
	case tokens.deviceIdleTTL < 0:
		return 0
	case tokens.deviceIdleTTL == 0:
		return DefaultDeviceIdleTTL
	}

	return tokens.deviceIdleTTL
}

// Verified sets the check that tells whether a user's email is verified.
// [Issuer.CreateHandler] and [Issuer.ExchangeHandler] then answer 403 (the
// problem of account.ErrEmailUnverified) to an unverified user, so whoever
// registered an address first cannot mint a credential before its owner proves
// the address. Without it the handlers admit everyone, which is only right when
// the application enforces the rule itself; the facade always sets it with the
// built-in users. [Issuer.Issue] and [Issuer.IssueDevice] are not gated.
//
// Example:
//
//	tokens.Verified(directory.Verified)
func (tokens *Builder) Verified(check func(ctx context.Context, subject string) (bool, error)) *Builder {
	tokens.verified = check

	return tokens
}

// validate checks the settings no setter can check alone.
func (tokens *Builder) validate() error {
	if err := storage.ValidInstance(tokens.instance); err != nil {
		return err
	}
	if !prefixPattern.MatchString(tokens.prefix) {
		return fmt.Errorf("prefix %q must be 2 to 16 lowercase letters or digits", tokens.prefix)
	}
	if tokens.maxTTL < 0 {
		return errors.New("negative maximum lifetime")
	}
	catalog := slices.Sorted(slices.Values(tokens.scopes))
	if slices.Contains(catalog, "") || len(slices.Compact(catalog)) != len(tokens.scopes) {
		return errors.New("scopes must be unique and non-empty")
	}

	return nil
}
