package gate_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
)

// identityWith returns an identity holding scopes and roles, built without a
// session as a bearer-token authenticator would.
func identityWith(scopes, roles []string) auth.Identity {
	return auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		principal.Scopes(scopes...).Roles(roles...)
	})
}

// mustPanic fails the test unless call panics.
func mustPanic(t *testing.T, call func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Fatal("call did not panic")
		}
	}()

	call()
}

// TestPermissionValidNames verifies names of allowed bytes within 128 bytes are
// valid.
func TestPermissionValidNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"a", "posts:write", "billing.invoices:read", "a_b-c", "0", strings.Repeat("a", 128)} {
		if err := gate.NewPermission(name).Err(); err != nil {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

// TestPermissionInvalidNames verifies empty, too long, uppercase, spaced and
// '@' names are ErrInvalidName, and so is the zero permission.
func TestPermissionInvalidNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", strings.Repeat("a", 129), "Posts", "posts write", "posts@42", "posts/write", "é"} {
		if err := gate.NewPermission(name).Err(); !errors.Is(err, gate.ErrInvalidName) {
			t.Fatalf("%q: %v", name, err)
		}
	}

	if err := (gate.Permission{}).Err(); !errors.Is(err, gate.ErrInvalidName) {
		t.Fatalf("zero permission: %v", err)
	}
}

// TestHasPermissionRequiresAll verifies every required permission must be among
// the identity's scopes.
func TestHasPermissionRequiresAll(t *testing.T) {
	t.Parallel()

	rule := gate.HasPermission(gate.NewPermission("posts:read"), gate.NewPermission("posts:write"))

	if ok, _ := rule(t.Context(), identityWith([]string{"posts:read", "posts:write"}, nil)); !ok {
		t.Fatal("denied an identity holding both")
	}

	if ok, _ := rule(t.Context(), identityWith([]string{"posts:read"}, nil)); ok {
		t.Fatal("allowed an identity holding one")
	}
}

// TestHasAnyPermissionRequiresOne verifies one held permission is enough and
// none is denied.
func TestHasAnyPermissionRequiresOne(t *testing.T) {
	t.Parallel()

	rule := gate.HasAnyPermission(gate.NewPermission("posts:write"), gate.NewPermission("posts:moderate"))

	if ok, _ := rule(t.Context(), identityWith([]string{"posts:moderate"}, nil)); !ok {
		t.Fatal("denied an identity holding one")
	}

	if ok, _ := rule(t.Context(), identityWith([]string{"posts:read"}, nil)); ok {
		t.Fatal("allowed an identity holding none")
	}
}

// TestPermissionRulesPanicWithoutArguments verifies both constructors reject an
// empty requirement at composition.
func TestPermissionRulesPanicWithoutArguments(t *testing.T) {
	t.Parallel()

	mustPanic(t, func() { gate.HasPermission() })
	mustPanic(t, func() { gate.HasAnyPermission() })
}

// TestPermissionRulesPanicOnInvalidPermission verifies both constructors reject
// an invalid permission at composition.
func TestPermissionRulesPanicOnInvalidPermission(t *testing.T) {
	t.Parallel()

	invalid := gate.NewPermission("Posts")

	mustPanic(t, func() { gate.HasPermission(gate.NewPermission("posts:read"), invalid) })
	mustPanic(t, func() { gate.HasAnyPermission(invalid) })
}

// TestHasPermissionCopiesArguments verifies later edits of the caller's slice
// cannot change the rule.
func TestHasPermissionCopiesArguments(t *testing.T) {
	t.Parallel()

	required := []gate.Permission{gate.NewPermission("admin")}
	rule := gate.HasPermission(required...)
	required[0] = gate.NewPermission("read")

	if ok, _ := rule(t.Context(), identityWith([]string{"read"}, nil)); ok {
		t.Fatal("rule followed caller edit")
	}
}

func ExampleNewPermission() {
	postsRead := gate.NewPermission("posts:read")

	fmt.Println(postsRead.Name(), postsRead.Err())
	// Output: posts:read <nil>
}

func ExamplePermission_Err() {
	err := gate.NewPermission("posts@42").Err()

	fmt.Println(errors.Is(err, gate.ErrInvalidName))
	// Output: true
}

func ExampleHasPermission() {
	postsRead := gate.NewPermission("posts:read")
	postsWrite := gate.NewPermission("posts:write")
	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Scopes("posts:read")
	}))

	fmt.Println(gate.Authorize(ctx, gate.HasPermission(postsRead)))
	fmt.Println(errors.Is(gate.Authorize(ctx, gate.HasPermission(postsRead, postsWrite)), gate.ErrForbidden))
	// Output:
	// <nil>
	// true
}

func ExampleHasAnyPermission() {
	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Scopes("posts:moderate")
	}))

	rule := gate.HasAnyPermission(gate.NewPermission("posts:write"), gate.NewPermission("posts:moderate"))

	fmt.Println(gate.Authorize(ctx, rule))
	// Output: <nil>
}
