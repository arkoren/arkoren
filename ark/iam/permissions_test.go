package iam_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/internal/apptest"
	"arkoren.dev/ark/iam/permission"
)

// Definitions of the facade's permission tests: admin includes editor, which
// includes viewer.
var (
	postsRead   = gate.NewPermission("posts:read")
	postsWrite  = gate.NewPermission("posts:write")
	usersManage = gate.NewPermission("users:manage")

	viewer = gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(postsRead)
	})
	editor = gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Includes(viewer).Grants(postsWrite)
	})
	admin = gate.NewRole("admin", func(role *gate.RoleBuilder) {
		role.Includes(editor).Grants(usersManage)
	})
)

// withPermissions enables passwords, registration, tokens and the permission
// store with every route, then applies configure.
func withPermissions(configure ...iam.BuilderFunc) iam.BuilderFunc {
	return func(identity *iam.Builder) {
		identity.Password(hasher(), registration)
		identity.Tokens()
		identity.Permissions(func(policy *permission.Builder) {
			policy.Roles(admin).KeepOne(admin).SelfService(true).Management(usersManage)
		})
		for _, apply := range configure {
			apply(identity)
		}
	}
}

// signedUp registers and verifies ada@example.com on a new device, grants role
// when it is not the zero role, and returns the device and the subject.
func signedUp(t *testing.T, sys *app, role gate.Role) (*apptest.Client, string) {
	t.Helper()

	device := sys.client.NewDevice()
	registerVerified(t, sys, device, "ada@example.com")
	subject := mustSubject(t, sys)
	if role.Name() != "" {
		if err := sys.identity.GrantRole(testContext(t), subject, role); err != nil {
			t.Fatalf("GrantRole: %v", err)
		}
	}

	return device, subject
}

// mintToken creates a personal token with scopes on device and returns its
// Authorization header, failing unless the reply has status want.
func mintToken(t *testing.T, device *apptest.Client, want int, scopes ...string) string {
	t.Helper()

	body := `{"name":"ci","scopes":["` + strings.Join(scopes, `","`) + `"]}`
	reply := device.Post("/tokens", body)
	if reply.Status != want {
		t.Fatalf("POST /tokens = %d %s", reply.Status, reply.Body)
	}

	return "Authorization: Bearer " + between(reply.Body, `"secret":"`, `"`)
}

// status returns the status of GET path on client with headers.
func status(client *apptest.Client, path string, headers ...string) int {
	return client.Do(http.MethodGet, path, "", headers...).Status
}

// TestPermissionsSessionCarriesGrants verifies a session of an editor passes
// the editor's permissions and the included viewer role, and nothing more.
func TestPermissionsSessionCarriesGrants(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, withPermissions())
		device, _ := signedUp(t, sys, editor)
		for path, want := range map[string]int{
			"/can/posts:write":  http.StatusNoContent,
			"/is/viewer":        http.StatusNoContent,
			"/can/users:manage": http.StatusForbidden,
			"/is/admin":         http.StatusForbidden,
		} {
			if got := status(device, path); got != want {
				t.Fatalf("GET %s = %d, want %d", path, got, want)
			}
		}
	})
}

// TestPermissionsSessionWithoutRoleIsForbidden verifies a user without grants
// fails permission and role checks.
func TestPermissionsSessionWithoutRoleIsForbidden(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "", withPermissions())
	device, _ := signedUp(t, sys, gate.Role{})
	if status(device, "/can/posts:read") != http.StatusForbidden || status(device, "/is/viewer") != http.StatusForbidden {
		t.Fatal("a user without grants passed a check")
	}
}

// TestPermissionsTokenCarriesOnlyGrantedScopes verifies a token minted with
// posts:read passes that permission only and never a role.
func TestPermissionsTokenCarriesOnlyGrantedScopes(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, withPermissions())
		device, _ := signedUp(t, sys, editor)
		header := mintToken(t, device, http.StatusCreated, "posts:read")
		client := sys.client.NewDevice()
		for path, want := range map[string]int{
			"/can/posts:read":  http.StatusNoContent,
			"/can/posts:write": http.StatusForbidden,
			"/is/editor":       http.StatusForbidden,
		} {
			if got := status(client, path, header); got != want {
				t.Fatalf("GET %s with the token = %d, want %d", path, got, want)
			}
		}
	})
}

// TestPermissionsTokenScopeAboveUserIsRefused verifies a viewer cannot mint a
// token with posts:write.
func TestPermissionsTokenScopeAboveUserIsRefused(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "", withPermissions())
	device, _ := signedUp(t, sys, viewer)
	reply := device.Post("/tokens", `{"name":"ci","scopes":["posts:write"]}`)
	if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "scope-not-granted") {
		t.Fatalf("POST /tokens = %d %s", reply.Status, reply.Body)
	}
}

// TestPermissionsRevokedRoleNarrowsToken verifies revoking a role removes its
// permissions from a token issued before, on the next request.
func TestPermissionsRevokedRoleNarrowsToken(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "", withPermissions())
	device, subject := signedUp(t, sys, editor)
	header := mintToken(t, device, http.StatusCreated, "posts:write")
	if err := sys.identity.RevokeRole(testContext(t), subject, editor); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}
	if got := status(sys.client.NewDevice(), "/can/posts:write", header); got != http.StatusForbidden {
		t.Fatalf("GET /can/posts:write after revoke = %d", got)
	}
}

