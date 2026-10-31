package router_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// TestMetadataFailureDoesNotChangeRuntime checks sticky declaration errors,
// handler execution, trailing slash matching and error-cause preservation.
func TestMetadataFailureDoesNotChangeRuntime(t *testing.T) {
	t.Parallel()

	cause := errors.New("schema unavailable")
	routes := router.New()
	routes.Get("/users", metadataHandler, func(operation *router.OperationBuilder) {
		operation.JSONBody(failedSchema{err: cause})
		operation.JSONBody(router.RawSchema(`true`))
	})

	response := routes.Record(httptest.NewRequest(http.MethodGet, "/users/", nil))
	t.Cleanup(func() {
		_ = response.Body.Close() // In-memory test response has no close failure.
	})

	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("metadata changed runtime: %d", response.StatusCode)
	}

	_, err := routes.OpenAPI("Test", "1")

	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "GET /users") {
		t.Fatalf("sticky contextual cause: %v", err)
	}
}

// TestOpenAPIProblemMatchesNegotiatedRepresentations checks wire-model
// metadata without serializing Details' Go structure or private error cause.
func TestOpenAPIProblemMatchesNegotiatedRepresentations(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/missing", func(http.ResponseWriter, *http.Request) error {
		return problem.NotFound()
	}, func(operation *router.OperationBuilder) {
		operation.Problem(problem.NotFound())
	})

	operation := exportedOperation(t, routes, "/missing", "get")
	response := operation["responses"].(map[string]any)["404"].(map[string]any)
	content := response["content"].(map[string]any)

	if len(content) != 3 || content["application/problem+xml"] != nil {
		t.Fatalf("negotiation metadata: %v", content)
	}

	for _, media := range []string{"application/problem+json", "application/json", "text/plain"} {
		request := httptest.NewRequest(http.MethodGet, "/missing", nil)
		request.Header.Set("Accept", media)

		recorder := httptest.NewRecorder()

		routes.ServeHTTP(recorder, request)

		if !strings.HasPrefix(recorder.Header().Get("Content-Type"), media) {
			t.Fatalf("%s negotiation: %v", media, recorder.Header())
		}
	}
}

