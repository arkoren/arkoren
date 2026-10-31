package router_test

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/http/router"
)

// TestOpenAPISecuritySchemeCapturedAndDeclared checks explicit scheme output
// and detachment from both retained document drafts and original bytes.
func TestOpenAPISecuritySchemeCapturedAndDeclared(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/users", metadataHandler, func(operation *router.OperationBuilder) {
		operation.Security("bearer")
	})

	scheme := router.RawSchema(`{"type":"http","scheme":"bearer"}`)
	var retained *router.OpenAPIBuilder
	handler := routes.OpenAPIHandler("Test", "1", func(document *router.OpenAPIBuilder) {
		retained = document
		document.SecurityScheme("bearer", scheme)
	})

	scheme[0] = '!'
	retained.SecurityScheme("bearer", router.RawSchema(`false`))

	recorder := httptest.NewRecorder()

	if err := handler(recorder, httptest.NewRequest(http.MethodGet, "/spec", nil)); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(recorder.Body.String(), `"securitySchemes":{"bearer":{"scheme":"bearer","type":"http"}}`) {
		t.Fatalf("scheme output: %s", recorder.Body)
	}
}

// TestOpenAPIInvalidSecuritySchemeFailsEvenUnused checks configuration errors
// are explicit document errors, not silently skipped unused components.
func TestOpenAPIInvalidSecuritySchemeFailsEvenUnused(t *testing.T) {
	t.Parallel()

	_, err := router.New().OpenAPI("Test", "1", func(document *router.OpenAPIBuilder) {
		document.SecurityScheme("key", router.RawSchema(`{"type":"apiKey","name":"X-Key","in":"body"}`))
	})

	if err == nil || !strings.Contains(err.Error(), "security scheme") {
		t.Fatalf("error: %v", err)
	}
}

// metadataHandler is a deterministic successful handler for export scenarios.
func metadataHandler(writer http.ResponseWriter, _ *http.Request) error {
	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// exportedDocument decodes a generated document for structural assertions.
func exportedDocument(t *testing.T, routes *router.Router) map[string]any {
	t.Helper()

	data, err := routes.OpenAPI("Test", "1")

	if err != nil {
		t.Fatal(err)
	}

	var document map[string]any

	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	return document
}

// exportedOperation selects one operation from a decoded test document.
func exportedOperation(t *testing.T, routes *router.Router, path, method string) map[string]any {
	t.Helper()

	document := exportedDocument(t, routes)
	paths := document["paths"].(map[string]any)

	return paths[path].(map[string]any)[method].(map[string]any)
}

// TestOpenAPIOmitsUnannotatedAndHidden checks opt-in metadata visibility.
func TestOpenAPIOmitsUnannotatedAndHidden(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/plain", metadataHandler)
	routes.Get("/hidden", metadataHandler, func(operation *router.OperationBuilder) {
		operation.Hidden(true).JSONBody(router.RawSchema(`invalid`))
	})
	routes.Get("/shown", metadataHandler, func(operation *router.OperationBuilder) {
		operation.ID("shown").Summary("Shown").Description("Long description").Tags("public").Deprecated(true)
	})

	document := exportedDocument(t, routes)
	paths := document["paths"].(map[string]any)

	if len(paths) != 1 || document["openapi"] != "3.2.1" {
		t.Fatalf("unexpected document: %v", document)
	}

	operation := paths["/shown"].(map[string]any)["get"].(map[string]any)

	if operation["operationId"] != "shown" || operation["deprecated"] != true {
		t.Fatalf("metadata lost: %v", operation)
	}

	if _, ok := operation["responses"]; ok {
		t.Fatal("undeclared responses must not be fabricated")
	}

	if len(routes.Routes()) != 3 {
		t.Fatal("metadata changed route inspection")
	}
}

// TestOpenAPIFinalGroupParameters checks inherited prefixes and explicit
// parameter overrides, including inferred required string parameters.
func TestOpenAPIFinalGroupParameters(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Group("/teams/{team}", func(group *router.Router) {
		group.Clone().With().Get("/users/{id}", metadataHandler, func(operation *router.OperationBuilder) {
			operation.Parameter("id", "path", router.RawSchema(`{"type":"integer"}`))
		})
	})

	operation := exportedOperation(t, routes, "/teams/{team}/users/{id}", "get")
	parameters := operation["parameters"].([]any)

	if len(parameters) != 2 {
		t.Fatalf("parameters: %v", parameters)
	}

	team := parameters[1].(map[string]any)

	if team["name"] != "team" || team["required"] != true {
		t.Fatalf("group parameter: %v", team)
	}

	if team["schema"].(map[string]any)["type"] != "string" {
		t.Fatalf("inferred schema was not a JSON object: %v", team)
	}
}

// TestOpenAPIQueryAndAdditionalMethods checks the OpenAPI 3.2 method slots.
func TestOpenAPIQueryAndAdditionalMethods(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Query("/users", metadataHandler, func(operation *router.OperationBuilder) {})
	routes.Method("COPY", "/users", metadataHandler, func(operation *router.OperationBuilder) {})
	routes.Connect("/tunnel", metadataHandler, func(operation *router.OperationBuilder) {})

	paths := exportedDocument(t, routes)["paths"].(map[string]any)
	users := paths["/users"].(map[string]any)

	if users["query"] == nil || users["additionalOperations"].(map[string]any)["COPY"] == nil {
		t.Fatalf("method encoding: %v", users)
	}

	tunnel := paths["/tunnel"].(map[string]any)

	if tunnel["additionalOperations"].(map[string]any)["CONNECT"] == nil {
		t.Fatalf("CONNECT encoding: %v", tunnel)
	}
}

// TestOpenAPIDuplicateIDReportsRoute checks document-wide ID uniqueness.
func TestOpenAPIDuplicateIDReportsRoute(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Methods([]string{"GET", "POST"}, "/x", metadataHandler, func(operation *router.OperationBuilder) {
		operation.ID("shared")
	})

	_, err := routes.OpenAPI("Test", "1")

	if err == nil || !strings.Contains(err.Error(), "POST /x") || !strings.Contains(err.Error(), "duplicate operation ID") {
		t.Fatalf("error: %v", err)
	}
}

// TestOpenAPIEquivalentTemplatesRejectDifferentNames checks path collisions
// even when different methods let both registrations coexist in ServeMux.
func TestOpenAPIEquivalentTemplatesRejectDifferentNames(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/users/{id}", metadataHandler, func(operation *router.OperationBuilder) {})
	routes.Post("/users/{name}", metadataHandler, func(operation *router.OperationBuilder) {})

	_, err := routes.OpenAPI("Test", "1")

	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("error: %v", err)
	}
}

