package federation

import (
	"context"
	"fmt"

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

// Resolver returns an [auth.Resolver] that works like [Links.Resolve] over the
// same connections, takeover, fallback and events, with the sign-up settings of
// configure: use it to let one provider create users and another not. A
// resolver starts from the sign-up setting of the [Builder] given to
// [NewLinks], and its [ResolverBuilder.Create] functions run before the
// [Builder.Create] ones. With sign-up on and no Create function in either
// place it returns an error wrapping [ErrInvalidConfig], so a misconfigured
// provider fails at construction and never at its first login. The resolver is
// safe for concurrent use and keeps no reference to the draft.
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
	onboard := onboarding{signUp: settings.signUp, create: web.First(settings.create, links.onboard.create)}
	if onboard.signUp && onboard.create == nil {
		return nil, fmt.Errorf("%w: SignUp needs a Create function", ErrInvalidConfig)
	}

	return func(ctx context.Context, external auth.External) (string, error) {
		return links.resolve(ctx, external, onboard)
	}, nil
}