// TestPermissionsExplicitGrantsReplacesCeiling verifies a Grants call in the
// Tokens callbacks replaces the permission ceiling.
func TestPermissionsExplicitGrantsReplacesCeiling(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "", withPermissions(func(identity *iam.Builder) {
		identity.Tokens(func(tokens *apitoken.Builder) {
			tokens.Grants(apitoken.AnyScope, "posts:write")
		})
	}))
	device, _ := signedUp(t, sys, viewer)
	mintToken(t, device, http.StatusCreated, "posts:write")
}

// TestPermissionsRoutesRegisterOnlyWhenEnabled verifies no permission route exists
// without SelfService and Management.
func TestPermissionsRoutesRegisterOnlyWhenEnabled(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "", func(identity *iam.Builder) {
		identity.Password(hasher(), registration)
		identity.Permissions(func(policy *permission.Builder) {
			policy.Roles(admin)
		})
	})
	device, _ := signedUp(t, sys, admin)
	for _, path := range []string{"/me/permissions", "/users/1/permissions"} {
		if got := status(device, path); got != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, got)
		}
	}
}

// TestPermissionsSelfRouteShape verifies /me/permissions for a session and for
// a token of the same user.
func TestPermissionsSelfRouteShape(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "", withPermissions())
	device, _ := signedUp(t, sys, editor)
	session := device.Do(http.MethodGet, "/me/permissions", "")
	if strings.TrimSpace(session.Body) != `{"roles":["editor","viewer"],"permissions":["posts:read","posts:write"]}` {
		t.Fatalf("session = %d %s", session.Status, session.Body)
	}
	header := mintToken(t, device, http.StatusCreated, "posts:read")
	token := sys.client.NewDevice().Do(http.MethodGet, "/me/permissions", "", header)
	if strings.TrimSpace(token.Body) != `{"roles":[],"permissions":["posts:read"]}` {
		t.Fatalf("token = %d %s", token.Status, token.Body)
	}
}

// TestPermissionsManagementRoutes verifies an admin manages another user, a
// token cannot, and the last admin cannot lose the role.
func TestPermissionsManagementRoutes(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, withPermissions())
		device, subject := signedUp(t, sys, admin)
		other, err := sys.identity.Directory().CreateVerified(ctx, "grace@example.com")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if reply := device.Do(http.MethodPut, "/users/"+other+"/roles/editor", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("PUT role = %d %s", reply.Status, reply.Body)
		}
		if ok, err := sys.identity.HasRole(ctx, other, viewer); err != nil || !ok {
			t.Fatalf("HasRole = %v, %v", ok, err)
		}
		header := mintToken(t, device, http.StatusCreated, "users:manage")
		if reply := sys.client.NewDevice().Do(http.MethodPut, "/users/"+other+"/roles/viewer", "", header); reply.Status != http.StatusUnauthorized {
			t.Fatalf("PUT with a token = %d %s", reply.Status, reply.Body)
		}
		if reply := device.Do(http.MethodDelete, "/users/"+subject+"/roles/admin", ""); reply.Status != http.StatusConflict {
			t.Fatalf("DELETE last admin = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestPermissionsManagementRefusesEscalation verifies a manager holding only
// users:manage cannot grant editor.
func TestPermissionsManagementRefusesEscalation(t *testing.T) {
	t.Parallel()

	manager := gate.NewRole("manager", func(role *gate.RoleBuilder) {
		role.Grants(usersManage)
	})
	sys := newApp(t, memoryDatabase(t), "", withPermissions(func(identity *iam.Builder) {
		identity.Permissions(func(policy *permission.Builder) {
			policy.Roles(admin, manager).Management(usersManage)
		})
	}))
	device, _ := signedUp(t, sys, manager)
	other, _ := sys.identity.Directory().CreateVerified(testContext(t), "grace@example.com")
	reply := device.Do(http.MethodPut, "/users/"+other+"/roles/editor", "")
	if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "not-grantable") {
		t.Fatalf("PUT = %d %s", reply.Status, reply.Body)
	}
}

// TestPermissionsDeleteAccountClearsGrants verifies DELETE /account removes the
// user's grants.
func TestPermissionsDeleteAccountClearsGrants(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "", withPermissions())
	device, subject := signedUp(t, sys, editor)
	if reply := device.Do(http.MethodDelete, "/account", ""); reply.Status != http.StatusNoContent {
		t.Fatalf("DELETE /account = %d %s", reply.Status, reply.Body)
	}
	assigned, err := sys.identity.Permissions().Assignments(testContext(t), subject)
	if err != nil || len(assigned.Roles) != 0 {
		t.Fatalf("assignments = %+v, %v", assigned, err)
	}
}

// TestPermissionsForwardersNeedTheStore verifies the forwarders fail with
// ErrInvalidConfig without Builder.Permissions.
func TestPermissionsForwardersNeedTheStore(t *testing.T) {
	t.Parallel()

	identity, err := newFacade(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.GrantRole(testContext(t), "1", editor); !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("GrantRole = %v", err)
	}
	if identity.Permissions() != nil {
		t.Fatal("Permissions is not nil")
	}
}

// TestPermissionsMigrateLast verifies the permission migration comes after every
// other package's.
func TestPermissionsMigrateLast(t *testing.T) {
	t.Parallel()

	identity, err := newFacade(t, withPermissions())
	if err != nil {
		t.Fatal(err)
	}
	migrations := identity.Migrations()
	last := migrations[len(migrations)-1].ID()
	if !strings.HasPrefix(last, "arkoren.dev:ark/access:permission/") {
		t.Fatalf("last migration = %s", last)
	}
}
