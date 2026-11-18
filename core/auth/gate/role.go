package gate

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"arkoren.dev/core/auth"
)

// maxRoleName is the longest role name in bytes.
const maxRoleName = 64

// Role names a bundle of permissions, such as "editor", that a store assigns to
// users. It is an immutable value, normally a package-level variable, built with
// [NewRole]. A role grants permissions and may include other roles, which gives
// a hierarchy: an editor that includes viewer holds every viewer permission.
// Gate keeps no registry; a role exists because a variable declares it.
//
// Includes take already published roles, so a cycle cannot be expressed, and a
// hierarchy that names two different roles alike is invalid
// ([ErrDuplicateName]).
// [Role.Expand] lists the role and everything it includes, and
// [Role.Permissions] flattens their grants.
//
// The contract for any store that puts roles on an [auth.Identity]: the identity
// carries the expanded role set (the names of [Role.Expand] for every assigned
// role) in [auth.Identity.Roles] and the flattened permissions in
// [auth.Identity.Scopes]. [HasRole] compares names only, so HasRole(Viewer)
// passes for an editor only because the store put "viewer" on the identity.
//
// The zero Role has an empty name and is invalid.
//
// Example:
//
//	var (
//		PostsRead  = gate.NewPermission("posts:read")
//		PostsWrite = gate.NewPermission("posts:write")
//
//		Viewer = gate.NewRole("viewer", func(role *gate.RoleBuilder) {
//			role.Grants(PostsRead)
//		})
//		Editor = gate.NewRole("editor", func(role *gate.RoleBuilder) {
//			role.Includes(Viewer).Grants(PostsWrite)
//		})
//	)
type Role struct {
	// name is the role name exactly as given to [NewRole].
	name string

	// grants are the permissions granted directly, in call order, owned by the
	// role.
	grants []Permission

	// includes are the directly included roles, in call order, owned by the role.
	includes []Role

	// reached are the roles included directly or transitively, without the role
	// itself, one per name in [Role.Expand] order. It is computed once by
	// [NewRole]; the role itself cannot be stored here, because a value cannot
	// contain itself.
	reached []Role

	// permissions are the flattened grants of the role and of reached, one per
	// name, sorted by name, computed once by [NewRole].
	permissions []Permission

	// err is the first invalid grant or include, or the first name conflict in
	// the hierarchy, prefixed with the role name.
	err error
}

// RoleBuilder configures [NewRole] before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic and
// expanded nil or empty slices supply no edits. Methods append and return the
// same draft, and retaining it cannot change a published role.
//
// Example:
//
//	editor := gate.NewRole("editor", func(role *gate.RoleBuilder) {
//		role.Includes(Viewer).Grants(PostsWrite)
//	})
type RoleBuilder struct {
	// grants accumulates [RoleBuilder.Grants] arguments.
	grants []Permission

	// includes accumulates [RoleBuilder.Includes] arguments.
	includes []Role

	// err is the first invalid argument seen by the draft.
	err error
}

// RoleBuilderFunc configures a [RoleBuilder].
//
// Example:
//
//	writes := gate.RoleBuilderFunc(func(role *gate.RoleBuilder) {
//		role.Grants(PostsWrite)
//	})
//	editor := gate.NewRole("editor", writes)
type RoleBuilderFunc = func(*RoleBuilder)

// Grants appends permissions the role grants directly. The arguments are
// copied. An invalid permission (see [Permission.Err]) makes the role invalid;
// the first such error is kept. Granting the same permission twice is harmless.
//
// Example:
//
//	editor := gate.NewRole("editor", func(role *gate.RoleBuilder) {
//		role.Grants(PostsRead, PostsWrite)
//	})
func (role *RoleBuilder) Grants(permissions ...Permission) *RoleBuilder {
	for _, permission := range permissions {
		if err := permission.Err(); err != nil && role.err == nil {
			role.err = fmt.Errorf("grants an invalid permission: %w", err)
		}
	}

	role.grants = append(role.grants, permissions...)

	return role
}