// TestOperationCallbacksAllEntries runs the same ordered callback assertions
// through every registration API, without comparing differing expectations.
func TestOperationCallbacksAllEntries(t *testing.T) {
	t.Parallel()

	registrations := []func(*router.Router, ...router.OperationBuilderFunc){
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Get("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Head("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Post("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Put("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Patch("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Delete("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Connect("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Options("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Trace("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Query("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Method("COPY", "/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Methods([]string{"GET", "HEAD"}, "/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Any("/x", metadataHandler, configure...)
		},
		func(routes *router.Router, configure ...router.OperationBuilderFunc) {
			routes.Mount("/x", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), configure...)
		},
	}

	for _, register := range registrations {
		calls := 0
		var retained *router.OperationBuilder
		register(router.New(), func(operation *router.OperationBuilder) {
			calls++
			retained = operation
		}, func(operation *router.OperationBuilder) {
			if calls != 1 || retained != operation {
				t.Fatal("callbacks did not share one ordered draft")
			}

			calls++
		})

		if calls != 2 {
			t.Fatalf("callbacks invoked %d times", calls)
		}
	}
}

// TestOperationNilCallbackPanics distinguishes misuse from omitted callbacks.
func TestOperationNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	router.New().Get("/x", metadataHandler, nil)
}

// TestOperationNilSliceOmitsMetadata checks alias identity and nil expansion.
func TestOperationNilSliceOmitsMetadata(t *testing.T) {
	t.Parallel()

	var configure []func(*router.OperationBuilder)
	routes := router.New()
	routes.Get("/x", metadataHandler, configure...)

	if len(exportedDocument(t, routes)["paths"].(map[string]any)) != 0 {
		t.Fatal("nil callback slice added metadata")
	}
}

// TestOperationPublicationDetachesDraftSchemasExamplesAndTags mutates every
// caller-owned input after registration and verifies the captured document.
func TestOperationPublicationDetachesDraftSchemasExamplesAndTags(t *testing.T) {
	t.Parallel()

	routes := router.New()
	schema := router.RawSchema(`{"const":9007199254740993,"$ref":"https://example.invalid/schema"}`)
	tags := []string{"original"}
	example := map[string]string{"name": "Ada"}
	var retained *router.OperationBuilder
	routes.Post("/users", metadataHandler, func(operation *router.OperationBuilder) {
		retained = operation
		operation.Tags(tags...).JSONBody(schema).Example("application/json", "user", example)
	})

	before, err := routes.OpenAPI("Users", "1")

	if err != nil {
		t.Fatal(err)
	}

	schema[0] = '!'
	tags[0] = "changed"
	example["name"] = "Grace"
	retained.Tags("late").Summary("late").Hidden(true).JSONBody(router.RawSchema(`false`))

	after, err := routes.OpenAPI("Users", "1")

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(before, after) || !bytes.Contains(after, []byte(`9007199254740993`)) {
		t.Fatalf("publication changed or numeric schema lost: %s", after)
	}
}

// TestOperationMediaResponsesAndBodylessResponse checks replacement and
// accumulation without inventing bodies for status-only responses.
func TestOperationMediaResponsesAndBodylessResponse(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Post("/users", metadataHandler, func(operation *router.OperationBuilder) {
		operation.JSONBody(router.RawSchema(`true`)).BodyRequired(true)
		operation.Body("text/plain", router.RawSchema(`{"type":"string"}`))
		operation.JSONResponse(200, router.RawSchema(`{"type":"object"}`))
		operation.MediaResponse(200, "text/plain", router.RawSchema(`{"type":"string"}`))
		operation.ResponseHeader(200, "X-Request-ID", router.RawSchema(`{"type":"string"}`))
		operation.Response(200, "A user").Response(204, "")
	})

	operation := exportedOperation(t, routes, "/users", "post")
	body := operation["requestBody"].(map[string]any)

	if body["required"] != true || len(body["content"].(map[string]any)) != 2 {
		t.Fatalf("body: %v", body)
	}

	responses := operation["responses"].(map[string]any)
	response := responses["200"].(map[string]any)

	if response["description"] != "A user" || len(response["content"].(map[string]any)) != 2 {
		t.Fatalf("response: %v", response)
	}

	if response["headers"].(map[string]any)["X-Request-ID"] == nil {
		t.Fatal("missing response header")
	}

	if responses["204"].(map[string]any)["content"] != nil {
		t.Fatal("body fabricated for bodyless response")
	}
}

// TestOperationResponseHeaderReplacesCaseInsensitiveName verifies HTTP header
// identity does not depend on declaration casing, while the latest spelling and
// schema remain descriptive metadata rather than runtime response headers.
func TestOperationResponseHeaderReplacesCaseInsensitiveName(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/headers", metadataHandler, func(operation *router.OperationBuilder) {
		operation.ResponseHeader(http.StatusOK, "X-Limit", router.RawSchema(`{"type":"integer"}`))
		operation.ResponseHeader(http.StatusOK, "x-limit", router.RawSchema(`{"type":"string"}`))
	})

	operation := exportedOperation(t, routes, "/headers", "get")
	response := operation["responses"].(map[string]any)["200"].(map[string]any)
	headers := response["headers"].(map[string]any)

	if len(headers) != 1 {
		t.Fatalf("headers = %v, want one case-insensitive declaration", headers)
	}

	header := headers["x-limit"].(map[string]any)

	if schema := header["schema"].(map[string]any); schema["type"] != "string" {
		t.Fatalf("schema = %v, want the last declaration", schema)
	}

	if got := serve(routes, http.MethodGet, "/headers").Header().Get("X-Limit"); got != "" {
		t.Fatalf("descriptive metadata changed runtime header: %q", got)
	}
}

// TestOperationResponseHeaderDoesNotFoldInvalidUnicodeName verifies HTTP's ASCII
// case folding cannot erase an invalid Unicode header through a later valid
// name that happens to have the same Unicode case fold.
func TestOperationResponseHeaderDoesNotFoldInvalidUnicodeName(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/headers", metadataHandler, func(operation *router.OperationBuilder) {
		operation.ResponseHeader(http.StatusOK, "X-K", router.RawSchema(`true`))
		operation.ResponseHeader(http.StatusOK, "X-K", router.RawSchema(`true`))
	})

	_, err := routes.OpenAPI("Headers", "1")

	if err == nil || !strings.Contains(err.Error(), "invalid response header") {
		t.Fatalf("error = %v, want invalid response header", err)
	}
}

