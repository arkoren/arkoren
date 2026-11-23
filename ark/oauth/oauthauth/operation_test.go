package oauthauth

import (
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
)

// TestOperationsExportOpenAPI verifies the four handlers register with their
// operations, including a callback on GET and POST without an operation ID, and
// that the router exports a summary and the documented statuses for each.
func TestOperationsExportOpenAPI(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	guard := newGuard(t)
	client, _ := nativeGoogle(t, provider)

	routes := router.New()
	routes.Get("/auth/google", client.LoginHandler(), LoginOperation)
	routes.Methods([]string{http.MethodGet, http.MethodPost}, "/auth/google/callback", client.CallbackHandler(guard, idLogin), CallbackOperation)
	routes.Get("/auth/google/nonce", client.NonceHandler(), NonceOperation)
	routes.Post("/auth/google/native", client.NativeHandler(guard, idLogin), NativeOperation)

	data, err := routes.OpenAPI("OAuth", "1")
	if err != nil {
		t.Fatalf("OpenAPI() error = %v", err)
	}

	var document struct {
		Paths map[string]map[string]struct {
			OperationID string                    `json:"operationId"`
			Summary     string                    `json:"summary"`
			Responses   map[string]map[string]any `json:"responses"`
		} `json:"paths"`
	}

	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		"get /auth/google":           {"302", "400", "502", "503"},
		"get /auth/google/callback":  {"302", "400", "401"},
		"post /auth/google/callback": {"302", "400", "401"},
		"get /auth/google/nonce":     {"200", "401", "501"},
		"post /auth/google/native":   {"204", "202", "400", "401", "413", "422", "501"},
	}

	for route, statuses := range want {
		method, path, _ := strings.Cut(route, " ")
		operation, ok := document.Paths[path][method]

		if !ok {
			t.Fatalf("%s is missing from the document", route)
		}

		if operation.Summary == "" || operation.OperationID != "" {
			t.Errorf("%s summary = %q, operationId = %q", route, operation.Summary, operation.OperationID)
		}

		for _, status := range statuses {
			if _, ok := operation.Responses[status]; !ok {
				t.Errorf("%s does not declare status %s", route, status)
			}
		}
	}
}
