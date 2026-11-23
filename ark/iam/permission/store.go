package permission

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/iam/internal/migration"
	"arkoren.dev/ark/iam/internal/storage"
)

// Store keeps which users hold which roles and permissions, and answers what a
// user holds. It is safe for concurrent use. Create it with [NewStore].
//
// The definitions are core [gate.Permission] and [gate.Role] values; the store
// only records assignments of the ones it defines. [Store.Load] has the shape
// of [auth.GuardBuilder.Grants], so a guard puts a user's expanded role names
// and effective permission names on every session identity, and
// [Store.Permissions] has the shape of the apitoken grant function, so tokens
// never carry more than their owner holds.
//
// A name stored in the table that the store no longer defines (a role removed
// from the code) is ignored everywhere: it grants nothing and is not listed,
// which fails closed. Its row stays until the user is forgotten or the
// application deletes it.
//
// Example:
//
//	permissions, err := permission.NewStore(provider, db, store, func(policy *permission.Builder) {
//		policy.Roles(Viewer, Editor)
//	})
//	if err != nil {
//		return err
//	}
//	guard, err := auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
//		policy.Grants(permissions.Load)
//	})
type Store struct {
	// provider finds users before a grant and runs the forget hook.
	provider *auth.UserProvider

	// db holds the grants table.
	db *database.Database

	// cache holds the cached grants when cacheTTL is positive.
	cache *cache.Cache

	// loads names the cached grants of each subject.
	loads cache.Namespace[[]stored]

	// cacheTTL is [Builder.CacheTTL]; zero disables the cache.
	cacheTTL time.Duration

	// events receives the events of this package; nil publishes nothing.
	events *event.Bus

	// instance and table are the instance name and its grants table.
	instance string
	table    string

	// lockable reports whether the dialect supports FOR UPDATE: PostgreSQL and
	// MySQL. SQLite has a single writer and rejects the clause.
	lockable bool

	// catalog is the sorted list of defined permission names; permissions is
	// the same set for lookups.
	catalog     []string
	permissions map[string]bool

	// roles are the defined roles by name, including every role they reach.
	roles map[string]gate.Role

	// keepers maps each [Builder.KeepOne] role name to the sorted names of every
	// defined role whose expansion contains it, itself included: an assignment
	// of any of them makes its subject a holder of the protected role.
	keepers map[string][]string

	// selfService and management are [Builder.SelfService] and
	// [Builder.Management]; managed reports whether management is enabled.
	selfService bool
	management  gate.Permission
	managed     bool
}

// stored is one grant as the table and the cache hold it.
type stored struct {
	// Kind is whether the grant is a role or a permission.
	Kind Kind `json:"kind" db:"kind"`

	// Name is the role or permission name.
	Name string `json:"name" db:"name"`
}

// NewStore returns a permission store. provider finds users (a grant to an
// unknown user is [auth.ErrUserNotFound]) and gets a forget hook that deletes
// the user's grants in the caller's transaction; db holds the table; store is
// the cache used by [Builder.CacheTTL] (required either way, so enabling the
// cache later changes no signature). It does no I/O: the table comes from
// [Store.Migrations].
//
// Errors wrap [ErrInvalidConfig]: a nil dependency, an invalid instance or
// negative cache TTL, a definition whose Err is not nil (including
// [gate.ErrInvalidName] and [gate.ErrDuplicateName]), two different roles with
// one name anywhere among the roles given and the roles they include, a
// [Builder.KeepOne] role that [Builder.Roles] does not define, or a
// [Builder.Management] permission that is not defined. Two roles are different
// when their names match but the names of their included roles or of their
// permissions differ.
//
// Example:
//
//	permissions, err := permission.NewStore(provider, db, store, func(policy *permission.Builder) {
//		policy.Define(BillingExport).Roles(Viewer, Editor, Admin).KeepOne(Admin)
//	})
func NewStore(provider *auth.UserProvider, db *database.Database, store *cache.Cache, configure ...BuilderFunc) (*Store, error) {
	if provider == nil || db == nil || store == nil {
		return nil, fmt.Errorf("%w: provider, database and store are required", ErrInvalidConfig)
	}
	policy := &Builder{}
	for _, apply := range configure {
		apply(policy)
	}
	if err := policy.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	permissions := &Store{
		provider:    provider,
		db:          db,
		cache:       store,
		cacheTTL:    policy.cacheTTL,
		events:      policy.events,
		instance:    policy.instance,
		table:       storage.Table(policy.instance, "permission_grants"),
		lockable:    db.Dialect() == query.Postgres || db.Dialect() == query.MySQL,
		selfService: policy.selfService,
		management:  policy.management,
		managed:     policy.managed,
		loads: cache.NewNamespace[[]stored]("access:"+policy.instance+":permission:load", func(namespace *cache.NamespaceBuilder) {
			namespace.TTL(policy.cacheTTL)
		}),
	}
	if err := permissions.define(policy); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	provider.OnForget(permissions.forget)

	return permissions, nil
}

