package iam_test

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/permission"
)

// TestRegisterExportsAnOperationForEveryRoute verifies that with every feature
// enabled, each route Register mounts carries an operation, the router exports
// the document without metadata errors, and every exported operation has a
// summary, tags and at least one response.
func TestRegisterExportsAnOperationForEveryRoute(t *testing.T) {
	t.Parallel()

	identity, err := newFacadeWith(t, both, func(identity *iam.Builder) {
		identity.Password(hasher(), registration).TwoFactor(totp.New("Example"), cipher(t)).Tokens()
		identity.Provider(fakeProvider{name: "fake"}).Provider(nativeProvider{fakeProvider{name: "native"}})
		identity.Permissions(func(policy *permission.Builder) {
			policy.Roles(admin).SelfService(true).Management(usersManage)
		})
		identity.Account(func(view *iam.AccountBuilder) { view.Permissions(true).TwoFactor(true).Login(true) })
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	routes := router.New()
	identity.Register(routes)

	data, err := routes.OpenAPI("IAM", "1")
	if err != nil {
		t.Fatalf("OpenAPI() error = %v", err)
	}

	var document struct {
		Paths map[string]map[string]struct {
			OperationID string                    `json:"operationId"`
			Summary     string                    `json:"summary"`
			Tags        []string                  `json:"tags"`
			Responses   map[string]map[string]any `json:"responses"`
		} `json:"paths"`
	}

	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	mounted := 0

	for _, route := range routes.Routes() {
		mounted++
		operation, ok := document.Paths[route.Pattern][strings.ToLower(route.Method)]

		if !ok {
			t.Errorf("%s %s has no operation in the document", route.Method, route.Pattern)

			continue
		}

		if operation.Summary == "" || len(operation.Tags) == 0 || len(operation.Responses) == 0 {
			t.Errorf("%s %s operation = %+v, want summary, tags and responses", route.Method, route.Pattern, operation)
		}

		if operation.OperationID != "" {
			t.Errorf("%s %s sets operation ID %q, which multi-method registrations must omit", route.Method, route.Pattern, operation.OperationID)
		}
	}

	if mounted < 37 {
		t.Fatalf("mounted %d routes, want at least 37", mounted)
	}
}
