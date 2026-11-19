package federation_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/federation"
	"arkoren.dev/ark/iam/internal/apptest"
)

// verifiedIdentity is a first login whose email the provider proved.
var verifiedIdentity = auth.External{Issuer: google, Subject: "g9", Email: "new@example.com", EmailVerified: true}

// linksWithCreate returns a second store over the table of sys, with a Create
// function and sign-up left at its default (off).
func linksWithCreate(t *testing.T, sys *system, db *database.Database, instance string) *federation.Links {
	t.Helper()

	links, err := federation.NewLinks(sys.provider, db, func(links *federation.Builder) {
		links.Instance(instance).Create(sys.create)
	})
	if err != nil {
		t.Fatalf("NewLinks: %v", err)
	}

	return links
}

// TestResolverSignUpOverridesTheBuilder verifies a resolver can turn sign-up on
// over a store that has it off, while the store's own Resolve and a plain
// resolver keep refusing.
func TestResolverSignUpOverridesTheBuilder(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		links := linksWithCreate(t, newSystem(t, db, instance, false, false), db, instance)
		plain, err := links.Resolver()
		if err != nil {
			t.Fatalf("Resolver: %v", err)
		}
		open, err := links.Resolver(func(resolver *federation.ResolverBuilder) { resolver.SignUp(true) })
		if err != nil {
			t.Fatalf("Resolver: %v", err)
		}
		if _, err := links.Resolve(ctx, verifiedIdentity); !errors.Is(err, federation.ErrNoAccount) {
			t.Fatalf("Resolve = %v, want ErrNoAccount", err)
		}
		if _, err := plain(ctx, verifiedIdentity); !errors.Is(err, federation.ErrNoAccount) {
			t.Fatalf("plain resolver = %v, want ErrNoAccount", err)
		}
		if subject, err := open(ctx, verifiedIdentity); err != nil || subject == "" {
			t.Fatalf("resolver with sign-up = %q, %v, want a new user", subject, err)
		}
	})
}

// TestResolverSignUpCanTurnOff verifies a resolver can refuse sign-up over a
// store that has it on.
func TestResolverSignUpCanTurnOff(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, true, false)
		closed, err := sys.links.Resolver(func(resolver *federation.ResolverBuilder) { resolver.SignUp(false) })
		if err != nil {
			t.Fatalf("Resolver: %v", err)
		}
		if _, err := closed(testContext(t), verifiedIdentity); !errors.Is(err, federation.ErrNoAccount) {
			t.Fatalf("resolver without sign-up = %v, want ErrNoAccount", err)
		}
		if len(sys.created) != 0 {
			t.Fatalf("created %v, want nobody", sys.created)
		}
	})
}

// TestResolverSignUpKeepsTheEmailRule verifies a resolver with sign-up still
// refuses an identity whose email the provider did not prove.
func TestResolverSignUpKeepsTheEmailRule(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		links := linksWithCreate(t, newSystem(t, db, instance, false, false), db, instance)
		open, err := links.Resolver(func(resolver *federation.ResolverBuilder) { resolver.SignUp(true) })
		if err != nil {
			t.Fatalf("Resolver: %v", err)
		}
		unproven := verifiedIdentity
		unproven.EmailVerified = false
		if _, err := open(testContext(t), unproven); !errors.Is(err, federation.ErrEmailUnverified) {
			t.Fatalf("unproven email = %v, want ErrEmailUnverified", err)
		}
	})
}

// TestResolverSignUpWithoutCreateIsIgnored verifies sign-up with no Create
// function in the resolver or the store is ignored with a warning, so a first
// login finds no account, and that a function on the resolver alone is enough.
func TestResolverSignUpWithoutCreateIsIgnored(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, false, false)
		ignored, err := sys.links.Resolver(func(resolver *federation.ResolverBuilder) { resolver.SignUp(true) })
		if err != nil {
			t.Fatalf("Resolver without Create = %v, want the setting ignored", err)
		}
		if got := warnings.Settings(); !slices.Contains(got, "federation.ResolverBuilder.SignUp") {
			t.Fatalf("warnings = %v, want one naming federation.ResolverBuilder.SignUp", got)
		}
		if _, err := ignored(testContext(t), verifiedIdentity); !errors.Is(err, federation.ErrNoAccount) {
			t.Fatalf("first login through the ignored sign-up = %v, want ErrNoAccount", err)
		}
		if _, err := sys.links.Resolver(func(resolver *federation.ResolverBuilder) { resolver.SignUp(true).Create(sys.create) }); err != nil {
			t.Fatalf("Resolver with Create = %v, want nil", err)
		}
	})
}

// TestResolverCreateRunsBeforeTheBuilders verifies the resolver's Create
// functions run first, in order, and the store's runs for what they decline.
func TestResolverCreateRunsBeforeTheBuilders(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, false, false)
		var calls []string
		note := func(name string) func(context.Context, auth.External) (string, error) {
			return func(context.Context, auth.External) (string, error) {
				calls = append(calls, name)

				return "", nil
			}
		}
		links, err := federation.NewLinks(sys.provider, db, func(links *federation.Builder) {
			links.Instance(instance).Create(note("store")).Create(sys.create)
		})
		if err != nil {
			t.Fatalf("NewLinks: %v", err)
		}
		resolve, err := links.Resolver(func(resolver *federation.ResolverBuilder) {
			resolver.SignUp(true).Create(note("first")).Create(note("second"))
		})
		if err != nil {
			t.Fatalf("Resolver: %v", err)
		}
		if subject, err := resolve(testContext(t), verifiedIdentity); err != nil || subject == "" {
			t.Fatalf("resolve = %q, %v, want a new user", subject, err)
		}
		if got := strings.Join(calls, ","); got != "first,second,store" {
			t.Fatalf("Create order = %s, want first,second,store", got)
		}
	})
}
