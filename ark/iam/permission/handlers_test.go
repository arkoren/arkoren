package permission_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"

	"arkoren.dev/ark/iam/internal/apptest"
	"arkoren.dev/ark/iam/permission"
)

// manager holds users:manage and nothing else, so it cannot hand out editor;
// owner holds no permission at all and carries authority only by name.
var (
	manager = gate.NewRole("manager", func(role *gate.RoleBuilder) {
		role.Grants(usersManage)
	})
	owner = gate.NewRole("owner")
)

// server is a fixture behind HTTP: a guard that loads grants from the store, a
// bearer authenticator that accepts the token "token" as user 1 with the scopes
// users:manage and posts:read, and every handler of the package.
type server struct {
	*fixture
	client *apptest.Client
}

// newServer serves the handlers of a store with management enabled.
func newServer(t *testing.T) *server {
	t.Helper()

	store := cache.New(memorycache.New())
	sys := newFixtureOn(t, sqliteDB(t), "", store, func(policy *permission.Builder) {
		policy.Roles(manager, owner).Management(true, usersManage)
	})
	guard, err := auth.NewGuard(sys.provider, store, func(policy *auth.GuardBuilder) {
		policy.Grants(sys.store.Load)
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	token := auth.Bearer(func(_ context.Context, secret string) (auth.Identity, error) {
		if secret != "token" {
			return auth.Identity{}, auth.ErrUnauthenticated
		}

		return auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
			principal.Scopes("users:manage", "posts:read", "not:defined")
		}), nil
	})
	client := apptest.Serve(t, store, []auth.Authenticator{guard.Authenticator(), token}, func(r *router.Router) {
		r.Post("/dev-login/{subject}", func(w http.ResponseWriter, r *http.Request) error {
			outcome, err := guard.Begin(r, r.PathValue("subject"), auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
			if err != nil {
				return err
			}

			return outcome.Response(w)
		})
		r.Get("/me/permissions", sys.store.SelfHandler())
		r.Get("/users/{subject}/permissions", sys.store.UserHandler())
		r.Put("/users/{subject}/permissions/{name}", sys.store.GrantHandler(guard))
		r.Delete("/users/{subject}/permissions/{name}", sys.store.RevokeHandler(guard))
		r.Put("/users/{subject}/roles/{name}", sys.store.GrantRoleHandler(guard))
		r.Delete("/users/{subject}/roles/{name}", sys.store.RevokeRoleHandler(guard))
	})

	return &server{fixture: sys, client: client}
}

// login gives subject the roles, logs them in on a new device and returns it.
func (sys *server) login(t *testing.T, subject string, roles ...gate.Role) *apptest.Client {
	t.Helper()

	for _, role := range roles {
		if err := sys.store.GrantRole(testContext(t), subject, role); err != nil {
			t.Fatalf("GrantRole: %v", err)
		}
	}
	device := sys.client.NewDevice()
	if reply := device.Post("/dev-login/"+subject, ""); reply.Status != http.StatusNoContent {
		t.Fatalf("login = %d %s", reply.Status, reply.Body)
	}

	return device
}

// selfReply is the body of GET /me/permissions.
type selfReply struct {
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// TestSelfHandlerShowsSessionGrants verifies a session sees its expanded roles
// and permissions.
func TestSelfHandlerShowsSessionGrants(t *testing.T) {
	t.Parallel()

	sys := newServer(t)
	device := sys.login(t, "2", editor)
	got := apptest.JSON[selfReply](t, device.Do(http.MethodGet, "/me/permissions", ""))
	if !slices.Equal(got.Roles, []string{"editor", "viewer"}) || !slices.Equal(got.Permissions, []string{"posts:read", "posts:write"}) {
		t.Fatalf("self = %+v", got)
	}
}

// TestSelfHandlerTokenShowsDefinedScopesOnly verifies a token shows its defined
// scopes and no roles.
func TestSelfHandlerTokenShowsDefinedScopesOnly(t *testing.T) {
	t.Parallel()

	sys := newServer(t)
	reply := sys.client.Do(http.MethodGet, "/me/permissions", "", "Authorization: Bearer token")
	if strings.TrimSpace(reply.Body) != `{"roles":[],"permissions":["users:manage","posts:read"]}` {
		t.Fatalf("self = %d %s", reply.Status, reply.Body)
	}
}

// TestSelfHandlerAnonymousIsUnauthorized verifies 401 without an identity.
func TestSelfHandlerAnonymousIsUnauthorized(t *testing.T) {
	t.Parallel()

	if reply := newServer(t).client.Do(http.MethodGet, "/me/permissions", ""); reply.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d", reply.Status)
	}
}

// userReply is the body of GET /users/{subject}/permissions.
type userReply struct {
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
	Effective   []string `json:"effective"`
}

// TestManagementGrantsAndRevokes verifies an admin grants and revokes a role
// and a permission and reads the result.
func TestManagementGrantsAndRevokes(t *testing.T) {
	t.Parallel()

	sys := newServer(t)
	device := sys.login(t, "1", admin)
	for _, path := range []string{"/users/2/roles/editor", "/users/2/permissions/users:manage"} {
		if reply := device.Do(http.MethodPut, path, ""); reply.Status != http.StatusNoContent {
			t.Fatalf("PUT %s = %d %s", path, reply.Status, reply.Body)
		}
	}
	got := apptest.JSON[userReply](t, device.Do(http.MethodGet, "/users/2/permissions", ""))
	if !slices.Equal(got.Roles, []string{"editor"}) || !slices.Equal(got.Permissions, []string{"users:manage"}) || len(got.Effective) != 3 {
		t.Fatalf("user = %+v", got)
	}
	if reply := device.Do(http.MethodDelete, "/users/2/roles/editor", ""); reply.Status != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", reply.Status, reply.Body)
	}
	if roles, _, _ := sys.store.Load(testContext(t), "2"); roles != nil {
		t.Fatalf("roles after revoke = %v", roles)
	}
}

