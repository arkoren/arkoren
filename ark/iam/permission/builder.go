package permission

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/event"

	"arkoren.dev/ark/iam/internal/storage"
)

// Builder configures [NewStore] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft, and
// retaining it cannot change a published [Store]. [Builder.Define],
// [Builder.Roles] and [Builder.KeepOne] add up across calls; the other settings
// replace the previous value.
//
// Example:
//
//	permissions, err := permission.NewStore(provider, db, store, func(policy *permission.Builder) {
//		policy.Roles(Viewer, Editor, Admin).KeepOne(Admin).CacheTTL(time.Minute)
//	})
type Builder struct {
	// instance is [Builder.Instance].
	instance string

	// events is [Builder.Events]; nil publishes nothing.
	events *event.Bus

	// defined are the permissions of [Builder.Define], in call order.
	defined []gate.Permission

	// roles are the roles of [Builder.Roles], in call order.
	roles []gate.Role

	// keepOne are the roles of [Builder.KeepOne], in call order.
	keepOne []gate.Role

	// cacheTTL is [Builder.CacheTTL]; zero disables the cache.
	cacheTTL time.Duration

	// selfService is [Builder.SelfService].
	selfService bool

	// management is the permission of [Builder.Management]; managed reports
	// whether it was set.
	management gate.Permission
	managed    bool
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure permission.BuilderFunc = func(policy *permission.Builder) {
//		policy.Roles(Editor)
//	}
//	permissions, err := permission.NewStore(provider, db, store, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: the table is prefixed "<instance>_", the
// migration group gains ":<instance>" and cache keys are separated, so two
// stores can share one database and one cache. The name is 1 to 20 bytes of
// [a-z0-9_] starting with a letter; the default is the empty instance.
//
// Example:
//
//	policy.Instance("tenant_a")
func (policy *Builder) Instance(name string) *Builder {
	policy.instance = name

	return policy
}

// Events publishes [GrantCreated], [GrantRevoked] and [HolderOrphaned] on bus. A
// nil bus, the default, publishes nothing.
//
// Example:
//
//	policy.Events(events)
func (policy *Builder) Events(bus *event.Bus) *Builder {
	policy.events = bus

	return policy
}

// Define adds permissions that may be granted directly. The permissions of the
// roles of [Builder.Roles] are defined without it; use Define for permissions
// no role grants, or to list every permission in one place. The values are
// copied. An invalid permission makes [NewStore] fail.
//
// Example:
//
//	policy.Define(PostsRead, PostsWrite, BillingExport)
func (policy *Builder) Define(permissions ...gate.Permission) *Builder {
	policy.defined = append(policy.defined, permissions...)

	return policy
}

// Roles adds roles that may be assigned. Every role they include, directly or
// transitively, is defined too and may be assigned on its own, and so is every
// permission they hold. The values are copied. An invalid role, or a role whose
// hierarchy differs from another definition with the same name, makes
// [NewStore] fail.
//
// Example:
//
//	policy.Roles(Viewer, Editor, Admin)
func (policy *Builder) Roles(roles ...gate.Role) *Builder {
	policy.roles = append(policy.roles, roles...)

	return policy
}

// KeepOne protects roles against losing their last holder: a [Store.RevokeRole]
// that would leave one of them with no holder answers [ErrLastHolder] (409).
// Holders count through inclusion: with SuperAdmin including Admin, a user
// assigned SuperAdmin holds Admin, so the direct holder of Admin may lose it
// while that user remains, and revoking SuperAdmin from the last holder of
// either is refused. Each role must be defined by [Builder.Roles]. A role with
// no holder yet is not protected until it is first granted.
//
// It protects revokes only. [auth.UserProvider.Forget] still removes the user
// and publishes [HolderOrphaned]. Permissions granted directly ([Store.Grant])
// have no last-holder protection: grant authority that must never disappear,
// such as the [Builder.Management] permission, through a KeepOne role.
//
// On PostgreSQL and MySQL the check locks the holders' rows, so concurrent
// revokes are serialized. SQLite has no row locks: a concurrent revoke may fail
// with a busy or locked database error instead of waiting. That fails safe: the
// last holder is never removed.
//
// Example:
//
//	policy.Roles(Admin).KeepOne(Admin)
func (policy *Builder) KeepOne(roles ...gate.Role) *Builder {
	policy.keepOne = append(policy.keepOne, roles...)

	return policy
}

// CacheTTL caches the stored grants of each subject that [Store.Load] reads,
// for ttl in the store given to [NewStore]. Every write and forget through the
// store clears the subject's entry after its transaction commits, and a read
// inside a transaction of the store's database bypasses the cache (see
// [Store.Load]). Clearing races with readers: a concurrent [Store.Load] that read
// the rows just before the write committed can store them just after the
// clear. A change therefore applies at once in the common case and within ttl
// at worst; changes made to the table by other means also wait up to ttl. Zero,
// the default, reads the table on every call; a negative ttl makes [NewStore]
// fail.
// The cache holds what was granted, not what it means: definitions are applied
// on every read, so a changed role takes effect at once.
//
// Example:
//
//	policy.CacheTTL(time.Minute)
func (policy *Builder) CacheTTL(ttl time.Duration) *Builder {
	policy.cacheTTL = ttl

	return policy
}

// SelfService marks the self-service route as wanted: the facade of
// arkoren.dev/ark/iam then mounts GET /me/permissions ([Store.SelfHandler]). The
// default is false. It changes nothing for an application that mounts the
// handler itself.
//
// Example:
//
//	policy.SelfService(true)
func (policy *Builder) SelfService(enabled bool) *Builder {
	policy.selfService = enabled

	return policy
}

// Management turns the management handlers ([Store.UserHandler],
// [Store.GrantHandler] and the others) on and names the permission a
// caller must hold to use them. The facade of arkoren.dev/ark/iam then mounts
// the /users/{subject} routes, and the permission must be defined by the store.
// Unless the method is called the handlers panic at composition.
//
// Example:
//
//	policy.Roles(Admin).Management(UsersManage)
func (policy *Builder) Management(required gate.Permission) *Builder {
	policy.managed = true
	policy.management = required

	return policy
}

// validate checks the settings that do not need the catalog.
func (policy *Builder) validate() error {
	if err := storage.ValidInstance(policy.instance); err != nil {
		return err
	}
	if policy.cacheTTL < 0 {
		return errors.New("negative cache TTL")
	}
	for _, permission := range policy.defined {
		if err := permission.Err(); err != nil {
			return err
		}
	}
	for _, role := range slices.Concat(policy.roles, policy.keepOne) {
		if err := role.Err(); err != nil {
			return err
		}
	}
	if policy.managed {
		if err := policy.management.Err(); err != nil {
			return fmt.Errorf("management: %w", err)
		}
	}

	return nil
}
