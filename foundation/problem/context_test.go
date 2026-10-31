package problem_test

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"arkoren.dev/foundation/problem"
)

// TestResponseContextSetsReplacesAndDeletesProperty verifies the documented problem behavior.
func TestResponseContextSetsReplacesAndDeletesProperty(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()

	responseContext.SetProperty("request_id", "first")
	responseContext.SetProperty("request_id", "second")
	responseContext.DeleteProperty("missing")
	responseContext.DeleteProperty("request_id")

	if _, ok := responseContext.Properties()["request_id"]; ok {
		t.Fatal("expected request_id to be deleted")
	}
}

// TestResponseContextPropertiesReturnsIndependentSnapshot verifies the documented problem behavior.
func TestResponseContextPropertiesReturnsIndependentSnapshot(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetProperty("request_id", "original")

	snapshot := responseContext.Properties()

	snapshot["request_id"] = "modified"
	snapshot["other"] = "value"

	properties := responseContext.Properties()

	if properties["request_id"] != "original" {
		t.Fatalf("expected original request_id, got %v", properties["request_id"])
	}

	if _, ok := properties["other"]; ok {
		t.Fatal("snapshot mutation changed response context")
	}
}

// TestResponseContextSetHeaderReplacesValues verifies the documented problem behavior.
func TestResponseContextSetHeaderReplacesValues(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.AddHeader("Vary", "Origin")

	responseContext.SetHeader("vary", "Accept")

	if actual := responseContext.Headers().Values("Vary"); !reflect.DeepEqual(actual, []string{"Accept"}) {
		t.Fatalf("expected header values %#v, got %#v", []string{"Accept"}, actual)
	}
}

// TestResponseContextAddHeaderAppendsValuesInOrder verifies the documented problem behavior.
func TestResponseContextAddHeaderAppendsValuesInOrder(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.AddHeader("Vary", "Origin")
	responseContext.AddHeader("Vary", "Accept-Language")

	expected := []string{"Origin", "Accept-Language"}

	if actual := responseContext.Headers().Values("Vary"); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("expected header values %#v, got %#v", expected, actual)
	}
}

// TestResponseContextDeleteHeaderRemovesCanonicalizedField verifies the documented problem behavior.
func TestResponseContextDeleteHeaderRemovesCanonicalizedField(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetHeader("x-request-id", "request-123")

	responseContext.DeleteHeader("X-Request-Id")
	responseContext.DeleteHeader("missing")

	if value := responseContext.Headers().Get("X-Request-Id"); value != "" {
		t.Fatalf("expected header to be deleted, got %q", value)
	}
}

// TestResponseContextCanonicalizesHeaderNames verifies the documented problem behavior.
func TestResponseContextCanonicalizesHeaderNames(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetHeader("x-request-id", "request-123")

	if value := responseContext.Headers().Get("X-Request-Id"); value != "request-123" {
		t.Fatalf("expected canonicalized header value, got %q", value)
	}
}

// TestResponseContextHeadersReturnsDeepIndependentSnapshot verifies the documented problem behavior.
func TestResponseContextHeadersReturnsDeepIndependentSnapshot(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.AddHeader("Vary", "Origin")

	snapshot := responseContext.Headers()

	snapshot["Vary"][0] = "Modified"
	snapshot.Add("Vary", "Accept")

	if actual := responseContext.Headers().Values("Vary"); !reflect.DeepEqual(actual, []string{"Origin"}) {
		t.Fatalf("expected header values %#v, got %#v", []string{"Origin"}, actual)
	}
}

// TestResponseContextIsSafeForConcurrentUse verifies the documented problem behavior.
func TestResponseContextIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	var waitGroup sync.WaitGroup

	for index := range 100 {
		waitGroup.Go(func() {
			name := strconv.Itoa(index)
			responseContext.SetProperty("property_"+name, index)
			responseContext.SetHeader("X-Property-"+name, name)
			responseContext.AddHeader("X-Values-"+name, name)
			responseContext.DeleteProperty("missing_" + name)
			responseContext.DeleteHeader("X-Missing-" + name)
			_ = responseContext.Properties()
			_ = responseContext.Headers()
		})
	}

	waitGroup.Wait()

	if actual := len(responseContext.Properties()); actual != 100 {
		t.Fatalf("expected 100 properties, got %d", actual)
	}

	if actual := len(responseContext.Headers()); actual != 200 {
		t.Fatalf("expected 200 headers, got %d", actual)
	}
}

// TestBuilderResponseContextAttachesContainer verifies the documented problem behavior.
func TestBuilderResponseContextAttachesContainer(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	ctx := problem.NewContext(t.Context(), responseContext)

	actual, ok := problem.FromContext(ctx)

	if !ok || actual != responseContext {
		t.Fatal("expected attached response context")
	}
}

// TestResponseContextFromHandlesMissingAndNilContainer verifies the documented problem behavior.
func TestResponseContextFromHandlesMissingAndNilContainer(t *testing.T) {
	t.Parallel()

	if responseContext, ok := problem.FromContext(t.Context()); ok || responseContext != nil {
		t.Fatal("expected no response context")
	}

	original := t.Context()

	if actual := problem.NewContext(original, nil); actual != original {
		t.Fatal("expected nil response context to leave context unchanged")
	}
}

