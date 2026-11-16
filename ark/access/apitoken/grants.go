package apitoken

import (
	"context"
	"fmt"
	"slices"
	"time"

	"arkoren.dev/core/service/cache"
)

// anyScope is the private marker [AnyScope] returns to say "every scope of the
// catalog". It is not a scope name anyone would write: it starts with a NUL
// byte, which no scope in a header or a JSON body is expected to carry, and the
// catalog may not contain it. A grant function that returns the literal "*"
// therefore grants only a scope named "*", not the whole catalog.
const anyScope = "\x00arkoren:any-scope"

// AnyScope is a grant function for [Builder.Grants] that lets every user hold
// every scope of the catalog: it answers a private marker, which the [Issuer]
// expands to the whole catalog. No other answer, "*" included, has that effect. Use it only when the scopes never grant more
// than the user has anyway, for example scopes that merely narrow what the
// user's own permissions allow; otherwise a token holds powers its owner
// lacks, and a demoted user keeps them. The grant is then not a ceiling but
// only the catalog.
//
// Example:
//
//	tokens.Grants(apitoken.AnyScope, "posts:read", "posts:write")
func AnyScope(context.Context, string) ([]string, error) {
	return []string{anyScope}, nil
}

// grantsNamespace returns the cache namespace of the cached grants of one
// instance, so two instances sharing a store never read each other's answers.
func grantsNamespace(instance string, ttl time.Duration) cache.Namespace[[]string] {
	return cache.NewNamespace[[]string]("access:"+instance+":apitoken:grants", func(namespace *cache.NamespaceBuilder) {
		namespace.TTL(ttl)
	})
}

// within returns the scopes of limit that allowed also lists, in limit's order;
// the [AnyScope] marker in allowed lists every scope of limit. It returns a new slice
// and mutates neither argument.
func within(limit, allowed []string) []string {
	if slices.Contains(allowed, anyScope) {
		return slices.Clone(limit)
	}

	return slices.DeleteFunc(slices.Clone(limit), func(scope string) bool {
		return !slices.Contains(allowed, scope)
	})
}

// granted returns the catalog scopes subject may hold now, asking the grant
// function directly. It returns nil, and calls nothing, without a catalog.
func (issuer *Issuer) granted(ctx context.Context, subject string) ([]string, error) {
	if issuer.grantable == nil {
		return nil, nil
	}
	allowed, err := issuer.grantable(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("apitoken: grant scopes: %w", err)
	}

	return within(issuer.catalog, allowed), nil
}

// effective returns the scopes a token authenticates with: the scopes stored at
// issue, narrowed to what subject may hold now. A fixed mode
// ([Builder.GrantTTL] negative) and an issuer without a catalog use the stored
// scopes as they are. With a positive TTL the grant function's answer comes
// from the shared store, computed at most once per TTL per subject. An error
// from the function or the store is returned and never widens the scopes: the
// request fails.
func (issuer *Issuer) effective(ctx context.Context, subject string, stored []string) ([]string, error) {
	if issuer.grantable == nil || issuer.grantTTL < 0 {
		return stored, nil
	}
	if issuer.grantTTL == 0 {
		now, err := issuer.granted(ctx, subject)
		if err != nil {
			return nil, err
		}

		return within(stored, now), nil
	}
	allowed, err := issuer.store.Remember(ctx, issuer.grants.Key(subject), func() ([]string, error) {
		return issuer.grantable(ctx, subject)
	})
	if err != nil {
		return nil, fmt.Errorf("apitoken: grant scopes: %w", err)
	}

	return within(stored, within(issuer.catalog, allowed)), nil
}
