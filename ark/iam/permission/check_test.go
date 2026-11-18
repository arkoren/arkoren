package permission_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/iam/permission"
)

// TestLoadExpandsRolesAndPermissions verifies Load returns the expanded roles
// and the union of direct and role permissions, sorted.
func TestLoadExpandsRolesAndPermissions(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newFixture(t, db, instance)
		_ = sys.store.GrantRole(ctx, "1", editor)
		_ = sys.store.Grant(ctx, "1", billingExport)
		roles, scopes, err := sys.store.Load(ctx, "1")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(roles, []string{"editor", "viewer"}) || !slices.Equal(scopes, []string{"billing:export", "posts:read", "posts:write"}) {
			t.Fatalf("roles = %v, scopes = %v", roles, scopes)
		}
	})
}

// TestLoadIgnoresUndefinedStoredNames verifies rows naming a role or
// permission the store no longer defines grant nothing and are not listed.
func TestLoadIgnoresUndefinedStoredNames(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	db := sqliteDB(t)
	sys := newFixture(t, db, "")
	for _, row := range []map[string]any{
		{"subject": "1", "kind": "role", "name": "retired", "created_at": time.Now().Unix()},
		{"subject": "1", "kind": "permission", "name": "posts:delete", "created_at": time.Now().Unix()},
	} {
		if _, err := db.Exec(ctx, query.Table("permission_grants", func(statement *query.Builder) {
			statement.Insert(row)
		})); err != nil {
			t.Fatal(err)
		}
	}
	if roles, scopes, err := sys.store.Load(ctx, "1"); err != nil || roles != nil || scopes != nil {
		t.Fatalf("Load = %v %v %v", roles, scopes, err)
	}
	if assigned, err := sys.store.Assignments(ctx, "1"); err != nil || len(assigned.Roles)+len(assigned.Permissions) != 0 {
		t.Fatalf("Assignments = %+v, %v", assigned, err)
	}
}

// TestPermissionsIsLoadScopes verifies the token ceiling is the scopes half of
// Load.
func TestPermissionsIsLoadScopes(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "")
	_ = sys.store.GrantRole(ctx, "1", viewer)
	if scopes, err := sys.store.Permissions(ctx, "1"); err != nil || !slices.Equal(scopes, []string{"posts:read"}) {
		t.Fatalf("Permissions = %v, %v", scopes, err)
	}
}

// TestHasPermissionRequiresAll verifies HasPermission needs every permission and
// HasAnyPermission one of them.
func TestHasPermissionRequiresAll(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "")
	_ = sys.store.GrantRole(ctx, "1", viewer)
	if ok, err := sys.store.HasPermission(ctx, "1", postsRead, postsWrite); err != nil || ok {
		t.Fatalf("HasPermission = %v, %v", ok, err)
	}
	if ok, err := sys.store.HasAnyPermission(ctx, "1", postsRead, postsWrite); err != nil || !ok {
		t.Fatalf("HasAnyPermission = %v, %v", ok, err)
	}
}

// TestHasRoleSeesIncludedRoles verifies a user assigned editor holds viewer, and
// HasAnyRole needs one role.
func TestHasRoleSeesIncludedRoles(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "")
	_ = sys.store.GrantRole(ctx, "1", editor)
	if ok, err := sys.store.HasRole(ctx, "1", viewer, editor); err != nil || !ok {
		t.Fatalf("HasRole = %v, %v", ok, err)
	}
	if ok, err := sys.store.HasAnyRole(ctx, "1", admin); err != nil || ok {
		t.Fatalf("HasAnyRole = %v, %v", ok, err)
	}
}

// TestHasChecksRejectUndefinedArguments verifies empty or undefined arguments
// are ErrUnknownGrant.
func TestHasChecksRejectUndefinedArguments(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "")
	if _, err := sys.store.HasPermission(ctx, "1"); !errors.Is(err, permission.ErrUnknownGrant) {
		t.Fatalf("empty = %v", err)
	}
	if _, err := sys.store.HasPermission(ctx, "1", unknownThing); !errors.Is(err, permission.ErrUnknownGrant) {
		t.Fatalf("unknown permission = %v", err)
	}
	if _, err := sys.store.HasRole(ctx, "1", impostor); !errors.Is(err, permission.ErrUnknownGrant) {
		t.Fatalf("impostor role = %v", err)
	}
}

// TestHoldersListsDirectHolders verifies Holders lists direct assignments only.
func TestHoldersListsDirectHolders(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "")
	_ = sys.store.GrantRole(ctx, "2", viewer)
	_ = sys.store.GrantRole(ctx, "1", editor)
	_ = sys.store.GrantRole(ctx, "3", viewer)
	if holders, err := sys.store.Holders(ctx, viewer); err != nil || !slices.Equal(holders, []string{"2", "3"}) {
		t.Fatalf("holders = %v, %v", holders, err)
	}
}

// TestLoadInsideRolledBackTransactionCachesNothing verifies a grant seen inside
// a transaction that rolls back never reaches the cache.
func TestLoadInsideRolledBackTransactionCachesNothing(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newFixture(t, db, instance, func(policy *permission.Builder) {
			policy.CacheTTL(time.Hour)
		})
		rollback := errors.New("roll back")
		err := db.WithTransaction(ctx, func(ctx context.Context) error {
			if err := sys.store.GrantRole(ctx, "1", editor); err != nil {
				return err
			}
			if ok, err := sys.store.HasRole(ctx, "1", editor); err != nil || !ok {
				t.Errorf("HasRole inside = %v, %v", ok, err)
			}

			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("transaction = %v", err)
		}
		if roles, _, err := sys.store.Load(ctx, "1"); err != nil || roles != nil {
			t.Fatalf("roles after rollback = %v, %v", roles, err)
		}
	})
}
