package hatchetworkflow

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

// TestBackendListsExactNamespacedWorkflow inspects the real SDK's HTTP query.
// A nearby workflow returned first must not become the UUID filter, and the
// backend must preserve actual row names for the driver's ownership check.
// The test is serial because SDK endpoint/TLS setup uses environment variables.
func TestBackendListsExactNamespacedWorkflow(t *testing.T) {
	queries := make(chan url.Values, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries <- r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		body := `{"rows":[{"name":"stage_arkoren-wf-order-other","metadata":{"id":"11111111-1111-1111-1111-111111111111"}},{"name":"stage_arkoren-wf-order","metadata":{"id":"22222222-2222-2222-2222-222222222222"}}]}`

		if strings.HasSuffix(r.URL.Path, "/workflow-runs") {
			body = `{"rows":[{"workflowName":"foreign_arkoren-wf-order","workflowRunExternalId":"33333333-3333-3333-3333-333333333333","createdAt":"2026-10-01T00:00:00Z"}]}`
		}

		if _, err := fmt.Fprint(w, body); err != nil {
			t.Errorf("write SDK response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	t.Setenv("HATCHET_CLIENT_SERVER_URL", server.URL)
	t.Setenv("HATCHET_CLIENT_HOST_PORT", "127.0.0.1:1")
	t.Setenv("HATCHET_CLIENT_TLS_STRATEGY", "none")

	claims, err := json.Marshal(map[string]any{
		"sub": "44444444-4444-4444-4444-444444444444", "exp": 4102444800,
		"server_url": server.URL, "grpc_broadcast_address": "127.0.0.1:1",
	})

	if err != nil {
		t.Fatal(err)
	}

	client, err := hatchet.NewClient(hatchet.WithToken("e30." + base64.RawURLEncoding.EncodeToString(claims) + ".test"))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := client.Close(context.Background()); err != nil {
			t.Errorf("close SDK client: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	rows, err := (hatchetBackend{
		client: client,
	}).list(ctx, "stage_arkoren-wf-order", "qualified-key", time.Unix(0, 0))

	if err != nil {
		t.Fatal(err)
	}

	if got := <-queries; got.Get("name") != "stage_arkoren-wf-order" {
		t.Fatalf("workflow query = %v", got)
	}

	if got := <-queries; got.Get("workflow_ids") != "22222222-2222-2222-2222-222222222222" || got.Get("idempotency_keys") != "qualified-key" {
		t.Fatalf("run query = %v", got)
	}

	if len(rows) != 1 || rows[0].Workflow != "foreign_arkoren-wf-order" {
		t.Fatalf("actual listed workflow = %+v", rows)
	}
}