// define fills the catalog, the roles and the protected roles from policy, and
// checks that every name has one definition and that the KeepOne roles and the
// management permission are defined.
func (store *Store) define(policy *Builder) error {
	store.permissions = map[string]bool{}
	for _, permission := range policy.defined {
		store.permissions[permission.Name()] = true
	}
	if err := store.defineRoles(policy.roles); err != nil {
		return err
	}
	store.keepers = map[string][]string{}
	for _, role := range policy.keepOne {
		if !store.definesRole(role) {
			return fmt.Errorf("keep-one role %q is not defined by Roles", role.Name())
		}
		store.keepers[role.Name()] = store.including(role.Name())
	}
	if policy.managed && !store.definesPermission(policy.management) {
		return fmt.Errorf("management permission %q is not defined", policy.management.Name())
	}
	store.catalog = slices.Sorted(maps.Keys(store.permissions))

	return nil
}

// defineRoles records every role reached from roles and the permissions they
// hold, and fails when two different roles share a name.
func (store *Store) defineRoles(roles []gate.Role) error {
	store.roles = map[string]gate.Role{}
	for _, role := range roles {
		for _, reached := range role.Expand() {
			if previous, ok := store.roles[reached.Name()]; ok && !sameRole(previous, reached) {
				return fmt.Errorf("two different roles are named %q", reached.Name())
			}
			store.roles[reached.Name()] = reached
		}
		for _, permission := range role.Permissions() {
			store.permissions[permission.Name()] = true
		}
	}

	return nil
}

// including returns, sorted, the names of the defined roles whose expansion
// contains the role called name, that role included.
func (store *Store) including(name string) []string {
	var names []string
	for candidate, role := range store.roles {
		if slices.ContainsFunc(role.Expand(), func(reached gate.Role) bool { return reached.Name() == name }) {
			names = append(names, candidate)
		}
	}
	slices.Sort(names)

	return names
}

// sameRole reports whether left and right define the same role: equal names,
// equal sets of included role names and equal sets of permission names.
func sameRole(left, right gate.Role) bool {
	return left.Name() == right.Name() &&
		slices.Equal(names(left.Includes()), names(right.Includes())) &&
		slices.Equal(names(left.Permissions()), names(right.Permissions()))
}

// names returns the sorted, deduplicated names of definitions.
func names[T interface{ Name() string }](definitions []T) []string {
	result := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, definition.Name())
	}
	slices.Sort(result)

	return slices.Compact(result)
}

// definesPermission reports whether permission is valid and defined.
func (store *Store) definesPermission(permission gate.Permission) bool {
	return permission.Err() == nil && store.permissions[permission.Name()]
}

// definesRole reports whether role is valid and the store defines the same role
// under its name.
func (store *Store) definesRole(role gate.Role) bool {
	defined, ok := store.roles[role.Name()]

	return ok && role.Err() == nil && sameRole(defined, role)
}

// Catalog returns the names of every permission the store defines, sorted: those
// of [Builder.Define] and those the roles of [Builder.Roles] hold. It is the
// scope catalog for API tokens:
// apitoken.Builder.Grants(store.Permissions, store.Catalog()...).
//
// Example:
//
//	tokens.Grants(permissions.Permissions, permissions.Catalog()...)
func (store *Store) Catalog() []string {
	return slices.Clone(store.catalog)
}

// SelfService reports whether [Builder.SelfService] asked for the self-service
// route, which the facade reads to decide what to mount.
//
// Example:
//
//	if permissions.SelfService() {
//		web.Get("/me/permissions", permissions.SelfHandler())
//	}
func (store *Store) SelfService() bool {
	return store.selfService
}

