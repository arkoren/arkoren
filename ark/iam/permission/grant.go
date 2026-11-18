package permission

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/iam/internal/storage"
)

// Grant gives subject a permission directly. permission must be defined by the
// store, otherwise the error is [ErrUnknownGrant]; subject must be a user the
// provider finds, otherwise the provider's error is returned
// ([auth.ErrUserNotFound]). A disabled user can be granted permissions. Granting
// a permission the subject already has stores nothing and publishes nothing, so
// Grant is idempotent; a new grant publishes [GrantCreated] after commit.
//
// Grant joins the transaction in ctx. An error wrapping
// [database.ErrAfterCommit] means the grant committed but clearing the cached
// grants failed (see [Builder.CacheTTL]).
//
// Example:
//
//	if err := permissions.Grant(ctx, subject, BillingExport); err != nil {
//		return err
//	}
func (store *Store) Grant(ctx context.Context, subject string, permission gate.Permission) error {
	if !store.definesPermission(permission) {
		return ErrUnknownGrant
	}

	return store.insert(ctx, subject, KindPermission, permission.Name())
}

// GrantRole assigns subject a role, and so every role it includes and every
// permission they hold. role must be defined by the store, as [Builder.Roles]
// or a role one of them includes, otherwise the error is [ErrUnknownGrant]. It
// otherwise behaves like [Store.Grant]: the user must exist, a repeat is a
// no-op and a new assignment publishes [GrantCreated].
//
// To transfer a [Builder.KeepOne] role, grant it to the new holder and revoke it
// from the old one in one transaction:
//
//	err := db.WithTransaction(ctx, func(ctx context.Context) error {
//		if err := permissions.GrantRole(ctx, next, Owner); err != nil {
//			return err
//		}
//
//		return permissions.RevokeRole(ctx, previous, Owner)
//	})
//
// Example:
//
//	if err := permissions.GrantRole(ctx, subject, Editor); err != nil {
//		return err
//	}
func (store *Store) GrantRole(ctx context.Context, subject string, role gate.Role) error {
	if !store.definesRole(role) {
		return ErrUnknownGrant
	}

	return store.insert(ctx, subject, KindRole, role.Name())
}

// insert stores the grant unless it exists, after checking that subject is a
// user, and publishes [GrantCreated] after commit when a row was inserted.
func (store *Store) insert(ctx context.Context, subject string, kind Kind, name string) error {
	if _, err := store.provider.Find(ctx, subject); err != nil && !errors.Is(err, auth.ErrUserDisabled) {
		return err
	}
	affected, err := store.db.Exec(ctx, query.Table(store.table, func(statement *query.Builder) {
		statement.Insert(map[string]any{
			"subject":    subject,
			"kind":       string(kind),
			"name":       name,
			"created_at": storage.Unix(time.Now()),
		})
		statement.OnConflict("subject", "kind", "name").DoNothing()
	}))
	if err != nil {
		return fmt.Errorf("permission: grant: %w", err)
	}
	if affected == 0 {
		return nil
	}

	return store.committed(ctx, subject, GrantCreated, GrantEvent{Subject: subject, Kind: kind, Name: name})
}

// Revoke takes a directly granted permission away from subject. It does not
// touch roles: a permission the subject also holds through a role stays held.
// permission must be defined by the store, otherwise the error is
// [ErrUnknownGrant]. Revoking a permission the subject was not granted is a
// no-op; a deleted grant publishes [GrantRevoked] after commit. It joins the
// transaction in ctx, and an error wrapping [database.ErrAfterCommit] means the
// revoke committed but clearing the cache failed.
//
// Example:
//
//	if err := permissions.Revoke(ctx, subject, BillingExport); err != nil {
//		return err
//	}
func (store *Store) Revoke(ctx context.Context, subject string, permission gate.Permission) error {
	if !store.definesPermission(permission) {
		return ErrUnknownGrant
	}

	return store.delete(ctx, subject, KindPermission, permission.Name())
}

