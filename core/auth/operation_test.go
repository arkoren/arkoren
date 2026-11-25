package auth_test

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
)

// TestOperationsExportOpenAPI verifies the four guard handlers register with
// their operations, that the router exports them without metadata errors, and
// that each exported operation has a summary and its documented statuses.
func TestOperationsExportOpenAPI(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	routes := router.New()
	routes.Get("/login/pending", guard.PendingHandler(), auth.PendingOperation)
	routes.Post("/login/challenge", guard.ChallengeHandler(), auth.ChallengeOperation)
	routes.Post("/confirm", guard.ConfirmHandler(), auth.ConfirmOperation)
	routes.Post("/logout", guard.LogoutHandler(), auth.LogoutOperation)

	data, err := routes.OpenAPI("Auth", "1")
	if err != nil {
		t.Fatalf("OpenAPI() error = %v", err)
	}

	var document struct {
		Paths map[string]map[string]struct {
			Summary   string                    `json:"summary"`
			Responses map[string]map[string]any `json:"responses"`
		} `json:"paths"`
	}

	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		"get /login/pending":    {"200", "401"},
		"post /login/challenge": {"204", "400", "401", "403", "422"},
		"post /confirm":         {"204", "400", "401", "403", "422"},
		"post /logout":          {"204", "303", "500"},
	}

	for route, statuses := range want {
		method, path, _ := strings.Cut(route, " ")
		operation, ok := document.Paths[path][method]

		if !ok {
			t.Fatalf("%s is missing from the document", route)
		}

		if operation.Summary == "" {
			t.Errorf("%s has no summary", route)
		}

		for _, status := range statuses {
			if _, ok := operation.Responses[status]; !ok {
				t.Errorf("%s does not declare status %s", route, status)
			}
		}
	}
}