// TestOpenAPIOpaqueAnnotatedMountRejectsExport ensures catch-alls are never
// converted into misleading one-segment path templates.
func TestOpenAPIOpaqueAnnotatedMountRejectsExport(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Mount("/files", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		func(operation *router.OperationBuilder) {
			operation.Summary("Files")
		})

	_, err := routes.OpenAPI("Test", "1")

	if err == nil || !strings.Contains(err.Error(), "opaque") {
		t.Fatalf("error: %v", err)
	}
}

// TestOpenAPIUnannotatedCatchAllIsIgnored permits mixed application routers.
func TestOpenAPIUnannotatedCatchAllIsIgnored(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/files/{path...}", metadataHandler)
	routes.Get("/health", metadataHandler, func(operation *router.OperationBuilder) {})

	if len(exportedDocument(t, routes)["paths"].(map[string]any)) != 1 {
		t.Fatal("catch-all was advertised")
	}
}

// TestOpenAPIHandlerCapturesConfigurationButSnapshotsFirstRequest checks the
// two distinct once-only boundaries and successful document cache behavior.
func TestOpenAPIHandlerCapturesConfigurationButSnapshotsFirstRequest(t *testing.T) {
	t.Parallel()

	routes := router.New()
	calls := 0
	var retained *router.OpenAPIBuilder
	handler := routes.OpenAPIHandler("Test", "1", func(document *router.OpenAPIBuilder) {
		calls++
		retained = document
		document.Description("original")
	})
	retained.Description("late")
	routes.Get("/before", metadataHandler, func(operation *router.OperationBuilder) {})

	first := httptest.NewRecorder()

	if err := handler(first, httptest.NewRequest(http.MethodGet, "/openapi.json", nil)); err != nil {
		t.Fatal(err)
	}

	routes.Get("/after", metadataHandler, func(operation *router.OperationBuilder) {})

	second := httptest.NewRecorder()

	if err := handler(second, httptest.NewRequest(http.MethodGet, "/openapi.json", nil)); err != nil {
		t.Fatal(err)
	}

	if calls != 1 || first.Body.String() != second.Body.String() {
		t.Fatal("handler replayed configuration or invalidated cache")
	}

	if !strings.Contains(first.Body.String(), `"/before"`) || strings.Contains(first.Body.String(), `"/after"`) {
		t.Fatalf("wrong snapshot timing: %s", first.Body)
	}

	if !strings.Contains(first.Body.String(), "original") || strings.Contains(first.Body.String(), "late") {
		t.Fatalf("retained configuration changed handler: %s", first.Body)
	}
}

// TestOpenAPIHandlerConcurrentFailureIsCached checks that concurrent callers
// receive one cached error object rather than rerendering invalid metadata.
func TestOpenAPIHandlerConcurrentFailureIsCached(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/invalid", metadataHandler, func(operation *router.OperationBuilder) {
		operation.JSONBody(router.RawSchema(`null`))
	})
	var configurations atomic.Int32
	handler := routes.OpenAPIHandler("Test", "1", func(document *router.OpenAPIBuilder) {
		configurations.Add(1)
	})

	results := make(chan error, 16)
	var workers sync.WaitGroup

	for range cap(results) {
		workers.Go(func() {
			results <- handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/spec", nil))
		})
	}

	workers.Wait()
	close(results)
	var first error

	for err := range results {
		if err == nil {
			t.Fatal("invalid document served successfully")
		}

		if first == nil {
			first = err
		}

		if !errors.Is(err, first) {
			t.Fatal("error was recomputed rather than cached")
		}
	}

	if configurations.Load() != 1 {
		t.Fatal("configuration callback replayed")
	}
}

// TestOpenAPIHandlerReturnsWriteFailure preserves writer failures at the edge.
func TestOpenAPIHandlerReturnsWriteFailure(t *testing.T) {
	t.Parallel()

	handler := router.New().OpenAPIHandler("Test", "1")
	writer := &failingSpecWriter{header: http.Header{}}

	if err := handler(writer, httptest.NewRequest(http.MethodGet, "/spec", nil)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error: %v", err)
	}
}

// failingSpecWriter reports a stable write failure without external I/O.
type failingSpecWriter struct {
	// header retains the handler's declared content type.
	header http.Header
}

// Header returns the in-memory header map.
func (writer *failingSpecWriter) Header() http.Header {
	return writer.header
}

// WriteHeader accepts status commitment without side effects.
func (writer *failingSpecWriter) WriteHeader(int) {}

// Write simulates a disconnected client.
func (writer *failingSpecWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}