// TestOperationExamplesAreCapturedWithoutEvaluation permits a descriptive
// example that does not satisfy its schema while capturing its JSON value.
func TestOperationExamplesAreCapturedWithoutEvaluation(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Post("/users", metadataHandler, func(operation *router.OperationBuilder) {
		operation.JSONBody(router.RawSchema(`false`)).Example("application/json", "sample", "not validated")
	})

	operation := exportedOperation(t, routes, "/users", "post")
	content := operation["requestBody"].(map[string]any)["content"].(map[string]any)
	example := content["application/json"].(map[string]any)["examples"].(map[string]any)["sample"].(map[string]any)

	if example["value"] != "not validated" {
		t.Fatalf("example: %v", example)
	}
}

// TestOperationInvalidExampleEncodingIsSticky checks declaration failures
// remain errors even when later calls supply a valid replacement example.
func TestOperationInvalidExampleEncodingIsSticky(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Post("/users", metadataHandler, func(operation *router.OperationBuilder) {
		operation.JSONBody(router.RawSchema(`true`)).Example("application/json", "sample", make(chan int))
		operation.Example("application/json", "sample", "valid")
	})

	if _, err := routes.OpenAPI("Test", "1"); err == nil {
		t.Fatal("example encoding error was cleared")
	}
}

// TestOperationInvalidMediaTypeRejected checks the supported envelope's
// media-type boundary, independently from raw JSON Schema syntax.
func TestOperationInvalidMediaTypeRejected(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Post("/users", metadataHandler, func(operation *router.OperationBuilder) {
		operation.Body("not-a-media-type", router.RawSchema(`true`))
	})

	_, err := routes.OpenAPI("Test", "1")

	if err == nil || !strings.Contains(err.Error(), "media type") {
		t.Fatalf("error: %v", err)
	}
}

// TestOperationInvalidStatusStaysSticky checks status validation at declaration
// time rather than clearing an earlier error on a later response declaration.
func TestOperationInvalidStatusStaysSticky(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/users", metadataHandler, func(operation *router.OperationBuilder) {
		operation.Response(0, "bad").Response(http.StatusOK, "good")
	})

	_, err := routes.OpenAPI("Test", "1")

	if err == nil || !strings.Contains(err.Error(), "response status") {
		t.Fatalf("error: %v", err)
	}
}

// TestOperationMissingFinalPathParameterRejected checks declarations against
// the final route rather than allowing stale parameter names in output.
func TestOperationMissingFinalPathParameterRejected(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/users/{id}", metadataHandler, func(operation *router.OperationBuilder) {
		operation.Parameter("other", "path", router.RawSchema(`true`))
	})

	_, err := routes.OpenAPI("Test", "1")

	if err == nil || !strings.Contains(err.Error(), "absent from final path") {
		t.Fatalf("error: %v", err)
	}
}

// TestOperationDuplicateHeaderParameterRejected honors header case-insensitivity.
func TestOperationDuplicateHeaderParameterRejected(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/users", metadataHandler, func(operation *router.OperationBuilder) {
		operation.Parameter("X-ID", "header", router.RawSchema(`true`))
		operation.Parameter("x-id", "header", router.RawSchema(`true`))
	})

	_, err := routes.OpenAPI("Test", "1")

	if err == nil || !strings.Contains(err.Error(), "duplicate parameter") {
		t.Fatalf("error: %v", err)
	}
}

// TestOpenAPISecurityRequiresExplicitSchemes rejects implicit policy inference.
func TestOpenAPISecurityRequiresExplicitSchemes(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/users", metadataHandler, func(operation *router.OperationBuilder) {
		operation.Security("bearer")
	})

	_, err := routes.OpenAPI("Test", "1")

	if err == nil || !strings.Contains(err.Error(), "undefined security scheme") {
		t.Fatalf("error: %v", err)
	}
}
