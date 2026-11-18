package gate

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/auth"
)

// maxPermissionName is the longest permission name in bytes.
const maxPermissionName = 128

// ErrInvalidName reports a [Permission] or [Role] whose name breaks the naming
// rules, or a role that grants an invalid permission or includes a role with an
// invalid name. A role that includes a role invalid for another reason keeps
// that role's error instead, such as [ErrDuplicateName]. [Permission.Err] and
// [Role.Err] wrap it; compare with [errors.Is].
//
// Example:
//
//	if err := gate.NewPermission("Posts Write").Err(); errors.Is(err, gate.ErrInvalidName) {
//		return err
//	}
var ErrInvalidName = errors.New("gate: invalid name")

// ErrDuplicateName reports a [Role] whose hierarchy is ambiguous: it includes,
// directly or transitively, a role with its own name, or two different roles
// with the same name. Two roles are different when their names match but their
// directly granted permission names or directly included role names do not.
// The same role reached twice, as in a diamond, is not a duplicate. [Role.Err]
// wraps it; compare with [errors.Is].
//
// Example:
//
//	viewer := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
//		role.Grants(PostsRead)
//	})
//	other := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
//		role.Grants(PostsWrite)
//	})
//	admin := gate.NewRole("admin", func(role *gate.RoleBuilder) {
//		role.Includes(viewer, other)
//	})
//	fmt.Println(errors.Is(admin.Err(), gate.ErrDuplicateName)) // true
var ErrDuplicateName = errors.New("gate: duplicate name")

// Permission names one action a user may perform, such as "posts:write". It is
// an immutable value, normally a package-level variable shared by the code that
// checks it ([HasPermission]) and the store that grants it. Gate keeps no
// registry: a permission exists because a variable declares it.
//
// On an [auth.Identity] a permission is a scope: whoever builds the identity
// puts the names of the user's effective permissions in [auth.Identity.Scopes].
// That is why names are restricted to valid OAuth scope tokens. A session gets
// them from [auth.GuardBuilder.Grants]; an API token carries only the scopes it
// was granted.
//
// The zero Permission has an empty name and is invalid.
//
// Example:
//
//	var PostsWrite = gate.NewPermission("posts:write")
//
//	r.With(gate.Require(gate.HasPermission(PostsWrite))).Post("/posts", create)
type Permission struct {
	// name is the permission name exactly as given to [NewPermission].
	name string
}

// NewPermission returns the permission called name. A valid name is 1 to 128
// bytes of lowercase ASCII letters, digits, ':', '.', '_' and '-', such as
// "posts:write" or "billing.invoices:read". Uppercase, spaces and '@' are
// invalid; '@' is reserved for future per-resource grants. NewPermission never
// panics: an invalid name is kept and reported by [Permission.Err], and the
// rules of this package panic when given such a value.
//
// Example:
//
//	var PostsRead = gate.NewPermission("posts:read")
//	fmt.Println(PostsRead.Name(), PostsRead.Err())
func NewPermission(name string) Permission {
	return Permission{name: name}
}

// Name returns the permission name, the scope string that an identity carries
// for it. It is returned even when the name is invalid.
//
// Example:
//
//	fmt.Println(gate.NewPermission("posts:read").Name()) // posts:read
func (permission Permission) Name() string {
	return permission.name
}

// Err returns nil for a valid permission and otherwise an error wrapping
// [ErrInvalidName] that quotes the name. The zero Permission is invalid.
//
// Example:
//
//	err := gate.NewPermission("posts@42").Err()
//	fmt.Println(errors.Is(err, gate.ErrInvalidName)) // true
func (permission Permission) Err() error {
	if !validName(permission.name, maxPermissionName, ":._-") {
		return fmt.Errorf("%w: permission %q", ErrInvalidName, permission.name)
	}

	return nil
}

// HasPermission returns a rule over [auth.Identity] that allows an identity
// holding every required permission, read from [auth.Identity.Scopes]. Use it
// with [Require], [Authorize], [Gate.Require] or [Bind]; it reads only the
// identity and does no I/O.
//
// It copies the permission names before returning, so later caller edits cannot
// change authorization. It panics at composition when no permission is given or
// when any permission's [Permission.Err] is not nil, so a typo fails at startup
// instead of denying or allowing silently.
//
// A session identity holds the user's permissions when the guard has
// [auth.GuardBuilder.Grants]; a token identity holds only the scopes the token
// was granted, so the same rule also limits what a token can do. Prefer
// permission checks over role checks for authorization.
//
// Example:
//
//	r.With(gate.Require(gate.HasPermission(PostsWrite))).Post("/posts", create)
func HasPermission(required ...Permission) func(context.Context, auth.Identity) (bool, error) {
	names := ruleNames("HasPermission", required)

	return func(_ context.Context, identity auth.Identity) (bool, error) {
		return containsAll(identity.Scopes(), names), nil
	}
}

// HasAnyPermission returns a rule over [auth.Identity] that allows an identity
// holding at least one of permissions, read from [auth.Identity.Scopes]. It
// copies its arguments and panics at composition, like [HasPermission], when
// none is given or one is invalid.
//
// Example:
//
//	r.With(gate.Require(gate.HasAnyPermission(PostsWrite, PostsModerate))).Delete("/posts/{id}", remove)
func HasAnyPermission(permissions ...Permission) func(context.Context, auth.Identity) (bool, error) {
	names := ruleNames("HasAnyPermission", permissions)

	return func(_ context.Context, identity auth.Identity) (bool, error) {
		return containsAny(identity.Scopes(), names), nil
	}
}
