package permission_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/iam/permission"
)

// TestGrantIsIdempotent verifies granting twice stores one row and publishes one
// event, for both kinds.
func TestGrantIsIdempotent(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newFixture(t, db, instance)
		for range 2 {
			if err := sys.store.Grant(ctx, "1", billingExport); err != nil {
				t.Fatalf("Grant: %v", err)
			}
			if err := sys.store.GrantRole(ctx, "1", editor); err != nil {
				t.Fatalf("GrantRole: %v", err)
			}
		}
		assigned, err := sys.store.Assignments(ctx, "1")
		if err != nil || !slices.Equal(assigned.Roles, []string{"editor"}) || !slices.Equal(assigned.Permissions, []string{"billing:export"}) {
			t.Fatalf("assignments = %+v, %v", assigned, err)
		}
		want := []string{"permission.grant.created 1 permission billing:export", "permission.grant.created 1 role editor"}
		if events := sys.events.all(); !slices.Equal(events, want) {
			t.Fatalf("events = %v", events)
		}
	})
}

// TestRevokeIsIdempotent verifies revoking twice deletes once and publishes one
// event, for both kinds, and revoking what was never granted does nothing.
func TestRevokeIsIdempotent(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newFixture(t, db, instance)
		_ = sys.store.Grant(ctx, "1", billingExport)
		_ = sys.store.GrantRole(ctx, "1", editor)
		for range 2 {
			if err := sys.store.Revoke(ctx, "1", billingExport); err != nil {
				t.Fatalf("Revoke: %v", err)
			}
			if err := sys.store.RevokeRole(ctx, "1", editor); err != nil {
				t.Fatalf("RevokeRole: %v", err)
			}
		}
		if err := sys.store.RevokeRole(ctx, "2", viewer); err != nil {
			t.Fatalf("RevokeRole never granted: %v", err)
		}
		revoked := slices.DeleteFunc(sys.events.all(), func(event string) bool {
			return !slices.Contains([]string{"permission.grant.revoked 1 permission billing:export", "permission.grant.revoked 1 role editor"}, event)
		})
		if len(revoked) != 2 {
			t.Fatalf("events = %v", sys.events.all())
		}
	})
}

// TestGrantRejectsUndefinedValues verifies a permission or role the store does
// not define, including a different role with a defined name, is
// ErrUnknownGrant.
func TestGrantRejectsUndefinedValues(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "")
	if err := sys.store.Grant(ctx, "1", unknownThing); !errors.Is(err, permission.ErrUnknownGrant) {
		t.Fatalf("Grant = %v", err)
	}
	if err := sys.store.GrantRole(ctx, "1", impostor); !errors.Is(err, permission.ErrUnknownGrant) {
		t.Fatalf("GrantRole = %v", err)
	}
	if err := sys.store.Revoke(ctx, "1", unknownThing); !errors.Is(err, permission.ErrUnknownGrant) {
		t.Fatalf("Revoke = %v", err)
	}
}

// TestGrantRejectsUnknownUser verifies a grant needs a user the provider finds.
func TestGrantRejectsUnknownUser(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "")
	if err := sys.store.GrantRole(ctx, "404", editor); !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// TestGrantAcceptsDisabledUser verifies a disabled user can still be granted.
func TestGrantAcceptsDisabledUser(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "")
	if err := sys.users.Disable("2"); err != nil {
		t.Fatal(err)
	}
	if err := sys.store.GrantRole(ctx, "2", editor); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// TestRevokeRoleProtectsLastHolder verifies the only holder of a KeepOne role
// cannot lose it, while one of two holders can.
func TestRevokeRoleProtectsLastHolder(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newFixture(t, db, instance)
		_ = sys.store.GrantRole(ctx, "1", admin)
		_ = sys.store.GrantRole(ctx, "2", admin)
		if err := sys.store.RevokeRole(ctx, "1", admin); err != nil {
			t.Fatalf("first revoke: %v", err)
		}
		if err := sys.store.RevokeRole(ctx, "2", admin); !errors.Is(err, permission.ErrLastHolder) {
			t.Fatalf("last revoke = %v", err)
		}
		if holders, err := sys.store.Holders(ctx, admin); err != nil || !slices.Equal(holders, []string{"2"}) {
			t.Fatalf("holders = %v, %v", holders, err)
		}
	})
}

// TestConcurrentRevokesKeepOneHolder verifies two concurrent revokes of the last
// two holders leave exactly one, on the databases with row locks.
func TestConcurrentRevokesKeepOneHolder(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		if db.Dialect() == query.SQLite {
			t.Skip("SQLite has a single writer; the race needs row locks")
		}
		ctx := testContext(t)
		sys := newFixture(t, db, instance)
		_ = sys.store.GrantRole(ctx, "1", admin)
		_ = sys.store.GrantRole(ctx, "2", admin)
		var wait sync.WaitGroup
		errs := make([]error, 2)
		for index, subject := range []string{"1", "2"} {
			wait.Go(func() {
				errs[index] = sys.store.RevokeRole(ctx, subject, admin)
			})
		}
		wait.Wait()
		holders, err := sys.store.Holders(ctx, admin)
		if err != nil || len(holders) != 1 {
			t.Fatalf("holders = %v, %v (revokes: %v)", holders, err, errs)
		}
		failed := slices.IndexFunc(errs, func(err error) bool { return err != nil })
		if failed < 0 || !errors.Is(errs[failed], permission.ErrLastHolder) || errs[1-failed] != nil {
			t.Fatalf("revokes = %v", errs)
		}
	})
}