// TestServeHTTPExplicitPropertyOverridesResponseContext verifies the documented problem behavior.
func TestServeHTTPExplicitPropertyOverridesResponseContext(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetProperty("request_id", "context")

	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(
		problem.NewContext(t.Context(), responseContext),
	)
	request.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	(problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)

		details.Extension("request_id", "explicit")
	}).
		ServeHTTP(recorder, request)

	var body map[string]any

	if err := json.UnmarshalRead(recorder.Body, &body); err != nil {
		t.Fatalf("failed to decode response: %s", err)
	}

	if body["request_id"] != "explicit" {
		t.Fatalf("expected explicit request_id, got %v", body["request_id"])
	}
}

// TestServeHTTPIgnoresStandardResponseContextProperties verifies the documented problem behavior.
func TestServeHTTPIgnoresStandardResponseContextProperties(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetProperty("status", http.StatusTeapot)
	responseContext.SetProperty("title", "Context Title")
	responseContext.SetProperty("request_id", "request")

	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(
		problem.NewContext(t.Context(), responseContext),
	)
	request.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	(problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
	}).ServeHTTP(recorder, request)

	body := responseBody(t, recorder)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}

	if body["title"] != "Bad Request" {
		t.Fatalf("expected explicit title, got %v", body["title"])
	}

	if body["request_id"] != "request" {
		t.Fatalf("expected request_id, got %v", body["request_id"])
	}
}

// TestServeHTTPWritesResponseContextPropertiesAndHeaders verifies the documented problem behavior.
func TestServeHTTPWritesResponseContextPropertiesAndHeaders(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetProperty("request_id", "context")
	responseContext.SetHeader("Vary", "Origin")
	responseContext.AddHeader("Vary", "Accept-Language")

	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(
		problem.NewContext(t.Context(), responseContext),
	)
	request.Header.Set("Accept", "application/problem+json")
	recorder := httptest.NewRecorder()

	(problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
	}).ServeHTTP(recorder, request)

	var body map[string]any

	if err := json.UnmarshalRead(recorder.Body, &body); err != nil {
		t.Fatalf("failed to decode response: %s", err)
	}

	if body["request_id"] != "context" {
		t.Fatalf("expected contextual request_id, got %v", body["request_id"])
	}

	expected := []string{"Origin", "Accept-Language", "Accept"}

	if actual := recorder.Header().Values("Vary"); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("expected Vary values %#v, got %#v", expected, actual)
	}
}

// TestServeHTTPDetailsHeadersReplaceResponseContextHeaders verifies the documented problem behavior.
func TestServeHTTPDetailsHeadersReplaceResponseContextHeaders(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetHeader("Vary", "Origin")
	responseContext.AddHeader("Vary", "Accept-Language")

	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(
		problem.NewContext(t.Context(), responseContext),
	)
	request.Header.Set("Accept", "application/problem+json")
	recorder := httptest.NewRecorder()

	(problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)

		details.Header("Vary", "Accept")
	}).
		ServeHTTP(recorder, request)

	if actual := recorder.Header().Values("Vary"); !reflect.DeepEqual(actual, []string{"Accept"}) {
		t.Fatalf("expected Vary values %#v, got %#v", []string{"Accept"}, actual)
	}
}

// TestServeHTTPContextAndDetailsContentTypeDoNotOverrideProblemJSON verifies the documented problem behavior.
func TestServeHTTPContextAndDetailsContentTypeDoNotOverrideProblemJSON(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetHeader("Content-Type", "application/context")

	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(
		problem.NewContext(t.Context(), responseContext),
	)
	request.Header.Set("Accept", "application/problem+json")
	recorder := httptest.NewRecorder()

	(problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)

		details.Header("Content-Type", "application/details")
	}).
		ServeHTTP(recorder, request)

	if actual := recorder.Header().Get("Content-Type"); actual != "application/problem+json" {
		t.Fatalf("expected application/problem+json, got %q", actual)
	}
}

// TestServeHTTPContextAndDetailsContentTypeDoNotOverrideJSON verifies the documented problem behavior.
func TestServeHTTPContextAndDetailsContentTypeDoNotOverrideJSON(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetHeader("Content-Type", "application/context")

	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(
		problem.NewContext(t.Context(), responseContext),
	)
	request.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	(problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)

		details.Header("Content-Type", "application/details")
	}).
		ServeHTTP(recorder, request)

	if actual := recorder.Header().Get("Content-Type"); actual != "application/json" {
		t.Fatalf("expected application/json, got %q", actual)
	}
}

// TestServeHTTPContextAndDetailsContentTypeDoNotOverrideTextFallback verifies the documented problem behavior.
func TestServeHTTPContextAndDetailsContentTypeDoNotOverrideTextFallback(t *testing.T) {
	t.Parallel()

	responseContext := problem.NewResponseContext()
	responseContext.SetHeader("Content-Type", "application/context")

	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(
		problem.NewContext(t.Context(), responseContext),
	)
	request.Header.Set("Accept", "text/html")
	recorder := httptest.NewRecorder()

	(problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)

		details.Header("Content-Type", "application/details")
	}).
		ServeHTTP(recorder, request)

	if actual := recorder.Header().Get("Content-Type"); actual != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain; charset=utf-8, got %q", actual)
	}
}
