package wiring_test

import (
	"net/http"
	"testing"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/permission"
	"arkoren.dev/core/auth/gate"
)

// Definitions of the permission scenario: editor includes viewer and adds
// posts:write.
var (
	postsRead  = gate.NewPermission("posts:read")
	postsWrite = gate.NewPermission("posts:write")

	viewer = gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(postsRead)
	})
	editor = gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Includes(viewer).Grants(postsWrite)
	})
)

// TestPasswordLoginThenGrantRoleThenGate runs the permission path through the
// facade: a password login, then GrantRole, then a gate rule that passes on the
// same session with no new login, because the guard loads the grants on every
// request.
func TestPasswordLoginThenGrantRoleThenGate(t *testing.T) {
	t.Parallel()

	sys := newSystem(t, func(identity *iam.Builder) {
		identity.Permissions(func(policy *permission.Builder) {
			policy.Roles(editor)
		})
	})
	register(t, sys, sys.browser)
	if r := sys.browser.get("/posts/write"); r.status != http.StatusForbidden {
		t.Fatalf("before the grant = %d %s", r.status, r.body)
	}
	subject, _, err := sys.identity.Directory().Lookup(t.Context(), "ada@example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if err := sys.identity.GrantRole(t.Context(), subject, editor); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	if r := sys.browser.get("/posts/write"); r.status != http.StatusOK || r.body != "allowed" {
		t.Fatalf("after the grant = %d %s", r.status, r.body)
	}
}