// TestTransferInOneTransaction verifies a KeepOne role moves to another user
// with GrantRole then RevokeRole in one transaction.
func TestTransferInOneTransaction(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newFixture(t, db, instance)
		_ = sys.store.GrantRole(ctx, "1", admin)
		err := db.WithTransaction(ctx, func(ctx context.Context) error {
			if err := sys.store.GrantRole(ctx, "2", admin); err != nil {
				return err
			}

			return sys.store.RevokeRole(ctx, "1", admin)
		})
		if err != nil {
			t.Fatalf("transfer: %v", err)
		}
		if holders, err := sys.store.Holders(ctx, admin); err != nil || !slices.Equal(holders, []string{"2"}) {
			t.Fatalf("holders = %v, %v", holders, err)
		}
	})
}

// TestCacheClearedAfterCommit verifies a cached Load sees a grant once the
// grant's transaction commits, while a Load inside that transaction reads the
// table.
func TestCacheClearedAfterCommit(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newFixtureOn(t, db, instance, cache.New(memorycache.New()), func(policy *permission.Builder) {
			policy.CacheTTL(time.Hour)
		})
		if _, scopes, _ := sys.store.Load(ctx, "1"); scopes != nil {
			t.Fatalf("scopes before = %v", scopes)
		}
		err := db.WithTransaction(ctx, func(ctx context.Context) error {
			if err := sys.store.Grant(ctx, "1", billingExport); err != nil {
				return err
			}
			// Inside the transaction Load bypasses the cache and sees the grant.
			if _, scopes, _ := sys.store.Load(ctx, "1"); !slices.Equal(scopes, []string{"billing:export"}) {
				t.Errorf("scopes inside the transaction = %v", scopes)
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, scopes, err := sys.store.Load(ctx, "1"); err != nil || !slices.Equal(scopes, []string{"billing:export"}) {
			t.Fatalf("scopes after = %v, %v", scopes, err)
		}
	})
}

// superAdmin includes admin, so its holders also hold admin.
var superAdmin = gate.NewRole("super_admin", func(role *gate.RoleBuilder) {
	role.Includes(admin)
})

// TestKeepOneCountsIncludingRoles verifies a user assigned a role that includes
// the protected one counts as a holder: the direct holder may lose it, and the
// last holder across the set may not.
func TestKeepOneCountsIncludingRoles(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newFixture(t, db, instance, func(policy *permission.Builder) {
			policy.Roles(superAdmin)
		})
		_ = sys.store.GrantRole(ctx, "1", admin)
		_ = sys.store.GrantRole(ctx, "2", superAdmin)
		if err := sys.store.RevokeRole(ctx, "1", admin); err != nil {
			t.Fatalf("revoke the direct holder: %v", err)
		}
		if err := sys.store.RevokeRole(ctx, "2", superAdmin); !errors.Is(err, permission.ErrLastHolder) {
			t.Fatalf("revoke the last holder through inclusion = %v", err)
		}
	})
}

// TestForgetCountsIncludingRoles verifies forgetting the last holder through an
// including role publishes the orphan event for the protected role.
func TestForgetCountsIncludingRoles(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "", func(policy *permission.Builder) {
		policy.Roles(superAdmin)
	})
	_ = sys.store.GrantRole(ctx, "2", superAdmin)
	if err := sys.provider.Forget(ctx, "2"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if !slices.Contains(sys.events.all(), "permission.holder.orphaned 2 role admin") {
		t.Fatalf("events = %v", sys.events.all())
	}
}

// TestConcurrentForgetsPublishOrphaned verifies two concurrent forgets of the
// only two holders publish the orphan event at least once, on the databases
// with row locks.
func TestConcurrentForgetsPublishOrphaned(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		if db.Dialect() == query.SQLite {
			t.Skip("SQLite has a single writer; the race needs row locks")
		}
		ctx := testContext(t)
		sys := newFixture(t, db, instance)
		_ = sys.store.GrantRole(ctx, "1", admin)
		_ = sys.store.GrantRole(ctx, "2", admin)
		var wait sync.WaitGroup
		errs := make([]error, 2)
		for index, subject := range []string{"1", "2"} {
			wait.Go(func() {
				errs[index] = sys.provider.Forget(ctx, subject)
			})
		}
		wait.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("forgets = %v", errs)
		}
		if !slices.ContainsFunc(sys.events.all(), func(event string) bool {
			return strings.HasPrefix(event, "permission.holder.orphaned ")
		}) {
			t.Fatalf("events = %v", sys.events.all())
		}
	})
}
