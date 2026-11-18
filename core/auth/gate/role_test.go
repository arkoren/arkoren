package gate_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
)

// Shared definitions: admin includes editor and viewer, editor includes viewer,
// so admin reaches viewer twice.
var (
	postsRead   = gate.NewPermission("posts:read")
	postsWrite  = gate.NewPermission("posts:write")
	usersManage = gate.NewPermission("users:manage")

	viewer = gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(postsRead)
	})
	editor = gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Includes(viewer).Grants(postsWrite, postsRead)
	})
	admin = gate.NewRole("admin", func(role *gate.RoleBuilder) {
		role.Grants(usersManage).Includes(editor, viewer)
	})
)

// roleNames returns the names of roles in order.
func roleNames(roles []gate.Role) []string {
	names := make([]string, 0, len(roles))

	for _, role := range roles {
		names = append(names, role.Name())
	}

	return names
}

// TestRoleValidNames verifies names of allowed bytes within 64 bytes are valid.
func TestRoleValidNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"a", "editor", "super_admin", "team-lead", "l33t", strings.Repeat("a", 64)} {
		if err := gate.NewRole(name).Err(); err != nil {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

// TestRoleInvalidNames verifies empty, too long, uppercase, ':' and '@' names
// are ErrInvalidName, and so is the zero role.
func TestRoleInvalidNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", strings.Repeat("a", 65), "Editor", "posts:editor", "editor@team", "team.lead", "a b"} {
		if err := gate.NewRole(name).Err(); !errors.Is(err, gate.ErrInvalidName) {
			t.Fatalf("%q: %v", name, err)
		}
	}

	if err := (gate.Role{}).Err(); !errors.Is(err, gate.ErrInvalidName) {
		t.Fatalf("zero role: %v", err)
	}
}

// TestRoleGrantingInvalidPermissionIsInvalid verifies an invalid grant makes
// the role invalid while the callbacks still run.
func TestRoleGrantingInvalidPermissionIsInvalid(t *testing.T) {
	t.Parallel()

	role := gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Grants(gate.NewPermission("Posts"))
	}, func(role *gate.RoleBuilder) {
		role.Grants(postsWrite)
	})

	if err := role.Err(); !errors.Is(err, gate.ErrInvalidName) || !strings.Contains(err.Error(), `"Posts"`) {
		t.Fatalf("err = %v", err)
	}
}

// TestRoleIncludingInvalidRoleIsInvalid verifies an invalid include makes the
// including role invalid.
func TestRoleIncludingInvalidRoleIsInvalid(t *testing.T) {
	t.Parallel()

	broken := gate.NewRole("Broken")
	role := gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Includes(broken)
	})

	if err := role.Err(); !errors.Is(err, gate.ErrInvalidName) || !strings.Contains(err.Error(), `"Broken"`) {
		t.Fatalf("err = %v", err)
	}
}

// TestRoleKeepsFirstError verifies a later invalid input cannot replace the
// first error.
func TestRoleKeepsFirstError(t *testing.T) {
	t.Parallel()

	role := gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Grants(gate.NewPermission("First")).Includes(gate.NewRole("Second")).Grants(gate.NewPermission("Third"))
	})

	if err := role.Err(); !strings.Contains(err.Error(), `"First"`) || strings.Contains(err.Error(), "Second") {
		t.Fatalf("err = %v", err)
	}
}

// TestRoleNameErrorComesFirst verifies an invalid name is reported before an
// invalid grant.
func TestRoleNameErrorComesFirst(t *testing.T) {
	t.Parallel()

	role := gate.NewRole("Editor", func(role *gate.RoleBuilder) {
		role.Grants(gate.NewPermission("Posts"))
	})

	if err := role.Err(); !strings.Contains(err.Error(), `role "Editor"`) || strings.Contains(err.Error(), "Posts") {
		t.Fatalf("err = %v", err)
	}
}

// TestRoleDiamondIsValid verifies the same role reached through two paths is
// valid and listed once.
func TestRoleDiamondIsValid(t *testing.T) {
	t.Parallel()

	auditor := gate.NewRole("auditor", func(role *gate.RoleBuilder) {
		role.Includes(viewer)
	})
	lead := gate.NewRole("lead", func(role *gate.RoleBuilder) {
		role.Includes(editor, auditor)
	})

	if err := lead.Err(); err != nil {
		t.Fatal(err)
	}

	if got := roleNames(lead.Expand()); !slices.Equal(got, []string{"lead", "editor", "viewer", "auditor"}) {
		t.Fatalf("expand = %v", got)
	}
}

