package permission_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/permission"
)

// newStore builds a store on an unmigrated SQLite database over an empty user
// provider.
func newStore(t *testing.T, configure ...permission.BuilderFunc) (*permission.Store, error) {
	t.Helper()

	provider, err := auth.NewUserProvider(memoryauth.New())
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}

	return permission.NewStore(provider, sqliteDB(t), cache.New(memorycache.New()), configure...)
}

// TestNewStoreRejectsMissingDependencies verifies nil dependencies are
// ErrInvalidConfig.
func TestNewStoreRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	if _, err := permission.NewStore(nil, nil, nil); !errors.Is(err, permission.ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}
}

// TestNewStoreRejectsInvalidDefinitions verifies an invalid permission or role
// is ErrInvalidConfig and keeps the gate error.
func TestNewStoreRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()

	if _, err := newStore(t, func(policy *permission.Builder) {
		policy.Define(gate.NewPermission("Posts"))
	}); !errors.Is(err, permission.ErrInvalidConfig) || !errors.Is(err, gate.ErrInvalidName) {
		t.Fatalf("invalid permission: %v", err)
	}
	broken := gate.NewRole("lead", func(role *gate.RoleBuilder) {
		role.Includes(viewer, gate.NewRole("viewer", func(role *gate.RoleBuilder) {
			role.Grants(postsWrite)
		}))
	})
	if _, err := newStore(t, func(policy *permission.Builder) {
		policy.Roles(broken)
	}); !errors.Is(err, permission.ErrInvalidConfig) || !errors.Is(err, gate.ErrDuplicateName) {
		t.Fatalf("invalid role: %v", err)
	}
}

// TestNewStoreRejectsDifferentRolesWithOneName verifies two roles given
// separately that share a name but not a definition are rejected, also when
// one of them is only reached through an include.
func TestNewStoreRejectsDifferentRolesWithOneName(t *testing.T) {
	t.Parallel()

	other := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(billingExport)
	})
	if _, err := newStore(t, func(policy *permission.Builder) {
		policy.Roles(editor, other)
	}); !errors.Is(err, permission.ErrInvalidConfig) || !strings.Contains(err.Error(), `"viewer"`) {
		t.Fatalf("err = %v", err)
	}
}

// TestNewStoreAcceptsEqualRolesWithOneName verifies the same role reached from
// several roles is one definition.
func TestNewStoreAcceptsEqualRolesWithOneName(t *testing.T) {
	t.Parallel()

	if _, err := newStore(t, func(policy *permission.Builder) {
		policy.Roles(admin, editor, viewer)
	}); err != nil {
		t.Fatal(err)
	}
}

// TestNewStoreRejectsUndefinedKeepOne verifies a KeepOne role must be defined
// by Roles.
func TestNewStoreRejectsUndefinedKeepOne(t *testing.T) {
	t.Parallel()

	if _, err := newStore(t, func(policy *permission.Builder) {
		policy.Roles(editor).KeepOne(admin)
	}); !errors.Is(err, permission.ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}
}

// TestNewStoreRejectsUndefinedManagement verifies the management permission
// must be defined.
func TestNewStoreRejectsUndefinedManagement(t *testing.T) {
	t.Parallel()

	if _, err := newStore(t, func(policy *permission.Builder) {
		policy.Roles(editor).Management(true, usersManage)
	}); !errors.Is(err, permission.ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}
}

// TestNewStoreRejectsBadSettings verifies an invalid instance and a negative
// cache TTL are ErrInvalidConfig.
func TestNewStoreRejectsBadSettings(t *testing.T) {
	t.Parallel()

	if _, err := newStore(t, func(policy *permission.Builder) {
		policy.Instance("Bad")
	}); !errors.Is(err, permission.ErrInvalidConfig) {
		t.Fatalf("instance: %v", err)
	}
	if _, err := newStore(t, func(policy *permission.Builder) {
		policy.CacheTTL(-1)
	}); !errors.Is(err, permission.ErrInvalidConfig) {
		t.Fatalf("cache TTL: %v", err)
	}
}

