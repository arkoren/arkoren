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
	"arkoren.dev/ark/access/internal/web"
)

// prefixPattern accepts the token prefixes: 2 to 16 lowercase letters or digits.
var prefixPattern = regexp.MustCompile(`^[a-z0-9]{2,16}$`)

// Builder configures [NewIssuer] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	issuer, err := apitoken.NewIssuer(provider, db, store, func(tokens *apitoken.Builder) {
//		tokens.Grants(apitoken.AnyScope, "posts:read", "posts:write").MaxTTL(90 * 24 * time.Hour)
//	})
type Builder struct {
	instance  string
	events    *event.Bus
	grantable func(context.Context, string) ([]string, error)
	scopes    []string
	grantTTL  time.Duration
	prefix    string
	maxTTL    time.Duration

	// personal and device are [Builder.PersonalTokens] and
	// [Builder.DeviceTokens]; both start true.
	personal bool
	device   bool

	// deviceIdleTTL is [Builder.DeviceIdleTTL] before its default is applied.
	deviceIdleTTL time.Duration
	verified      func(context.Context, string) (bool, error)
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure apitoken.BuilderFunc = func(tokens *apitoken.Builder) {
//		tokens.MaxTTL(time.Hour)
//	}
//	issuer, err := apitoken.NewIssuer(provider, db, store, configure)
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

// Grants declares the catalog of scopes the application knows and which of them
// each user may hold. scopes is the catalog; grantable returns, for a subject,
// the catalog scopes that user may hold now: a personal token may carry any
// subset of catalog and grant, and a device token carries all of it. Names in
// grantable's answer outside the catalog are ignored, so it may return the
// user's whole permission set; the one exception is the private marker that
// [AnyScope] returns, which stands for the whole catalog, so a literal "*" in
// the answer is only a scope named "*". Use [AnyScope] when every user may hold
// every scope. grantable is required with a catalog, which is why both are arguments
// of one method: a catalog with no ceiling would let a user mint a token that
// can do more than they can. Without Grants, tokens carry no scopes. The names
// are copied; an empty or duplicate name is
// [ErrInvalidConfig], as is a nil grantable with a catalog.
//
// The grant is a ceiling checked at issue (a scope outside it is
// [ErrScopeNotGranted]) and again when the token authenticates, so a user who
// loses a permission loses the matching scope of tokens already issued; see
// [Builder.GrantTTL] for how often that check runs. A change of the user's
// UserRecord.Version (a password reset, the application's own rotation)
// ends every token whatever the mode.
//
// Example:
//
//	tokens.Grants(func(ctx context.Context, subject string) ([]string, error) {
//		if isAdmin(ctx, subject) {
//			return []string{"posts:read", "posts:write", "users:write"}, nil
//		}
//		return []string{"posts:read", "posts:write"}, nil
//	}, "posts:read", "posts:write", "users:write")
func (tokens *Builder) Grants(grantable func(ctx context.Context, subject string) ([]string, error), scopes ...string) *Builder {
	tokens.grantable = grantable
	tokens.scopes = slices.Clone(scopes)

	return tokens
}

// GrantTTL sets how often an authenticating token re-checks the user's grant
// ([Builder.Grants]):
//
//   - zero (the default) calls the grant function on every request that
//     authenticates with a token, so a change applies at once, at the cost of
//     one call per request;
//   - a positive ttl caches the answer per subject for ttl in the shared store,
//     so a change applies within ttl and the function runs at most about once
//     per ttl per user (concurrent misses are coalesced);
//   - a negative value fixes the scopes at creation: the token keeps what it was
//     issued with and the function is not called again. A demoted user keeps
//     their tokens' scopes until they are revoked.
//
// Whatever the mode, a token ends when the user's UserRecord.Version
// changes (a password reset, the application's own rotation), and an error from
// the grant function or the store fails the request instead of widening the
// scopes. It has no effect without a catalog.
//
// Example:
//
//	tokens.GrantTTL(time.Minute)
func (tokens *Builder) GrantTTL(ttl time.Duration) *Builder {
	tokens.grantTTL = ttl

	return tokens
}

// PersonalTokens enables the personal access tokens users create for
// themselves ([Issuer.CreateHandler]); the default is true. With false,
// [Issuer.CreateHandler] panics at composition and the facade mounts no
// POST /tokens. [Issuer.Issue] stays available to the application.
//
// Example:
//
//	tokens.PersonalTokens(false) // device tokens only
func (tokens *Builder) PersonalTokens(enabled bool) *Builder {
	tokens.personal = enabled

	return tokens
}

// DeviceTokens enables the device tokens a first-party app gets for a login
// ([Issuer.ExchangeHandler], [Issuer.DeleteCurrentHandler]); the default is
// true. With false both panic at composition and the facade mounts neither
// POST /device-tokens nor DELETE /device-tokens/current. [Issuer.IssueDevice]
// stays available to the application.
//
// Example:
//
//	tokens.DeviceTokens(false) // personal tokens only
func (tokens *Builder) DeviceTokens(enabled bool) *Builder {
	tokens.device = enabled

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
// have [Builder.MaxTTL] and their own expiry. A lapsed token is rejected whether
// or not its row still exists; [Issuer.Prune] deletes the rows, and a negative
// ttl makes it skip them.
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

// Verified adds the check that tells whether a user's email is verified.
// [Issuer.CreateHandler] and [Issuer.ExchangeHandler] then answer 403 (the
// problem of account.ErrEmailUnverified) to an unverified user, so whoever
// registered an address first cannot mint a credential before its owner proves
// the address. Without it the handlers admit everyone, which is only right when
// the application enforces the rule itself; the facade always sets it with the
// built-in users. [Issuer.Issue] and [Issuer.IssueDevice] are not gated. Checks
// add up instead of replacing each other: a user is verified only when every
// check says so, so an application check can add a rule but never lift the
// facade's.
//
// Example:
//
//	tokens.Verified(directory.Verified)
func (tokens *Builder) Verified(check func(ctx context.Context, subject string) (bool, error)) *Builder {
	tokens.verified = web.All(tokens.verified, check)

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
	if !tokens.personal && !tokens.device {
		return errors.New("tokens enabled with no token kind: enable personal or device tokens")
	}

	return tokens.validateGrants()
}

// validateGrants checks the scope catalog and its grant function: names are
// unique and non-empty, and a catalog comes with the function that caps it.
func (tokens *Builder) validateGrants() error {
	catalog := slices.Sorted(slices.Values(tokens.scopes))
	if slices.Contains(catalog, "") || slices.Contains(catalog, anyScope) || len(slices.Compact(catalog)) != len(tokens.scopes) {
		return errors.New("scopes must be unique and non-empty")
	}
	if tokens.grantable == nil && len(catalog) > 0 {
		return errors.New("a scope catalog needs a grant function: pass AnyScope to grant every scope")
	}

	return nil
}