// TestRoleEqualDefinitionsWithOneNameAreValid verifies two separately built
// roles with the same name, grants and includes are not a conflict.
func TestRoleEqualDefinitionsWithOneNameAreValid(t *testing.T) {
	t.Parallel()

	copied := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(postsRead, postsRead)
	})
	role := gate.NewRole("lead", func(role *gate.RoleBuilder) {
		role.Includes(viewer, copied)
	})

	if err := role.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestRoleConflictingSameNameIsInvalid verifies two different roles sharing a
// name in one hierarchy make the role invalid with ErrDuplicateName.
func TestRoleConflictingSameNameIsInvalid(t *testing.T) {
	t.Parallel()

	other := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(postsWrite)
	})
	role := gate.NewRole("lead", func(role *gate.RoleBuilder) {
		role.Includes(editor, other)
	})

	if err := role.Err(); !errors.Is(err, gate.ErrDuplicateName) || !strings.Contains(err.Error(), `"viewer"`) {
		t.Fatalf("err = %v", err)
	}
}

// TestRoleIncludingItsOwnNameIsInvalid verifies a role cannot include, even
// transitively, a role named like itself.
func TestRoleIncludingItsOwnNameIsInvalid(t *testing.T) {
	t.Parallel()

	role := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Includes(editor)
	})

	if err := role.Err(); !errors.Is(err, gate.ErrDuplicateName) {
		t.Fatalf("err = %v", err)
	}
}

// TestRoleExpandAndPermissionsReturnCopies verifies callers cannot change the
// precomputed lists.
func TestRoleExpandAndPermissionsReturnCopies(t *testing.T) {
	t.Parallel()

	admin.Expand()[1] = viewer
	admin.Permissions()[0] = usersManage

	if roleNames(admin.Expand())[1] != "editor" || admin.Permissions()[0].Name() != "posts:read" {
		t.Fatalf("lists changed: %v %v", admin.Expand(), admin.Permissions())
	}
}

// TestRoleIncludesReturnsDirectRoles verifies Includes lists only direct
// includes in call order, as a copy.
func TestRoleIncludesReturnsDirectRoles(t *testing.T) {
	t.Parallel()

	admin.Includes()[0] = viewer

	if got := roleNames(admin.Includes()); !slices.Equal(got, []string{"editor", "viewer"}) {
		t.Fatalf("includes = %v", got)
	}
}

// TestRoleExpandOrderAndDedup verifies Expand starts with the role, walks
// includes depth first and keeps the first role of each name.
func TestRoleExpandOrderAndDedup(t *testing.T) {
	t.Parallel()

	if got := roleNames(admin.Expand()); !slices.Equal(got, []string{"admin", "editor", "viewer"}) {
		t.Fatalf("expand = %v", got)
	}

	if got := roleNames(viewer.Expand()); !slices.Equal(got, []string{"viewer"}) {
		t.Fatalf("leaf expand = %v", got)
	}
}

// TestRolePermissionsFlattenSorted verifies Permissions merges own and included
// grants, one per name, sorted by name.
func TestRolePermissionsFlattenSorted(t *testing.T) {
	t.Parallel()

	var names []string

	for _, permission := range admin.Permissions() {
		names = append(names, permission.Name())
	}

	if !slices.Equal(names, []string{"posts:read", "posts:write", "users:manage"}) {
		t.Fatalf("permissions = %v", names)
	}
}

// TestRoleRetainedBuilderCannotChangePublishedRole verifies the draft is
// detached at publication.
func TestRoleRetainedBuilderCannotChangePublishedRole(t *testing.T) {
	t.Parallel()

	var retained *gate.RoleBuilder
	role := gate.NewRole("editor", func(role *gate.RoleBuilder) {
		retained = role
		role.Grants(postsRead)
	})

	retained.Grants(usersManage).Includes(admin)

	if len(role.Permissions()) != 1 || len(role.Includes()) != 0 {
		t.Fatalf("role changed: %v %v", role.Permissions(), role.Includes())
	}
}

// TestHasRoleRequiresAll verifies every required role must be among the
// identity's roles.
func TestHasRoleRequiresAll(t *testing.T) {
	t.Parallel()

	rule := gate.HasRole(editor, viewer)

	if ok, _ := rule(t.Context(), identityWith(nil, []string{"editor", "viewer"})); !ok {
		t.Fatal("denied an identity holding both")
	}

	if ok, _ := rule(t.Context(), identityWith(nil, []string{"editor"})); ok {
		t.Fatal("allowed an identity holding one")
	}
}

