package federation

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"arkoren.dev/core/auth"

	"arkoren.dev/ark/iam/internal/web"
)

// ResolverBuilder configures one [auth.Resolver] made by [Links.Resolver]. It
// holds the settings that may differ between providers sharing one connection
// store: whether a first login may create a user, and the application's rule
// for creating it. A draft starts from the sign-up setting of the [Builder] of
// the [Links]; callbacks run directly once, in order, on one fresh draft, and
// methods return the same draft.
//
// Example:
//
//	resolve, err := links.Resolver(func(resolver *federation.ResolverBuilder) {
//		resolver.SignUp(true).Create(createWorkUser)
//	})
type ResolverBuilder struct {
	signUp bool
	create func(context.Context, auth.External) (string, error)
	keep   []string
}

// ResolverBuilderFunc is the callback form of [ResolverBuilder].
//
// Example:
//
//	var configure federation.ResolverBuilderFunc = func(resolver *federation.ResolverBuilder) {
//		resolver.SignUp(true)
//	}
//	resolve, err := links.Resolver(configure)
type ResolverBuilderFunc = func(*ResolverBuilder)

// SignUp sets whether a first logged-out login with an unknown external
// identity creates a user for this resolver, as [Builder.SignUp] does for
// [Links.Resolve]. It replaces the setting the draft started from. With true
// the identity must carry an email the provider proved, or the login fails with
// [ErrEmailUnverified]; with false it fails with [ErrNoAccount]. The takeover
// of an unverified user ([Builder.TakeOver]) is not affected by it.
//
// Example:
//
//	resolver.SignUp(true)
func (resolver *ResolverBuilder) SignUp(enabled bool) *ResolverBuilder {
	resolver.signUp = enabled

	return resolver
}

// Create adds the function that makes a local user for an unknown identity of
// this resolver, when sign-up is enabled. It runs before the function of
// [Builder.Create], and functions add up: they run in the order added until one
// returns a subject, and one that leaves the identity to the next returns ""
// and no error. See [Builder.Create] for the rules the function follows.
//
// Example:
//
//	resolver.Create(func(ctx context.Context, external auth.External) (string, error) {
//		return createWorkUser(ctx, external.Email)
//	})
func (resolver *ResolverBuilder) Create(create func(ctx context.Context, external auth.External) (string, error)) *ResolverBuilder {
	resolver.create = web.First(resolver.create, create)

	return resolver
}

// KeepClaims names claims of this provider's verified identity that its
// connections store, so the application can prefill a profile form from
// [Links.List] (a name from Google, a login from GitHub). Nothing is kept by
// default and calls add up. The claims are the provider's own suggestions: they
// are unverified, never a basis for authorization, identity or matching users,
// and each login can change them.
//
// A value is stored only when it is a JSON string, boolean or number, or a
// list of strings; anything else is dropped with one warning per claim name.
// Each login overwrites the claims it carries and keeps the rest, so a provider
// that sends a claim only once (Apple's name, on the first login) does not lose
// it, and a claim the provider stops sending stays until the connection is
// removed. The stored JSON is capped at 8 KiB: an update beyond it is skipped
// with a warning. They are stored at sign-up, when a logged-in user connects the
// provider, and on every login (a failing write never fails the login), and
// deleted with the connection (unlink, [Links.UnlinkAll] and the forget hook).
//
// An empty name or a protocol claim (iss, sub, aud, exp, iat, nbf, nonce,
// at_hash, auth_time, scope, azp, acr, amr) makes [Links.Resolver] fail with
// [ErrInvalidConfig]. The connection store keeps nothing for [Links.Resolve] and
// [Links.Link], which take no such setting.
//
// Example:
//
//	resolver.KeepClaims("name", "picture")
func (resolver *ResolverBuilder) KeepClaims(names ...string) *ResolverBuilder {
	resolver.keep = append(resolver.keep, names...)

	return resolver
}

// Resolver returns an [auth.Resolver] that works like [Links.Resolve] over the
// same connections, takeover, fallback and events, with the sign-up settings of
// configure: use it to let one provider create users and another not. A
// resolver starts from the sign-up setting of the [Builder] given to
// [NewLinks], and its [ResolverBuilder.Create] functions run before the
// [Builder.Create] ones. With sign-up on and no Create function in either
// place nothing could create the user, so the setting is ignored: Resolver
// logs one warning at construction and the resolver behaves as if sign-up were
// off, a first login failing with [ErrNoAccount]. Invalid [ResolverBuilder.KeepClaims]
// names make it fail with [ErrInvalidConfig]. The resolver is safe for
// concurrent use and keeps no reference to the draft.
//
// Example:
//
//	resolve, err := links.Resolver(func(resolver *federation.ResolverBuilder) {
//		resolver.SignUp(true)
//	})
//	if err != nil {
//		return err
//	}
//	web.Get("/auth/work/callback", work.CallbackHandler(guard, resolve))
func (links *Links) Resolver(configure ...ResolverBuilderFunc) (auth.Resolver, error) {
	settings := ResolverBuilder{signUp: links.onboard.signUp}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := validateKeep(settings.keep); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	onboard := onboarding{signUp: settings.signUp, create: web.First(settings.create, links.onboard.create), keep: slices.Clone(settings.keep)}
	if onboard.signUp && onboard.create == nil {
		slog.WarnContext(context.Background(), "access: setting ignored", "setting", "federation.ResolverBuilder.SignUp", "reason", "no Create function is set, so no user can be created")
		onboard.signUp = false
	}

	return func(ctx context.Context, external auth.External) (string, error) {
		return links.resolve(ctx, external, onboard)
	}, nil
}
