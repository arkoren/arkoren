package contenttype_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/middleware/contenttype"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ExampleBuilderFunc configures the JSON policy and observes its default
// rejection through the ordinary HTTP seam rather than a private setting.
func ExampleBuilderFunc() {
	configure := contenttype.BuilderFunc(func(content *contenttype.Builder) {
		content.Allowed("application/json")
	})

	routes := router.New()
	routes.Use(contenttype.Middleware(configure))

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("data"))
	request.Header.Set("Content-Type", "text/plain")

	recorder := httptest.NewRecorder()

	routes.ServeHTTP(recorder, request)
	fmt.Println(recorder.Code)
	// Output: 415
}

// TestMiddlewarePreservesStatuslessProblem verifies that an absent advisory
// status does not discard custom problem members before HTTP rendering.
func TestMiddlewarePreservesStatuslessProblem(t *testing.T) {
	t.Parallel()

	var failure problem.Details

	if err := json.Unmarshal([]byte(`{
		"type":"https://example.com/problems/content-policy",
		"title":"Upload policy",
		"detail":"Use the upload endpoint.",
		"reason":"unsupported-format"
	}`), &failure); err != nil {
		t.Fatal(err)
	}

	handler := router.Handler(contenttype.Middleware(func(content *contenttype.Builder) {
		content.ErrorResponse(failure)
	})(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("rejected request reached the handler")

		return nil
	}))

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("data"))
	request.Header.Set("Content-Type", "text/plain")

	response := handler.Record(request)
	t.Cleanup(func() {
		// Recorder bodies own no external resources; close for normal client ownership.
		_ = response.Body.Close()
	})

	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the problem renderer's default 500", response.StatusCode)
	}

	var rendered problem.Details

	if err := json.UnmarshalRead(response.Body, &rendered); err != nil {
		t.Fatal(err)
	}

	if rendered.Type() != "https://example.com/problems/content-policy" || rendered.Title() != "Upload policy" {
		t.Fatalf("problem identity = %q / %q", rendered.Type(), rendered.Title())
	}

	if rendered.Detail() != "Use the upload endpoint." {
		t.Fatalf("detail = %q", rendered.Detail())
	}

	if reason, found := rendered.Extension("reason"); !found || reason != "unsupported-format" {
		t.Fatalf("reason = %v, found = %t", reason, found)
	}
}

// serve sends a request with the given method, body, and Content-Type
// through the middleware and returns the response status.
func serve(t *testing.T, method, body, contentType string, opts ...func(*contenttype.Builder)) int {
	t.Helper()

	handler := contenttype.Middleware(opts...)(func(writer http.ResponseWriter, _ *http.Request) error {
		writer.WriteHeader(http.StatusNoContent)

		return nil
	})

	request := httptest.NewRequest(method, "/", strings.NewReader(body))

	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	return router.Handler(handler).Record(request).StatusCode
}

// checkStatus sends a request through the middleware and fails the test unless
// the response status is want.
func checkStatus(t *testing.T, method, body, contentType string, want int, opts ...func(*contenttype.Builder)) {
	t.Helper()

	if got := serve(t, method, body, contentType, opts...); got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

// TestMiddlewareTextPlainPostRejected verifies a text/plain POST is rejected.
func TestMiddlewareTextPlainPostRejected(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "a=b", "text/plain", http.StatusUnsupportedMediaType)
}

// TestMiddlewareFormPostRejected verifies a form POST is rejected.
func TestMiddlewareFormPostRejected(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "a=b", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType)
}

// TestMiddlewareJSONWithCharsetAccepted verifies JSON with a charset parameter passes.
func TestMiddlewareJSONWithCharsetAccepted(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "{}", "application/json; charset=utf-8", http.StatusNoContent)
}

// TestMiddlewareJSONCaseInsensitive verifies the media type match ignores case.
func TestMiddlewareJSONCaseInsensitive(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "{}", "Application/JSON", http.StatusNoContent)
}

