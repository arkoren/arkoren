package openapi_test

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"testing"

	"arkoren.dev/core/http/router/internal/openapi"
	"arkoren.dev/core/http/router/internal/snapshot"
)

// TestExportPreservesRegistrationDiagnosticOrder checks that contextual errors
// use registration order rather than sorted output paths, preserving identity.
func TestExportPreservesRegistrationDiagnosticOrder(t *testing.T) {
	t.Parallel()

	first := errors.New("first declaration")
	second := errors.New("second declaration")

	_, err := openapi.Export(snapshot.Document{Title: "Test", Version: "1"}, []snapshot.Route{
		{Method: "POST", Pattern: "/z", Operation: &snapshot.Operation{Err: first}},
		{Method: "GET", Pattern: "/a", Operation: &snapshot.Operation{Err: second}},
	})

	if !errors.Is(err, first) || err.Error() != "openapi POST /z: first declaration" {
		t.Fatalf("error = %v", err)
	}
}

// TestExportDetachesInferredParametersAndPreservesSchema checks export's
// working copy independently from public builder publication and JSON parsing.
func TestExportDetachesInferredParametersAndPreservesSchema(t *testing.T) {
	t.Parallel()

	value := map[string]any{
		"responses": map[string]any{
			"200": map[string]any{
				"description": "Result",
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": jsontext.Value(`{"$ref":"https://example.invalid/schema","minimum":9007199254740993,"custom":true}`),
					},
				},
			},
		},
	}

	data, err := openapi.Export(snapshot.Document{Title: "Test", Version: "1"}, []snapshot.Route{
		{Method: "COPY", Pattern: "/users/{id}", Operation: &snapshot.Operation{Value: value}},
	})

	if err != nil {
		t.Fatal(err)
	}

	if value["parameters"] != nil {
		t.Fatal("inference changed the snapshot")
	}

	if !bytes.Contains(data, []byte(`"additionalOperations":{"COPY":`)) ||
		!bytes.Contains(data, []byte(`9007199254740993`)) ||
		!bytes.Contains(data, []byte(`"custom":true`)) ||
		!bytes.Contains(data, []byte(`"required":true`)) {
		t.Fatalf("export = %s", data)
	}
}

// TestExportHiddenDiagnosticsRemainOmitted checks visibility before failures.
func TestExportHiddenDiagnosticsRemainOmitted(t *testing.T) {
	t.Parallel()

	data, err := openapi.Export(snapshot.Document{Title: "Test", Version: "1"}, []snapshot.Route{
		{Method: "GET", Pattern: "/bad/{rest...}", Operation: &snapshot.Operation{
			Hidden: true, Err: errors.New("hidden declaration"),
		}},
	})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `{"info":{"title":"Test","version":"1"},"openapi":"3.2.1","paths":{}}` {
		t.Fatalf("export = %s", data)
	}
}
