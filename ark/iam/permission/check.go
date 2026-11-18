package permission

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"arkoren.dev/core/auth/gate"
)

// Assignments are the grants stored for one user: the roles assigned and the
// permissions granted directly, without expansion. Names the store no longer
// defines are left out.
//
// Example:
//
//	assigned, err := permissions.Assignments(ctx, subject)
//	fmt.Println(assigned.Roles, assigned.Permissions)
type Assignments struct {
	// Roles are the names of the assigned roles, sorted.
	Roles []string `json:"roles"`

	// Permissions are the names of the directly granted permissions, sorted.
	Permissions []string `json:"permissions"`
}

// Load returns what subject holds: roles are the expanded names of the assigned
// roles (each role and every role it includes) and scopes the names of the
// directly granted permissions and of every permission those roles hold. Both
// are sorted and contain each name once; both are nil for a user with no grants
// and for an unknown subject. Stored names the store no longer defines are
// ignored.
//
// Load has the shape of [auth.GuardBuilder.Grants], so the guard calls it on
// every authenticated session request. It runs one indexed query per call, or
// none while [Builder.CacheTTL] holds the subject's grants. Inside a transaction
// of the store's database it always queries and leaves the cache alone, so it
// sees that transaction's own writes and never caches uncommitted grants. A
// database or cache error is returned, which fails that request.
//
// Example:
//
//	guard, err := auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
//		policy.Grants(permissions.Load)
//	})
func (store *Store) Load(ctx context.Context, subject string) (roles, scopes []string, err error) {
	grants, err := store.grants(ctx, subject)
	if err != nil {
		return nil, nil, err
	}
	roles, scopes = store.expand(grants)

	return roles, scopes, nil
}

// expand turns stored grants into the expanded role names and the effective
// permission names, sorted and each once, nil when empty. Names the store no
// longer defines are ignored.
func (store *Store) expand(grants []stored) (roles, scopes []string) {
	held, granted := map[string]bool{}, map[string]bool{}
	for _, grant := range grants {
		if grant.Kind == KindPermission && store.permissions[grant.Name] {
			granted[grant.Name] = true
		}
		role, ok := store.roles[grant.Name]
		if grant.Kind != KindRole || !ok {
			continue
		}
		for _, reached := range role.Expand() {
			held[reached.Name()] = true
		}
		for _, permission := range role.Permissions() {
			granted[permission.Name()] = true
		}
	}

	return sortedSet(held), sortedSet(granted)
}

// sortedSet returns the keys of set, sorted, or nil when it is empty.
func sortedSet(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}

	return slices.Sorted(maps.Keys(set))
}

// grants returns the stored grants of subject, through the cache when
// [Builder.CacheTTL] is positive and ctx carries no transaction of the store's
// database. Inside a transaction it reads the table directly and neither reads
// nor fills the cache, so the caller sees its own uncommitted writes and an
// uncommitted grant is never cached.
func (store *Store) grants(ctx context.Context, subject string) ([]stored, error) {
	if store.cacheTTL == 0 || store.inTransaction(ctx) {
		return store.read(ctx, subject)
	}
	grants, err := store.cache.Remember(ctx, store.loads.Key(subject), func() ([]stored, error) {
		return store.read(ctx, subject)
	})
	if err != nil {
		return nil, fmt.Errorf("permission: cached grants: %w", err)
	}

	return grants, nil
}

// inTransaction reports whether ctx carries a transaction of the store's
// database. The database package exports no such check, so it relies on the
// contract of [database.Database.AfterCommit]: outside a transaction the hook
// runs at once, inside one it is only registered (and later runs as a no-op).
// An AfterCommit error, such as a context whose session has ended, also counts
// as a transaction, so the cache is bypassed and the read reports the error.
func (store *Store) inTransaction(ctx context.Context) bool {
	ran := false
	_ = store.db.AfterCommit(ctx, func(context.Context) error {
		ran = true

		return nil
	})

	return !ran
}

// Permissions returns the scopes half of [Store.Load]: the names of every
// permission subject holds, sorted. It has the shape of the apitoken grant
// function, so it is the ceiling of API tokens:
// apitoken.Builder.Grants(store.Permissions, store.Catalog()...).
//
// Example:
//
//	tokens.Grants(permissions.Permissions, permissions.Catalog()...)
func (store *Store) Permissions(ctx context.Context, subject string) ([]string, error) {
	_, scopes, err := store.Load(ctx, subject)

	return scopes, err
}

// HasPermission reports whether subject holds every required permission, from
// [Store.Load]. It answers for any user, not only the one making a request; for
// the request's identity use gate.HasPermission instead, which needs no query.
// An empty requirement, or a permission the store does not define, is an error
// wrapping [ErrUnknownGrant].
//
// Example:
//
//	allowed, err := permissions.HasPermission(ctx, subject, BillingExport)
func (store *Store) HasPermission(ctx context.Context, subject string, required ...gate.Permission) (bool, error) {
	wanted, err := store.permissionNames(required)
	if err != nil {
		return false, err
	}
	_, scopes, err := store.Load(ctx, subject)

	return containsAll(scopes, wanted), err
}