// Management returns the permission of [Builder.Management] and whether it was
// set, which the facade reads to decide what to mount.
//
// Example:
//
//	if _, ok := permissions.Management(); ok {
//		web.Get("/users/{subject}/permissions", permissions.UserHandler())
//	}
func (store *Store) Management() (gate.Permission, bool) {
	return store.management, store.managed
}

// Migrations returns the migration that creates the "permission_grants" table
// (prefixed "<instance>_" for a named instance) in the group
// "arkoren.dev:ark/access:permission", plus ":<instance>". The table holds one
// row per subject, kind and name, and an index on kind and name for
// [Store.Holders]. It creates the table strictly and its down step drops it.
// The application adds it to its migrator; constructing or reading it never runs
// DDL.
//
// Example:
//
//	if err := migrator.Add(permissions.Migrations()...); err != nil {
//		return err
//	}
func (store *Store) Migrations() []migrate.Definition {
	return migration.Permissions(store.instance)
}

// Ping verifies, read-only, that the table exists. A missing table returns an
// error wrapping [ErrNotMigrated]. Use it as a readiness check.
//
// Example:
//
//	if err := permissions.Ping(ctx); err != nil {
//		return err
//	}
func (store *Store) Ping(ctx context.Context) error {
	return storage.Ping(ctx, store.db, store.table)
}

// forget is the forget hook: in a transaction (joining the caller's) it deletes
// every grant of subject, clears the subject's cache entry after commit and
// publishes [HolderOrphaned] for each [Builder.KeepOne] role the subject held,
// directly or through an including role, that is left with no holder. It locks
// the holders of those roles first (FOR UPDATE on PostgreSQL and MySQL), so two
// concurrent forgets of the last two holders cannot both miss the orphan.
func (store *Store) forget(ctx context.Context, subject string) error {
	return store.db.WithTransaction(ctx, func(ctx context.Context) error {
		held, err := store.read(ctx, subject)
		if err != nil {
			return err
		}
		var orphaned []GrantEvent
		for _, kept := range slices.Sorted(maps.Keys(store.keepers)) {
			holds := func(grant stored) bool {
				return grant.Kind == KindRole && slices.Contains(store.keepers[kept], grant.Name)
			}
			if !slices.ContainsFunc(held, holds) {
				continue
			}
			holders, err := store.holders(ctx, store.keepers[kept], store.lockable)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(holders, func(other holding) bool { return other.Subject != subject }) {
				orphaned = append(orphaned, GrantEvent{Subject: subject, Kind: KindRole, Name: kept})
			}
		}
		if _, err := store.db.Exec(ctx, query.Table(store.table, func(statement *query.Builder) {
			statement.Where("subject", subject).Delete()
		})); err != nil {
			return fmt.Errorf("permission: forget: %w", err)
		}

		return store.committed(ctx, subject, HolderOrphaned, orphaned...)
	})
}

// read returns the stored grants of subject, in no particular order, straight
// from the table.
func (store *Store) read(ctx context.Context, subject string) ([]stored, error) {
	rows, err := store.db.Select[stored](ctx, query.Table(store.table, func(statement *query.Builder) {
		statement.Select("kind", "name").Where("subject", subject)
	}))
	if err != nil {
		return nil, fmt.Errorf("permission: read grants: %w", err)
	}

	return rows, nil
}

// committed clears the cached grants of subject and publishes events on topic
// once the surrounding transaction, if any, has committed. Outside a transaction
// it runs at once. A cache error is returned wrapped in
// [database.ErrAfterCommit]: the write committed, but the cache may serve the
// old grants until its TTL ends.
func (store *Store) committed(ctx context.Context, subject string, topic event.Topic[GrantEvent], events ...GrantEvent) error {
	if store.cacheTTL == 0 && len(events) == 0 {
		return nil
	}

	return store.db.AfterCommit(ctx, func(ctx context.Context) error {
		for _, payload := range events {
			store.events.Emit(ctx, topic, payload)
		}
		if store.cacheTTL == 0 {
			return nil
		}
		if err := store.cache.Delete(ctx, store.loads.Key(subject)); err != nil {
			return fmt.Errorf("permission: clear cached grants: %w", err)
		}

		return nil
	})
}
