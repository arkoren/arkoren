package permission

import (
	"errors"
	"net/http"

	"arkoren.dev/foundation/problem"

	"arkoren.dev/ark/iam/internal/storage"
)

// ErrNotMigrated is returned by [Store.Ping] when the table does not exist. Add
// [Store.Migrations] to the application's migrator and run them.
//
// Example:
//
//	if errors.Is(store.Ping(ctx), permission.ErrNotMigrated) {
//		// run the migrator
//	}
var ErrNotMigrated = storage.ErrNotMigrated

// ErrInvalidConfig is wrapped by the errors of [NewStore] for an unusable
// configuration: a missing dependency, an invalid instance, an invalid
// definition, two different definitions sharing a name, a [Builder.KeepOne] or
// [Builder.Management] value the store does not define, or a negative
// [Builder.CacheTTL].
//
// Example:
//
//	_, err := permission.NewStore(nil, nil, nil)
//	fmt.Println(errors.Is(err, permission.ErrInvalidConfig)) // true
var ErrInvalidConfig = errors.New("permission: invalid configuration")

// ErrUnknownGrant is returned when a permission or role is not defined by the
// store ([Builder.Define], [Builder.Roles] and the roles and permissions they
// reach), is invalid, or is a different definition that shares a defined name.
// It is a problem with status 422; the detail does not name the value.
//
// Example:
//
//	err := store.Grant(ctx, subject, gate.NewPermission("unknown:thing"))
//	fmt.Println(errors.Is(err, permission.ErrUnknownGrant)) // true
var ErrUnknownGrant = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:permission:unknown-grant")
	details.Title("Unknown permission or role")
	details.Detail("The permission or role is not defined.")
	details.Status(http.StatusUnprocessableEntity)
})

// ErrLastHolder is returned by [Store.RevokeRole] when the subject is the only
// holder of a role named in [Builder.KeepOne], so revoking it would leave the
// role without holders. Grant the role to someone else first, in the same
// transaction if needed. It is a problem with status 409.
//
// Example:
//
//	if errors.Is(store.RevokeRole(ctx, subject, Admin), permission.ErrLastHolder) {
//		// grant Admin to another user first
//	}
var ErrLastHolder = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:permission:last-holder")
	details.Title("Last holder")
	details.Detail("This role must keep at least one holder.")
	details.Status(http.StatusConflict)
})

// ErrNotGrantable is returned by the management handlers when the caller tries
// to grant or revoke more than they hold: a permission they do not hold, or a
// role unless they hold all of its permissions and every role it expands to. It
// is a problem with status 403.
//
// Example:
//
//	if errors.Is(err, permission.ErrNotGrantable) {
//		// the caller would have escalated their own authority
//	}
var ErrNotGrantable = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:permission:not-grantable")
	details.Title("Not grantable")
	details.Detail("You may not grant or revoke a permission you do not hold.")
	details.Status(http.StatusForbidden)
})

// ErrUserNotFound is returned by the management handlers when the subject in
// the path is not a user (auth.ErrUserNotFound from the provider). It is a
// problem with status 404.
//
// Example:
//
//	if errors.Is(err, permission.ErrUserNotFound) {
//		// no such user
//	}
var ErrUserNotFound = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:permission:user-not-found")
	details.Title("User not found")
	details.Detail("No such user.")
	details.Status(http.StatusNotFound)
})