// HasAnyPermission reports whether subject holds at least one of permissions,
// from [Store.Load]. Arguments are checked like those of [Store.HasPermission].
//
// Example:
//
//	allowed, err := permissions.HasAnyPermission(ctx, subject, PostsWrite, PostsModerate)
func (store *Store) HasAnyPermission(ctx context.Context, subject string, permissions ...gate.Permission) (bool, error) {
	wanted, err := store.permissionNames(permissions)
	if err != nil {
		return false, err
	}
	_, scopes, err := store.Load(ctx, subject)

	return containsAny(scopes, wanted), err
}

// HasRole reports whether subject holds every required role, directly or
// through a role that includes it, from [Store.Load]. An empty requirement, or a
// role the store does not define, is an error wrapping [ErrUnknownGrant].
//
// Example:
//
//	isEditor, err := permissions.HasRole(ctx, subject, Editor)
func (store *Store) HasRole(ctx context.Context, subject string, required ...gate.Role) (bool, error) {
	wanted, err := store.roleNames(required)
	if err != nil {
		return false, err
	}
	roles, _, err := store.Load(ctx, subject)

	return containsAll(roles, wanted), err
}

// HasAnyRole reports whether subject holds at least one of roles, from
// [Store.Load]. Arguments are checked like those of [Store.HasRole].
//
// Example:
//
//	staff, err := permissions.HasAnyRole(ctx, subject, Editor, Admin)
func (store *Store) HasAnyRole(ctx context.Context, subject string, roles ...gate.Role) (bool, error) {
	wanted, err := store.roleNames(roles)
	if err != nil {
		return false, err
	}
	held, _, err := store.Load(ctx, subject)

	return containsAny(held, wanted), err
}

// permissionNames returns the names of permissions, or an error wrapping
// [ErrUnknownGrant] when there are none or one is not defined.
func (store *Store) permissionNames(permissions []gate.Permission) ([]string, error) {
	if len(permissions) == 0 {
		return nil, fmt.Errorf("%w: no permission given", ErrUnknownGrant)
	}
	if !slices.ContainsFunc(permissions, func(permission gate.Permission) bool { return !store.definesPermission(permission) }) {
		return names(permissions), nil
	}

	return nil, ErrUnknownGrant
}

// roleNames returns the names of roles, or an error wrapping [ErrUnknownGrant]
// when there are none or one is not defined.
func (store *Store) roleNames(roles []gate.Role) ([]string, error) {
	if len(roles) == 0 {
		return nil, fmt.Errorf("%w: no role given", ErrUnknownGrant)
	}
	if !slices.ContainsFunc(roles, func(role gate.Role) bool { return !store.definesRole(role) }) {
		return names(roles), nil
	}

	return nil, ErrUnknownGrant
}

// containsAll reports whether held contains every name in wanted.
func containsAll(held, wanted []string) bool {
	return !slices.ContainsFunc(wanted, func(name string) bool { return !slices.Contains(held, name) })
}

// containsAny reports whether held contains at least one name in wanted.
func containsAny(held, wanted []string) bool {
	return slices.ContainsFunc(wanted, func(name string) bool { return slices.Contains(held, name) })
}

// Assignments returns the roles assigned to subject and the permissions granted
// directly, as stored, read from the table without the cache. Names the store
// no longer defines are left out. A user with no grants has empty lists.
//
// Example:
//
//	assigned, err := permissions.Assignments(ctx, subject)
func (store *Store) Assignments(ctx context.Context, subject string) (Assignments, error) {
	grants, err := store.read(ctx, subject)
	if err != nil {
		return Assignments{}, err
	}

	return store.assignments(grants), nil
}

// assignments returns the stored grants that the store still defines, by kind,
// sorted, with empty rather than nil lists.
func (store *Store) assignments(grants []stored) Assignments {
	assigned := Assignments{Roles: []string{}, Permissions: []string{}}
	for _, grant := range grants {
		_, role := store.roles[grant.Name]
		switch {
		case grant.Kind == KindRole && role:
			assigned.Roles = append(assigned.Roles, grant.Name)
		case grant.Kind == KindPermission && store.permissions[grant.Name]:
			assigned.Permissions = append(assigned.Permissions, grant.Name)
		}
	}
	slices.Sort(assigned.Roles)
	slices.Sort(assigned.Permissions)

	return assigned
}

// Holders returns the subjects assigned role directly, sorted. A user who holds
// it only through a role that includes it is not listed. role must be defined
// by the store, otherwise the error is [ErrUnknownGrant].
//
// Example:
//
//	admins, err := permissions.Holders(ctx, Admin)
func (store *Store) Holders(ctx context.Context, role gate.Role) ([]string, error) {
	if !store.definesRole(role) {
		return nil, ErrUnknownGrant
	}

	held, err := store.holders(ctx, []string{role.Name()}, false)
	if err != nil {
		return nil, err
	}
	subjects := make([]string, 0, len(held))
	for _, holding := range held {
		subjects = append(subjects, holding.Subject)
	}

	return slices.Compact(subjects), nil
}
