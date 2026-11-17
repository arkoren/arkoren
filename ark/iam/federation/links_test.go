package federation_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/federation"
)

// TestLookupNeverCreates verifies Lookup is read-only.
func TestLookupNeverCreates(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, true, false)
		if _, err := sys.links.Lookup(testContext(t), google, "g1"); !errors.Is(err, federation.ErrNotLinked) {
			t.Fatalf("Lookup error = %v", err)
		}
		if len(sys.created) != 0 {
			t.Fatalf("Lookup created users: %v", sys.created)
		}
	})
}

// TestOneConnectionPerIssuer verifies a user cannot hold two identities of one
// issuer.
func TestOneConnectionPerIssuer(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, true)
		if err := sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		if err := sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"}); err != nil {
			t.Fatalf("idempotent Link: %v", err)
		}
		if err := sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g2"}); !errors.Is(err, federation.ErrAlreadyLinked) {
			t.Fatalf("second identity error = %v, want ErrAlreadyLinked", err)
		}
		if err := sys.links.Link(ctx, "2", auth.External{Issuer: google, Subject: "g1"}); !errors.Is(err, federation.ErrLinkedElsewhere) {
			t.Fatalf("stolen identity error = %v, want ErrLinkedElsewhere", err)
		}
	})
}

// TestDeleteLastLink verifies the last connection needs a fallback.
func TestDeleteLastLink(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		for _, hasFallback := range []bool{false, true} {
			sys := newSystem(t, db, instance+map[bool]string{false: "_n", true: "_y"}[hasFallback], false, hasFallback)
			_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
			listed, _ := sys.links.List(ctx, "1")
			err := sys.links.Unlink(ctx, "1", listed[0].ID)
			if hasFallback && err != nil {
				t.Errorf("Unlink with a fallback: %v", err)
			}
			if !hasFallback && !errors.Is(err, federation.ErrLastLink) {
				t.Errorf("Unlink without a fallback error = %v, want ErrLastLink", err)
			}
		}
	})
}

// TestConcurrentUnlinkKeepsOneWayIn verifies two concurrent unlinks of a
// user's two connections, with no other way to log in, cannot both succeed:
// each would see the other still present if the rows were not locked.
func TestConcurrentUnlinkKeepsOneWayIn(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, false)
		for round := range 10 {
			subject := "r" + strconv.Itoa(round)
			_ = sys.links.Link(ctx, subject, auth.External{Issuer: google, Subject: "g" + subject})
			_ = sys.links.Link(ctx, subject, auth.External{Issuer: "https://github.com", Subject: "h" + subject})
			listed, _ := sys.links.List(ctx, subject)
			errs := make([]error, len(listed))
			var wg sync.WaitGroup
			for i, link := range listed {
				wg.Go(func() {
					errs[i] = sys.links.Unlink(ctx, subject, link.ID)
				})
			}
			wg.Wait()
			succeeded := 0
			for _, err := range errs {
				if err == nil {
					succeeded++
				} else if !errors.Is(err, federation.ErrLastLink) {
					t.Fatalf("round %d: Unlink error = %v", round, err)
				}
			}
			if remaining, _ := sys.links.List(ctx, subject); succeeded != 1 || len(remaining) != 1 {
				t.Fatalf("round %d: %d unlinks succeeded and %d connections remain, want 1 and 1", round, succeeded, len(remaining))
			}
		}
	})
}

// TestUnlinkAllRemovesEveryConnection verifies the purge a reset uses.
func TestUnlinkAllRemovesEveryConnection(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, false)
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: "https://github.com", Subject: "h1"})
		_ = sys.links.Link(ctx, "2", auth.External{Issuer: google, Subject: "g2"})
		if err := sys.links.UnlinkAll(ctx, "1"); err != nil {
			t.Fatalf("UnlinkAll: %v", err)
		}
		if err := sys.links.UnlinkAll(ctx, "1"); err != nil {
			t.Fatalf("second UnlinkAll: %v", err)
		}
		if listed, _ := sys.links.List(ctx, "1"); len(listed) != 0 {
			t.Fatalf("connections = %+v, want none", listed)
		}
		if listed, _ := sys.links.List(ctx, "2"); len(listed) != 1 {
			t.Fatalf("another user's connections = %+v, want one", listed)
		}
	})
}

// TestDeleteWithTwoLinksKeepsOne verifies a user with two connections can drop
// one with no fallback.
func TestDeleteWithTwoLinksKeepsOne(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, false)
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: "https://github.com", Subject: "h1"})
		listed, _ := sys.links.List(ctx, "1")
		if err := sys.links.Unlink(ctx, "1", listed[0].ID); err != nil {
			t.Fatalf("Unlink: %v", err)
		}
		if err := sys.links.Unlink(ctx, "1", listed[0].ID); !errors.Is(err, federation.ErrNotFound) {
			t.Fatalf("second Unlink error = %v, want ErrNotFound", err)
		}
		if err := sys.links.Unlink(ctx, "2", listed[1].ID); !errors.Is(err, federation.ErrNotFound) {
			t.Fatalf("another user's Unlink error = %v, want ErrNotFound", err)
		}
	})
}

// TestForgetHookDeletesLinks verifies forgetting a user removes the links.
func TestForgetHookDeletesLinks(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, true)
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		errRollback := errors.New("roll back")
		err := db.WithTransaction(ctx, func(ctx context.Context) error {
			if err := sys.provider.Forget(ctx, "1"); err != nil {
				return err
			}

			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("transaction error = %v", err)
		}
		if _, err := sys.links.Lookup(ctx, google, "g1"); err != nil {
			t.Fatalf("a rolled-back forget deleted the link: %v", err)
		}
		if err := sys.provider.Forget(ctx, "1"); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if _, err := sys.links.Lookup(ctx, google, "g1"); !errors.Is(err, federation.ErrNotLinked) {
			t.Fatalf("Lookup after Forget error = %v", err)
		}
	})
}

// TestTwoInstancesSeparateTables verifies two instances keep separate links.
func TestTwoInstancesSeparateTables(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		first := newSystem(t, db, instance+"_a", false, true)
		second := newSystem(t, db, instance+"_b", false, true)
		_ = first.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		if _, err := second.links.Lookup(ctx, google, "g1"); !errors.Is(err, federation.ErrNotLinked) {
			t.Fatalf("the second instance sees the first's link: %v", err)
		}
	})
}

// TestPingNamesMigrations verifies Ping reports a missing table.
func TestPingNamesMigrations(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance+"_x", false, true)
		links, err := federation.NewLinks(sys.provider, db, func(links *federation.Builder) {
			links.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewLinks: %v", err)
		}
		if err := links.Ping(ctx); !errors.Is(err, federation.ErrNotMigrated) {
			t.Fatalf("Ping error = %v, want ErrNotMigrated", err)
		}
		migrateUp(t, db, links.Migrations()...)
		if err := links.Ping(ctx); err != nil {
			t.Fatalf("Ping after migrations: %v", err)
		}
	})
}

// TestNewLinksRejectsBadConfig verifies the configuration errors.
func TestNewLinksRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := federation.NewLinks(nil, nil); !errors.Is(err, federation.ErrInvalidConfig) {
		t.Fatalf("nil dependencies error = %v", err)
	}
}