// RevokeRole takes a role assignment away from subject; roles it includes go
// with it unless assigned on their own. role must be defined by the store,
// otherwise the error is [ErrUnknownGrant]. Revoking a role the subject does not
// hold is a no-op; a deleted assignment publishes [GrantRevoked] after commit.
//
// When the role is, or includes, a [Builder.KeepOne] role, it runs in a
// transaction (joining the one in ctx) that locks the holders of each such
// protected role (FOR UPDATE on PostgreSQL and MySQL) and answers
// [ErrLastHolder] when removing this assignment would leave a protected role
// with no holder. Holders count through inclusion: a user assigned a role that
// includes the protected one holds it too.
//
// Example:
//
//	if err := permissions.RevokeRole(ctx, subject, Editor); err != nil {
//		return err
//	}
func (store *Store) RevokeRole(ctx context.Context, subject string, role gate.Role) error {
	if !store.definesRole(role) {
		return ErrUnknownGrant
	}
	protected := store.protectedBy(role.Name())
	if len(protected) == 0 {
		return store.delete(ctx, subject, KindRole, role.Name())
	}
	revoked := holding{Subject: subject, Name: role.Name()}

	return store.db.WithTransaction(ctx, func(ctx context.Context) error {
		for _, kept := range protected {
			held, err := store.holders(ctx, store.keepers[kept], store.lockable)
			if err != nil {
				return err
			}
			if !slices.Contains(held, revoked) {
				return nil
			}
			if !slices.ContainsFunc(held, func(other holding) bool { return other != revoked }) {
				return ErrLastHolder
			}
		}

		return store.delete(ctx, subject, KindRole, role.Name())
	})
}

// protectedBy returns, sorted, the [Builder.KeepOne] roles whose holders include
// the assignments of the role called name: the role itself when protected, and
// every protected role it includes. A sorted order keeps the lock order of
// concurrent revokes the same.
func (store *Store) protectedBy(name string) []string {
	var protected []string
	for _, kept := range slices.Sorted(maps.Keys(store.keepers)) {
		if slices.Contains(store.keepers[kept], name) {
			protected = append(protected, kept)
		}
	}

	return protected
}

// delete removes one grant of subject and publishes [GrantRevoked] after commit
// when a row was deleted.
func (store *Store) delete(ctx context.Context, subject string, kind Kind, name string) error {
	affected, err := store.db.Exec(ctx, query.Table(store.table, func(statement *query.Builder) {
		statement.Where("subject", subject).Where("kind", string(kind)).Where("name", name).Delete()
	}))
	if err != nil {
		return fmt.Errorf("permission: revoke: %w", err)
	}
	if affected == 0 {
		return nil
	}

	return store.committed(ctx, subject, GrantRevoked, GrantEvent{Subject: subject, Kind: kind, Name: name})
}

// holding is one role assignment: who holds which role directly.
type holding struct {
	// Subject is the user assigned the role.
	Subject string `db:"subject"`

	// Name is the assigned role.
	Name string `db:"name"`
}

// holders returns the assignments of the roles called names, sorted by subject
// and name. With lock the rows are selected FOR UPDATE, which only PostgreSQL
// and MySQL accept; the query uses no DISTINCT, which PostgreSQL rejects with a
// row lock.
func (store *Store) holders(ctx context.Context, names []string, lock bool) ([]holding, error) {
	rows, err := store.db.Select[holding](ctx, query.Table(store.table, func(statement *query.Builder) {
		statement.Select("subject", "name").Where("kind", string(KindRole)).WhereIn("name", names...)
		statement.OrderBy("subject").OrderBy("name")
		if lock {
			statement.ForUpdate()
		}
	}))
	if err != nil {
		return nil, fmt.Errorf("permission: read holders: %w", err)
	}

	return rows, nil
}