// TestCatalogListsDefinedAndRolePermissions verifies the catalog is the sorted
// union of Define and every permission the roles hold.
func TestCatalogListsDefinedAndRolePermissions(t *testing.T) {
	t.Parallel()

	store, err := newStore(t, func(policy *permission.Builder) {
		policy.Define(billingExport, postsRead).Roles(admin)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Catalog(); !slices.Equal(got, []string{"billing:export", "posts:read", "posts:write", "users:manage"}) {
		t.Fatalf("catalog = %v", got)
	}
}

// TestMigrationGroupIdentity verifies the shipped migration group and id of the
// default and a named instance.
func TestMigrationGroupIdentity(t *testing.T) {
	t.Parallel()

	for instance, want := range map[string]string{
		"":         "arkoren.dev:ark/access:permission/permission-grants-create-table",
		"tenant_a": "arkoren.dev:ark/access:permission:tenant_a/permission-grants-create-table",
	} {
		store, err := newStore(t, func(policy *permission.Builder) {
			policy.Instance(instance)
		})
		if err != nil {
			t.Fatal(err)
		}
		migrations := store.Migrations()
		if len(migrations) != 1 || migrations[0].ID() != want {
			t.Fatalf("%q: migrations = %v", instance, migrations)
		}
	}
}

// TestPingNamesMigrations verifies Ping reports a missing table and passes after
// the migrations ran.
func TestPingNamesMigrations(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		provider, _ := auth.NewUserProvider(memoryauth.New())
		store, err := permission.NewStore(provider, db, cache.New(memorycache.New()), func(policy *permission.Builder) {
			policy.Instance(instance)
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Ping(ctx); !errors.Is(err, permission.ErrNotMigrated) {
			t.Fatalf("Ping = %v, want ErrNotMigrated", err)
		}
		migrateUp(t, db, store.Migrations()...)
		if err := store.Ping(ctx); err != nil {
			t.Fatalf("Ping after migrations: %v", err)
		}
	})
}

// TestTwoInstancesAreIsolated verifies a grant in one instance is invisible to
// another on the same database.
func TestTwoInstancesAreIsolated(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		first := newFixture(t, db, instance+"_a")
		second := newFixture(t, db, instance+"_b")
		if err := first.store.GrantRole(ctx, "1", editor); err != nil {
			t.Fatal(err)
		}
		if roles, scopes, err := second.store.Load(ctx, "1"); err != nil || roles != nil || scopes != nil {
			t.Fatalf("second instance = %v %v %v", roles, scopes, err)
		}
	})
}

// TestForgetDeletesGrantsAndPublishesOrphaned verifies forgetting a user removes
// their grants and reports a KeepOne role left without holders.
func TestForgetDeletesGrantsAndPublishesOrphaned(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newFixture(t, db, instance)
		if err := sys.store.GrantRole(ctx, "1", admin); err != nil {
			t.Fatal(err)
		}
		if err := sys.store.Grant(ctx, "1", billingExport); err != nil {
			t.Fatal(err)
		}
		if err := sys.provider.Forget(ctx, "1"); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if assigned, err := sys.store.Assignments(ctx, "1"); err != nil || len(assigned.Roles)+len(assigned.Permissions) != 0 {
			t.Fatalf("assignments = %+v, %v", assigned, err)
		}
		if events := sys.events.all(); !slices.Contains(events, "permission.holder.orphaned 1 role admin") {
			t.Fatalf("events = %v", events)
		}
	})
}

// TestForgetKeepsOtherHolders verifies no orphan event while another user still
// holds the KeepOne role.
func TestForgetKeepsOtherHolders(t *testing.T) {
	t.Parallel()

	ctx := testContext(t)
	sys := newFixture(t, sqliteDB(t), "")
	for _, subject := range []string{"1", "2"} {
		if err := sys.store.GrantRole(ctx, subject, admin); err != nil {
			t.Fatal(err)
		}
	}
	if err := sys.provider.Forget(ctx, "1"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if slices.ContainsFunc(sys.events.all(), func(event string) bool { return strings.HasPrefix(event, "permission.holder.orphaned") }) {
		t.Fatalf("events = %v", sys.events.all())
	}
}