// Includes appends roles whose permissions this role also holds. The arguments
// are copied. An invalid role (see [Role.Err]) makes this role invalid; the
// first such error is kept. Including the same role twice is harmless.
//
// Example:
//
//	admin := gate.NewRole("admin", func(role *gate.RoleBuilder) {
//		role.Includes(Editor).Grants(UsersManage)
//	})
func (role *RoleBuilder) Includes(roles ...Role) *RoleBuilder {
	for _, included := range roles {
		if err := included.Err(); err != nil && role.err == nil {
			role.err = fmt.Errorf("includes an invalid role: %w", err)
		}
	}

	role.includes = append(role.includes, roles...)

	return role
}

// NewRole returns the role called name, configured by callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. A valid
// name is 1 to 64 bytes of lowercase ASCII letters, digits, '_' and '-'.
// NewRole never panics for invalid input: the callbacks still run, and the
// first problem (the name first, then the first invalid grant or include, then
// the first name conflict in the hierarchy) is reported by [Role.Err]. NewRole
// computes the expansion and flattened permissions once, so [Role.Expand] and
// [Role.Permissions] only copy. The rules of this package panic when given an invalid
// role.
//
// Example:
//
//	viewer := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
//		role.Grants(PostsRead)
//	})
//	fmt.Println(viewer.Name(), viewer.Err())
func NewRole(name string, configure ...RoleBuilderFunc) Role {
	draft := &RoleBuilder{}

	for _, configure := range configure {
		configure(draft)
	}

	role := Role{
		name:     name,
		grants:   slices.Clone(draft.grants),
		includes: slices.Clone(draft.includes),
		err:      draft.err,
	}

	role.reach()

	if role.err != nil {
		role.err = fmt.Errorf("gate: role %q %w", name, role.err)
	}

	return role
}

// reach computes role.reached and role.permissions from the direct includes,
// which already carry their own expansion, so a diamond hierarchy costs one pass
// over each include's list instead of a re-expansion per path. The same role
// reached twice is kept once. A reached role named like role, or two different
// roles sharing a name (see sameRole), sets role.err to an error wrapping
// [ErrDuplicateName] unless an earlier error is already set.
func (role *Role) reach() {
	seen := map[string]Role{}

	for _, included := range role.includes {
		for _, each := range included.Expand() {
			if each.name == role.name {
				role.fail(fmt.Errorf("includes a role with its own name: %w", ErrDuplicateName))

				continue
			}

			previous, ok := seen[each.name]

			if !ok {
				seen[each.name] = each
				role.reached = append(role.reached, each)

				continue
			}

			if !sameRole(previous, each) {
				role.fail(fmt.Errorf("reaches two different roles named %q: %w", each.name, ErrDuplicateName))
			}
		}
	}

	role.permissions = slices.Clone(role.grants)

	for _, each := range role.reached {
		role.permissions = append(role.permissions, each.grants...)
	}

	slices.SortStableFunc(role.permissions, func(left, right Permission) int {
		return cmp.Compare(left.name, right.name)
	})

	role.permissions = slices.CompactFunc(role.permissions, func(left, right Permission) bool {
		return left.name == right.name
	})
}

// fail keeps err as the role's error unless an earlier one is set.
func (role *Role) fail(err error) {
	if role.err == nil {
		role.err = err
	}
}

// sameRole reports whether left and right define the same role: equal names,
// equal sets of directly granted permission names and equal sets of directly
// included role names. Order and repetition do not matter.
func sameRole(left, right Role) bool {
	return left.name == right.name &&
		slices.Equal(nameSet(left.grants), nameSet(right.grants)) &&
		slices.Equal(nameSet(left.includes), nameSet(right.includes))
}

// nameSet returns the sorted, deduplicated names of definitions.
func nameSet[T interface{ Name() string }](definitions []T) []string {
	names := make([]string, 0, len(definitions))

	for _, definition := range definitions {
		names = append(names, definition.Name())
	}

	slices.Sort(names)

	return slices.Compact(names)
}

