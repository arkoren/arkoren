package permission_test

import (
	"context"
	"sync"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/permission"
)

// Definitions shared by the tests: admin includes editor, editor includes
// viewer, and users:manage is granted only by admin.
var (
	postsRead     = gate.NewPermission("posts:read")
	postsWrite    = gate.NewPermission("posts:write")
	usersManage   = gate.NewPermission("users:manage")
	billingExport = gate.NewPermission("billing:export")

	viewer = gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(postsRead)
	})
	editor = gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Includes(viewer).Grants(postsWrite)
	})
	admin = gate.NewRole("admin", func(role *gate.RoleBuilder) {
		role.Includes(editor).Grants(usersManage)
	})

	// unknownThing and impostor are valid definitions the stores never define;
	// impostor shares the name of editor.
	unknownThing = gate.NewPermission("unknown:thing")
	impostor     = gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Grants(usersManage)
	})
)

// recorder collects the events of the package.
type recorder struct {
	mu     sync.Mutex
	events []string
}

// record listens on bus and records "<topic> <subject> <kind> <name>".
func (recorder *recorder) record(t *testing.T, bus *event.Bus) {
	t.Helper()

	for _, topic := range []event.Topic[permission.GrantEvent]{permission.GrantCreated, permission.GrantRevoked, permission.HolderOrphaned} {
		if _, err := bus.Listen(topic, func(_ context.Context, payload permission.GrantEvent) error {
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			recorder.events = append(recorder.events, topic.Name()+" "+payload.Subject+" "+string(payload.Kind)+" "+payload.Name)

			return nil
		}); err != nil {
			t.Fatalf("Listen: %v", err)
		}
	}
}

// all returns a copy of the recorded events.
func (recorder *recorder) all() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	return append([]string(nil), recorder.events...)
}

// fixture is a migrated store over users "1", "2" and "3".
type fixture struct {
	users    *memoryauth.Driver
	provider *auth.UserProvider
	store    *permission.Store
	events   *recorder
}

// newFixture builds and migrates a store on db with the shared roles, admin
// protected by KeepOne, billing:export defined directly and events recorded.
// configure runs after those settings.
func newFixture(t *testing.T, db *database.Database, instance string, configure ...permission.BuilderFunc) *fixture {
	t.Helper()

	return newFixtureOn(t, db, instance, cache.New(memorycache.New()), configure...)
}

// newFixtureOn is newFixture over the cache store.
func newFixtureOn(t *testing.T, db *database.Database, instance string, store *cache.Cache, configure ...permission.BuilderFunc) *fixture {
	t.Helper()

	users := memoryauth.New()
	for _, subject := range []string{"1", "2", "3"} {
		if err := users.Put(auth.UserRecord{Subject: subject}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	provider, err := auth.NewUserProvider(users)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}
	bus := event.New()
	events := &recorder{}
	events.record(t, bus)
	permissions, err := permission.NewStore(provider, db, store, append([]permission.BuilderFunc{func(policy *permission.Builder) {
		policy.Instance(instance).Events(bus).Define(billingExport).Roles(admin).KeepOne(admin)
	}}, configure...)...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	migrateUp(t, db, permissions.Migrations()...)

	return &fixture{users: users, provider: provider, store: permissions, events: events}
}