// TestHasAnyRoleRequiresOne verifies one held role is enough and none is
// denied.
func TestHasAnyRoleRequiresOne(t *testing.T) {
	t.Parallel()

	rule := gate.HasAnyRole(editor, admin)

	if ok, _ := rule(t.Context(), identityWith(nil, []string{"admin"})); !ok {
		t.Fatal("denied an identity holding one")
	}

	if ok, _ := rule(t.Context(), identityWith(nil, []string{"viewer"})); ok {
		t.Fatal("allowed an identity holding none")
	}
}

// TestHasRoleDeniesTokenIdentity verifies an identity carrying only scopes, as
// a token does, fails a role check even when it holds every role permission.
func TestHasRoleDeniesTokenIdentity(t *testing.T) {
	t.Parallel()

	token := identityWith([]string{"posts:read", "posts:write"}, nil)

	if ok, _ := gate.HasRole(editor)(t.Context(), token); ok {
		t.Fatal("token passed HasRole")
	}

	if ok, _ := gate.HasPermission(editor.Permissions()...)(t.Context(), token); !ok {
		t.Fatal("token failed HasPermission")
	}
}

// TestRoleRulesPanicWithoutArguments verifies both constructors reject an empty
// requirement at composition.
func TestRoleRulesPanicWithoutArguments(t *testing.T) {
	t.Parallel()

	mustPanic(t, func() { gate.HasRole() })
	mustPanic(t, func() { gate.HasAnyRole() })
}

// TestRoleRulesPanicOnInvalidRole verifies both constructors reject an invalid
// role at composition.
func TestRoleRulesPanicOnInvalidRole(t *testing.T) {
	t.Parallel()

	invalid := gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Grants(gate.NewPermission("Posts"))
	})

	mustPanic(t, func() { gate.HasRole(viewer, invalid) })
	mustPanic(t, func() { gate.HasAnyRole(invalid) })
}

// TestHasRoleCopiesArguments verifies later edits of the caller's slice cannot
// change the rule.
func TestHasRoleCopiesArguments(t *testing.T) {
	t.Parallel()

	required := []gate.Role{admin}
	rule := gate.HasRole(required...)
	required[0] = viewer

	if ok, _ := rule(t.Context(), identityWith(nil, []string{"viewer"})); ok {
		t.Fatal("rule followed caller edit")
	}
}

func ExampleNewRole() {
	postsRead := gate.NewPermission("posts:read")
	postsWrite := gate.NewPermission("posts:write")
	viewer := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(postsRead)
	})
	editor := gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Includes(viewer).Grants(postsWrite)
	})

	for _, role := range editor.Expand() {
		fmt.Println(role.Name())
	}

	for _, permission := range editor.Permissions() {
		fmt.Println(permission.Name())
	}
	// Output:
	// editor
	// viewer
	// posts:read
	// posts:write
}

func ExampleRole_Err() {
	broken := gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Grants(gate.NewPermission("Posts Write"))
	})

	fmt.Println(errors.Is(broken.Err(), gate.ErrInvalidName))
	// Output: true
}

func ExampleErrDuplicateName() {
	viewer := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(gate.NewPermission("posts:read"))
	})
	other := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(gate.NewPermission("posts:write"))
	})
	admin := gate.NewRole("admin", func(role *gate.RoleBuilder) {
		role.Includes(viewer, other)
	})

	fmt.Println(errors.Is(admin.Err(), gate.ErrDuplicateName))
	// Output: true
}

func ExampleRoleBuilderFunc() {
	writes := gate.RoleBuilderFunc(func(role *gate.RoleBuilder) {
		role.Grants(gate.NewPermission("posts:write"))
	})

	fmt.Println(len(gate.NewRole("editor", writes).Permissions()))
	// Output: 1
}

func ExampleHasRole() {
	viewer := gate.NewRole("viewer")
	editor := gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Includes(viewer)
	})

	// A store puts the expanded role set on the identity: editor and viewer.
	var held []string

	for _, role := range editor.Expand() {
		held = append(held, role.Name())
	}

	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Roles(held...)
	}))

	fmt.Println(gate.Authorize(ctx, gate.HasRole(viewer)))
	// Output: <nil>
}

func ExampleHasAnyRole() {
	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Roles("editor")
	}))

	rule := gate.HasAnyRole(gate.NewRole("editor"), gate.NewRole("admin"))

	fmt.Println(gate.Authorize(ctx, rule))
	// Output: <nil>
}