// TestManagementRefusesTokens verifies a token holding the management
// permission cannot manage.
func TestManagementRefusesTokens(t *testing.T) {
	t.Parallel()

	sys := newServer(t)
	reply := sys.client.Do(http.MethodPut, "/users/2/permissions/posts:read", "", "Authorization: Bearer token")
	if reply.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d %s", reply.Status, reply.Body)
	}
}

// TestManagementRequiresPermission verifies a session without users:manage is
// forbidden.
func TestManagementRequiresPermission(t *testing.T) {
	t.Parallel()

	sys := newServer(t)
	device := sys.login(t, "2", editor)
	if reply := device.Do(http.MethodGet, "/users/3/permissions", ""); reply.Status != http.StatusForbidden {
		t.Fatalf("status = %d %s", reply.Status, reply.Body)
	}
}

// TestManagementRefusesEscalation verifies a manager cannot grant a role whose
// permissions they do not hold.
func TestManagementRefusesEscalation(t *testing.T) {
	t.Parallel()

	sys := newServer(t)
	device := sys.login(t, "1", manager)
	reply := device.Do(http.MethodPut, "/users/2/roles/editor", "")
	if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "permission:not-grantable") {
		t.Fatalf("status = %d %s", reply.Status, reply.Body)
	}
}

// TestManagementAnswers verifies the unknown-name, unknown-user and last-holder
// answers.
func TestManagementAnswers(t *testing.T) {
	t.Parallel()

	sys := newServer(t)
	device := sys.login(t, "1", admin)
	for path, status := range map[string]int{
		"/users/2/roles/ghost":          http.StatusUnprocessableEntity,
		"/users/404/roles/editor":       http.StatusNotFound,
		"/users/2/permissions/no:thing": http.StatusUnprocessableEntity,
	} {
		if reply := device.Do(http.MethodPut, path, ""); reply.Status != status {
			t.Fatalf("PUT %s = %d %s", path, reply.Status, reply.Body)
		}
	}
	if reply := device.Do(http.MethodDelete, "/users/1/roles/admin", ""); reply.Status != http.StatusConflict {
		t.Fatalf("last holder = %d %s", reply.Status, reply.Body)
	}
}

// TestManagementHandlersNeedManagement verifies the handlers panic at
// composition without Builder.Management.
func TestManagementHandlersNeedManagement(t *testing.T) {
	t.Parallel()

	sys := newFixture(t, sqliteDB(t), "")
	defer func() {
		if recover() == nil {
			t.Fatal("UserHandler did not panic")
		}
	}()
	sys.store.UserHandler()
}

// TestManagementRefusesRoleHeldOnlyByName verifies a manager without owner can
// neither grant nor revoke owner, a role that holds no permission.
func TestManagementRefusesRoleHeldOnlyByName(t *testing.T) {
	t.Parallel()

	sys := newServer(t)
	device := sys.login(t, "1", manager)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		reply := device.Do(method, "/users/2/roles/owner", "")
		if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "permission:not-grantable") {
			t.Fatalf("%s = %d %s", method, reply.Status, reply.Body)
		}
	}
}

// TestManagementGrantsRoleItHolds verifies a manager who holds owner can grant
// it.
func TestManagementGrantsRoleItHolds(t *testing.T) {
	t.Parallel()

	sys := newServer(t)
	device := sys.login(t, "1", manager, owner)
	if reply := device.Do(http.MethodPut, "/users/2/roles/owner", ""); reply.Status != http.StatusNoContent {
		t.Fatalf("PUT = %d %s", reply.Status, reply.Body)
	}
}