// TestMiddlewareProblemJSONAccepted verifies problem+json passes.
func TestMiddlewareProblemJSONAccepted(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "{}", "application/problem+json", http.StatusNoContent)
}

// TestMiddlewareMissingTypeWithBodyRejected verifies a body without a type is rejected.
func TestMiddlewareMissingTypeWithBodyRejected(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "{}", "", http.StatusUnsupportedMediaType)
}

// TestMiddlewareMalformedTypeRejected verifies a malformed type is rejected.
func TestMiddlewareMalformedTypeRejected(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPut, "{}", "application/", http.StatusUnsupportedMediaType)
}

// TestMiddlewareSuffixLookalikeRejected verifies a suffix lookalike is rejected.
func TestMiddlewareSuffixLookalikeRejected(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "{}", "application/jsonx", http.StatusUnsupportedMediaType)
}

// TestMiddlewareGetWithoutBodyPasses verifies a GET without a body passes.
func TestMiddlewareGetWithoutBodyPasses(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodGet, "", "", http.StatusNoContent)
}

// TestMiddlewareGetWithTextPlainButNoBodyPasses verifies a bodyless GET with text/plain passes.
func TestMiddlewareGetWithTextPlainButNoBodyPasses(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodGet, "", "text/plain", http.StatusNoContent)
}

// TestMiddlewareGetWithBodyRejected verifies a GET with a body is rejected.
func TestMiddlewareGetWithBodyRejected(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodGet, "{}", "text/plain", http.StatusUnsupportedMediaType)
}

// TestMiddlewareBodylessPostWithoutTypePasses verifies a bodyless POST without a type passes.
func TestMiddlewareBodylessPostWithoutTypePasses(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "", "", http.StatusNoContent)
}

// TestMiddlewareBodylessPostWithTextPlainRejected verifies a bodyless POST with text/plain is rejected.
func TestMiddlewareBodylessPostWithTextPlainRejected(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "", "text/plain", http.StatusUnsupportedMediaType)
}

// TestMiddlewareBodylessDeleteWithJSONPasses verifies a bodyless DELETE with JSON passes.
func TestMiddlewareBodylessDeleteWithJSONPasses(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodDelete, "", "application/json", http.StatusNoContent)
}

// TestMiddlewareCustomListAcceptsForm verifies a custom list accepts forms.
func TestMiddlewareCustomListAcceptsForm(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "a=b", "application/x-www-form-urlencoded",
		http.StatusNoContent, func(content *contenttype.Builder) {
			content.Allowed("application/x-www-form-urlencoded")
		})
}

// TestMiddlewareCustomListDropsJSON verifies a custom list replaces the JSON default.
func TestMiddlewareCustomListDropsJSON(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "{}", "application/json",
		http.StatusUnsupportedMediaType, func(content *contenttype.Builder) {
			content.Allowed("application/x-www-form-urlencoded")
		})
}

// TestMiddlewareCustomListMixedCaseEntry verifies a mixed-case custom entry matches.
func TestMiddlewareCustomListMixedCaseEntry(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "x", "text/plain", http.StatusNoContent, func(content *contenttype.Builder) {
		content.Allowed("Text/Plain")
	})
}

// TestMiddlewareCustomSuffixEntry verifies a custom suffix entry matches.
func TestMiddlewareCustomSuffixEntry(t *testing.T) {
	t.Parallel()

	checkStatus(t, http.MethodPost, "<a/>", "application/atom+xml",
		http.StatusNoContent, func(content *contenttype.Builder) {
			content.Allowed("+xml")
		})
}

// TestMiddlewareRejectsQueryMethod verifies the QUERY method is treated as
// unsafe.
func TestMiddlewareRejectsQueryMethod(t *testing.T) {
	t.Parallel()

	if got := serve(t, "QUERY", "", "text/plain"); got != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", got)
	}
}