// Name returns the role name, the string an identity carries for it. It is
// returned even when the role is invalid.
//
// Example:
//
//	fmt.Println(Editor.Name()) // editor
func (role Role) Name() string {
	return role.name
}

// Err returns nil for a valid role. Otherwise it returns an error wrapping
// [ErrInvalidName] for an invalid name or for the first invalid permission
// granted or role included, or wrapping [ErrDuplicateName] when the hierarchy
// includes a role with this role's name or two different roles with one name.
// The zero Role is invalid.
//
// Example:
//
//	broken := gate.NewRole("editor", func(role *gate.RoleBuilder) {
//		role.Grants(gate.NewPermission("Posts Write"))
//	})
//	fmt.Println(errors.Is(broken.Err(), gate.ErrInvalidName)) // true
func (role Role) Err() error {
	if !validName(role.name, maxRoleName, "_-") {
		return fmt.Errorf("%w: role %q", ErrInvalidName, role.name)
	}

	return role.err
}

// Includes returns a copy of the directly included roles, in the order they
// were added, without the roles those include; see [Role.Expand].
//
// Example:
//
//	for _, included := range Editor.Includes() {
//		fmt.Println(included.Name())
//	}
func (role Role) Includes() []Role {
	return slices.Clone(role.includes)
}

// Expand returns the role itself followed by every role it includes, directly
// or transitively, depth first in include order, each role once. A store puts
// these names on the identity of a user assigned the role. The list is computed
// once by [NewRole]; the result is a new slice and the roles in it are immutable
// values. When two different roles share a name the role is invalid (see
// [ErrDuplicateName]) and only the first of them is listed.
//
// Example:
//
//	for _, role := range Editor.Expand() {
//		fmt.Println(role.Name()) // editor, then viewer
//	}
func (role Role) Expand() []Role {
	return append([]Role{role}, role.reached...)
}

// Permissions returns every permission the role holds: its own grants and those
// of every role in [Role.Expand], one per name, sorted by name. A store puts
// these names in the identity's scopes for a user assigned the role. The list is
// computed once by [NewRole]; the result is a new slice.
//
// Example:
//
//	for _, permission := range Editor.Permissions() {
//		fmt.Println(permission.Name()) // posts:read, then posts:write
//	}
func (role Role) Permissions() []Permission {
	return slices.Clone(role.permissions)
}

// HasRole returns a rule over [auth.Identity] that allows an identity holding
// every required role, read from [auth.Identity.Roles] by name. It never expands
// a hierarchy itself: it relies on the identity carrying the expanded role set,
// as a store must (see [Role]). Use it with [Require], [Authorize],
// [Gate.Require] or [Bind]; it reads only the identity and does no I/O.
//
// It copies the role names before returning and panics at composition when no
// role is given or when any role's [Role.Err] is not nil.
//
// Roles normally come only with sessions ([auth.GuardBuilder.Grants]). A token
// identity carries no roles, so HasRole denies every token unless a custom
// authenticator sets them. Use [HasPermission] for authorization and keep role
// checks for session-only routes and user interfaces.
//
// Example:
//
//	r.With(gate.Require(gate.HasRole(Admin))).Get("/admin", dashboard)
func HasRole(required ...Role) func(context.Context, auth.Identity) (bool, error) {
	names := ruleNames("HasRole", required)

	return func(_ context.Context, identity auth.Identity) (bool, error) {
		return containsAll(identity.Roles(), names), nil
	}
}

// HasAnyRole returns a rule over [auth.Identity] that allows an identity holding
// at least one of roles, read from [auth.Identity.Roles]. It copies its
// arguments and panics at composition, like [HasRole], when none is given or
// one is invalid.
//
// Example:
//
//	r.With(gate.Require(gate.HasAnyRole(Editor, Admin))).Get("/drafts", drafts)
func HasAnyRole(roles ...Role) func(context.Context, auth.Identity) (bool, error) {
	names := ruleNames("HasAnyRole", roles)

	return func(_ context.Context, identity auth.Identity) (bool, error) {
		return containsAny(identity.Roles(), names), nil
	}
}
