package problem_test

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/foundation/problem"
)

// assertProblemDetails compares complete problem details values.
func assertProblemDetails(t *testing.T, actual, expected problem.Details) {
	t.Helper()

	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("expected details %#v, got %#v", expected, actual)
	}
}

// assertAdditional verifies a problem extension member's stored value.
func assertAdditional(t *testing.T, details problem.Details, key string, expected any) {
	t.Helper()

	actual, found := details.Extensions()[key]

	if !found {
		t.Fatalf("expected %q additional field", key)
	}

	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("expected additional field %q to be %#v, got %#v", key, expected, actual)
	}
}

// standardProblemFields holds the public RFC 9457 members of a problem.
type standardProblemFields struct {
	// Type holds the stored problem category URI. Comparisons use it
	// unchanged, including when decoding leaves the member absent.
	Type string

	// Title holds the category summary used in standard-member comparisons.
	// Keeping it separate from Detail catches accidental occurrence edits.
	Title string

	// Detail holds the occurrence explanation. Snapshot comparisons verify
	// that unrelated builder settings preserve its original value.
	Detail string

	// Status holds the advisory HTTP status without applying defaults.
	// Zero therefore remains distinguishable from a stored server error.
	Status int

	// Instance holds the occurrence URI reference. Comparisons retain an
	// empty value rather than deriving one from a request.
	Instance string
}

// standardFields returns the public RFC 9457 members of details.
func standardFields(details problem.Details) standardProblemFields {
	return standardProblemFields{
		Type:     details.Type(),
		Title:    details.Title(),
		Detail:   details.Detail(),
		Status:   details.Status(),
		Instance: details.Instance(),
	}
}

// assertStandardProblemDetails compares the public RFC 9457 members.
func assertStandardProblemDetails(t *testing.T, actual, expected problem.Details) {
	t.Helper()

	if standardFields(actual) != standardFields(expected) {
		t.Fatalf("expected standard details %#v, got %#v", standardFields(expected), standardFields(actual))
	}
}

// assertJSONFields compares selected decoded JSON object fields.
func assertJSONFields(t *testing.T, actual, expected map[string]any) {
	t.Helper()

	selected := make(map[string]any, len(expected))

	for key := range expected {
		selected[key] = actual[key]
	}

	if !reflect.DeepEqual(selected, expected) {
		t.Fatalf("expected JSON fields %#v, got %#v", expected, selected)
	}
}

// marshalDetails encodes details or fails the test.
func marshalDetails(t *testing.T, details problem.Details) []byte {
	t.Helper()

	encoded, err := json.Marshal(details)

	if err != nil {
		t.Fatalf("failed to marshal problem: %s", err)
	}

	return encoded
}

// unmarshalDetails decodes details or fails the test.
func unmarshalDetails(t *testing.T, encoded []byte) problem.Details {
	t.Helper()

	var details problem.Details

	if err := json.Unmarshal(encoded, &details); err != nil {
		t.Fatalf("failed to unmarshal problem: %s", err)
	}

	return details
}

// unmarshalObject decodes a JSON object or fails the test.
func unmarshalObject(t *testing.T, encoded []byte) map[string]any {
	t.Helper()

	var body map[string]any

	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("failed to decode JSON object: %s", err)
	}

	return body
}

// responseBody decodes a recorded JSON response or fails the test.
func responseBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var body map[string]any

	if err := json.UnmarshalRead(recorder.Body, &body); err != nil {
		t.Fatalf("failed to decode response: %s", err)
	}

	return body
}

// serveDetails records a GET response for details at path using accept.
func serveDetails(details problem.Details, path, accept string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)

	if accept != "" {
		request.Header.Set("Accept", accept)
	}

	recorder := httptest.NewRecorder()
	details.ServeHTTP(recorder, request)

	return recorder
}

// assertProblemResponse verifies the selected representation and JSON body.
func assertProblemResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	status int,
	contentType string,
	expected map[string]any,
) {
	t.Helper()

	if recorder.Code != status {
		t.Fatalf("expected status %d, got %d", status, recorder.Code)
	}

	if actual := recorder.Header().Get("Content-Type"); actual != contentType {
		t.Fatalf("expected content type %q, got %q", contentType, actual)
	}

	assertJSONFields(t, responseBody(t, recorder), expected)
}

// TestNewWithCause verifies the documented problem behavior.
func TestNewWithCause(t *testing.T) {
	t.Parallel()

	err := errors.New("something failed")
	p := problem.New(func(details *problem.Builder) {
		details.Error(err)
		details.Status(http.StatusBadRequest)
	})

	if p.Status() != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, p.Status())
	}

	if !errors.Is(p.Unwrap(), err) {
		t.Fatalf("expected error %v, got %v", err, p.Unwrap())
	}
}

// TestNewNilCause verifies the documented problem behavior.
func TestNewNilCause(t *testing.T) {
	t.Parallel()

	p := problem.New(func(details *problem.Builder) {
		details.Error(nil)
		details.Status(http.StatusNotFound)
	})

	if p.Status() != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, p.Status())
	}

	if p.Unwrap() != nil {
		t.Fatalf("expected nil error, got %v", p.Unwrap())
	}
}

// TestBuilderType verifies the documented problem behavior.
func TestBuilderType(t *testing.T) {
	t.Parallel()

	original := problem.Details{
		// Start with the zero-value problem details.
	}

	modified := original.Derive(func(details *problem.Builder) {
		details.Type("https://example.com/problems/custom")
	})

	if modified.Type() != "https://example.com/problems/custom" {
		t.Fatalf("expected type to be set, got %q", modified.Type())
	}

	if original.Type() != "" {
		t.Fatalf("original was mutated: got %q", original.Type())
	}
}

// TestBuilderTitle verifies the documented problem behavior.
func TestBuilderTitle(t *testing.T) {
	t.Parallel()

	original := problem.Details{
		// Start with the zero-value problem details.
	}

	modified := original.Derive(func(details *problem.Builder) {
		details.Title("Custom Title")
	})

	if modified.Title() != "Custom Title" {
		t.Fatalf("expected title to be set, got %q", modified.Title())
	}

	if original.Title() != "" {
		t.Fatalf("original was mutated: got %q", original.Title())
	}
}

// TestBuilderDetail verifies the documented problem behavior.
func TestBuilderDetail(t *testing.T) {
	t.Parallel()

	original := problem.Details{
		// Start with the zero-value problem details.
	}

	modified := original.Derive(func(details *problem.Builder) {
		details.Detail("The request could not be processed.")
	})

	if modified.Detail() != "The request could not be processed." {
		t.Fatalf("expected detail to be set, got %q", modified.Detail())
	}

	if original.Detail() != "" {
		t.Fatalf("original was mutated: got %q", original.Detail())
	}
}

// TestBuilderStatus verifies the documented problem behavior.
func TestBuilderStatus(t *testing.T) {
	t.Parallel()

	original := problem.Details{
		// Start with the zero-value problem details.
	}

	modified := original.Derive(func(details *problem.Builder) {
		details.Status(http.StatusUnprocessableEntity)
	})

	if modified.Status() != http.StatusUnprocessableEntity {
		t.Fatalf("expected status to be set, got %d", modified.Status())
	}

	if original.Status() != 0 {
		t.Fatalf("original was mutated: got %d", original.Status())
	}
}

// TestBuilderInstance verifies the documented problem behavior.
func TestBuilderInstance(t *testing.T) {
	t.Parallel()

	original := problem.Details{
		// Start with the zero-value problem details.
	}

	modified := original.Derive(func(details *problem.Builder) {
		details.Instance("/problems/123")
	})

	if modified.Instance() != "/problems/123" {
		t.Fatalf("expected instance to be set, got %q", modified.Instance())
	}

	if original.Instance() != "" {
		t.Fatalf("original was mutated: got %q", original.Instance())
	}
}

// TestAdditionalFound verifies the documented problem behavior.
func TestAdditionalFound(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
	})

	p = p.Derive(func(details *problem.Builder) {
		details.Extension("key", "value")
	})

	val, ok := p.Extensions()["key"]

	if !ok {
		t.Fatalf("expected key to be found")
	}

	if val != "value" {
		t.Fatalf("expected value %q, got %q", "value", val)
	}
}

// TestAdditionalNotFound verifies the documented problem behavior.
func TestAdditionalNotFound(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
	})

	val, ok := p.Extensions()["missing"]

	if ok {
		t.Fatalf("expected key to not be found")
	}

	if val != nil {
		t.Fatalf("expected nil value, got %v", val)
	}
}

// TestAdditionalNotFoundNilMap verifies the documented problem behavior.
func TestAdditionalNotFoundNilMap(t *testing.T) {
	t.Parallel()

	p := problem.Details{
		// Test the zero-value details.
	}

	val, ok := p.Extensions()["anything"]

	if ok {
		t.Fatalf("expected key to not be found on nil map")
	}

	if val != nil {
		t.Fatalf("expected nil value, got %v", val)
	}
}

// TestBuilderNilMap verifies the documented problem behavior.
func TestBuilderNilMap(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
	})

	p = p.Derive(func(details *problem.Builder) {
		details.Extension("foo", "bar")
	})

	val, ok := p.Extensions()["foo"]

	if !ok {
		t.Fatalf("expected key to be found")
	}

	if val != "bar" {
		t.Fatalf("expected value %q, got %q", "bar", val)
	}
}

// TestBuilderExistingMap verifies the documented problem behavior.
func TestBuilderExistingMap(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)

		details.Extension("first", 1)
		details.Extension("second", 2)
	})

	assertAdditional(t, p, "first", 1)
	assertAdditional(t, p, "second", 2)
}

// TestBuilderDoesNotMutateOriginal verifies the documented problem behavior.
func TestBuilderDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()

	original := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
	})
	original = original.Derive(func(details *problem.Builder) {
		details.Extension("key", "original")
	})

	modified := original.Derive(func(details *problem.Builder) {
		details.Extension("key", "modified")
	})

	val, _ := original.Extensions()["key"]

	if val != "original" {
		t.Fatalf("original was mutated: expected %q, got %v", "original", val)
	}

	val2, _ := modified.Extensions()["key"]

	if val2 != "modified" {
		t.Fatalf("modified has wrong value: expected %q, got %v", "modified", val2)
	}
}

// TestBuilderError verifies the documented problem behavior.
func TestBuilderError(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusInternalServerError)
	})
	err := errors.New("database error")
	p = p.Derive(func(details *problem.Builder) {
		details.Error(err)
	})

	if !errors.Is(p.Unwrap(), err) {
		t.Fatalf("expected error %v, got %v", err, p.Unwrap())
	}
}

// TestBuilderoutError verifies the documented problem behavior.
func TestBuilderoutError(t *testing.T) {
	t.Parallel()

	err := errors.New("some error")
	p := problem.New(func(details *problem.Builder) {
		details.Error(err)
		details.Status(http.StatusInternalServerError)
	})

	p = p.Derive(func(details *problem.Builder) {
		details.Error(nil)
	})

	if p.Unwrap() != nil {
		t.Fatalf("expected nil error, got %v", p.Unwrap())
	}
}

// TestBuilderoutNilMap verifies the documented problem behavior.
func TestBuilderoutNilMap(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
	})

	p = p.Derive(func(details *problem.Builder) {
		details.RemoveExtension("nonexistent")
	})

	if p.Status() != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, p.Status())
	}
}

// TestBuilderoutExistingKey verifies the documented problem behavior.
func TestBuilderoutExistingKey(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
	})
	p = p.Derive(func(details *problem.Builder) {
		details.Extension("key", "value")
	})

	p = p.Derive(func(details *problem.Builder) {
		details.RemoveExtension("key")
	})

	_, ok := p.Extensions()["key"]

	if ok {
		t.Fatalf("expected key to be removed")
	}
}

// TestBuilderoutDoesNotMutateOriginal verifies the documented problem behavior.
func TestBuilderoutDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()

	original := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
	})
	original = original.Derive(func(details *problem.Builder) {
		details.Extension("key", "value")
	})

	_ = original.Derive(func(details *problem.Builder) {
		details.RemoveExtension("key")
	})

	val, ok := original.Extensions()["key"]

	if !ok {
		t.Fatalf("original was mutated: key was removed")
	}

	if val != "value" {
		t.Fatalf("original was mutated: expected %q, got %v", "value", val)
	}
}

// TestErrorWithErr verifies the documented problem behavior.
func TestErrorWithErr(t *testing.T) {
	t.Parallel()

	err := errors.New("something broke")
	p := problem.New(func(details *problem.Builder) {
		details.Error(err)
		details.Status(http.StatusInternalServerError)
		details.Title("Database Unavailable")
	})

	got := p.Error()
	expected := "500 Database Unavailable: something broke"

	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

// TestErrorWithoutErr verifies the documented problem behavior.
func TestErrorWithoutErr(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusNotFound)
		details.Title("Resource Not Found")
	})

	got := p.Error()
	expected := "404 Resource Not Found"

	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

// TestUnwrap verifies the documented problem behavior.
func TestUnwrap(t *testing.T) {
	t.Parallel()

	err := errors.New("test error")
	p := problem.New(func(details *problem.Builder) {
		details.Error(err)
		details.Status(http.StatusBadRequest)
	})

	if !errors.Is(p.Unwrap(), err) {
		t.Fatalf("expected %v, got %v", err, p.Unwrap())
	}
}

// TestUnwrapNil verifies the documented problem behavior.
func TestUnwrapNil(t *testing.T) {
	t.Parallel()

	p := problem.Details{
		// Test the zero-value details.
	}

	if p.Unwrap() != nil {
		t.Fatalf("expected nil, got %v", p.Unwrap())
	}
}

// TestMarshalJSON verifies the documented problem behavior.
func TestMarshalJSON(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("https://example.com/errors/test")
		details.Title("Test Error")
		details.Detail("Something went wrong")
		details.Status(http.StatusBadRequest)
		details.Instance("/test/123")
	})
	expected := map[string]any{
		"type":     "https://example.com/errors/test",
		"title":    "Test Error",
		"detail":   "Something went wrong",
		"status":   float64(400),
		"instance": "/test/123",
	}

	assertJSONFields(t, unmarshalObject(t, marshalDetails(t, p)), expected)
}

// TestMarshalJSONWithAdditional verifies the documented problem behavior.
func TestMarshalJSONWithAdditional(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("https://example.com/errors/test")
		details.Title("Test")
		details.Status(http.StatusBadRequest)
	})

	p = p.Derive(func(details *problem.Builder) {
		details.Extension("custom_field", "custom_value")
	})

	data, err := json.Marshal(p)

	if err != nil {
		t.Fatalf("failed to marshal: %s", err)
	}

	var decoded map[string]any

	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal result: %s", err)
	}

	if decoded["custom_field"] != "custom_value" {
		t.Fatalf("expected embedded additional field, got %v", decoded["custom_field"])
	}
}

// TestUnmarshalJSON verifies the documented problem behavior.
func TestUnmarshalJSON(t *testing.T) {
	t.Parallel()

	raw := `{
		"type": "https://example.com/errors/test",
		"title": "Test Error",
		"detail": "Something went wrong",
		"status": 400,
		"instance": "/test/123"
	}`
	expected := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("https://example.com/errors/test")
		details.Title("Test Error")
		details.Detail("Something went wrong")
		details.Status(http.StatusBadRequest)
		details.Instance("/test/123")
	})

	assertStandardProblemDetails(t, unmarshalDetails(t, []byte(raw)), expected)
}

// TestUnmarshalJSONWithAdditional verifies the documented problem behavior.
func TestUnmarshalJSONWithAdditional(t *testing.T) {
	t.Parallel()

	raw := `{
		"type": "https://example.com/errors/test",
		"title": "Test",
		"status": 400,
		"custom": "extra"
	}`

	var p problem.Details

	err := json.Unmarshal([]byte(raw), &p)

	if err != nil {
		t.Fatalf("failed to unmarshal: %s", err)
	}

	if p.Extensions()["custom"] != "extra" {
		t.Fatalf("expected custom additional field, got %v", p.Extensions()["custom"])
	}
}

// TestUnmarshalJSONMissingFields verifies the documented problem behavior.
func TestUnmarshalJSONMissingFields(t *testing.T) {
	t.Parallel()

	assertStandardProblemDetails(t, unmarshalDetails(t, []byte(`{}`)), problem.Details{})
}

// TestUnmarshalJSONInvalid verifies the documented problem behavior.
func TestUnmarshalJSONInvalid(t *testing.T) {
	t.Parallel()

	var p problem.Details

	err := json.Unmarshal([]byte(`not valid json`), &p)

	if err == nil {
		t.Fatalf("expected error for invalid JSON")
	}
}

// TestDefaultedAllEmpty verifies the documented problem behavior.
func TestDefaultedAllEmpty(t *testing.T) {
	t.Parallel()

	p := problem.Details{
		// Test the zero-value details.
	}

	p = p.Defaulted(httptest.NewRequest(http.MethodGet, "/test/path", nil))

	expected := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("about:blank")
		details.Title("Internal Server Error")
		details.Status(http.StatusInternalServerError)
		details.Instance("/test/path")
	})

	assertStandardProblemDetails(t, p, expected)
}

// TestDefaultedAllFilled verifies the documented problem behavior.
func TestDefaultedAllFilled(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("https://example.com/errors/custom")
		details.Title("Custom Title")
		details.Status(http.StatusBadRequest)
		details.Instance("/custom/instance")
	})
	expected := p

	p = p.Defaulted(httptest.NewRequest(http.MethodGet, "/test/path", nil))

	assertStandardProblemDetails(t, p, expected)
}

// TestDefaultedPartialFilled verifies the documented problem behavior.
func TestDefaultedPartialFilled(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusNotFound)
	})
	p = p.Defaulted(httptest.NewRequest(http.MethodGet, "/items/42", nil))

	expected := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("about:blank")
		details.Title("Not Found")
		details.Status(http.StatusNotFound)
		details.Instance("/items/42")
	})

	assertStandardProblemDetails(t, p, expected)
}

// TestServeHTTPProblemJSON verifies the documented problem behavior.
func TestServeHTTPProblemJSON(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("https://example.com/errors/test")
		details.Title("Test Error")
		details.Detail("Details here")
		details.Status(http.StatusBadRequest)
	})

	recorder := serveDetails(p, "/test", "application/problem+json")

	assertProblemResponse(t, recorder, http.StatusBadRequest, "application/problem+json", map[string]any{
		"title": "Test Error",
	})
}

// TestServeHTTPJSON verifies the documented problem behavior.
func TestServeHTTPJSON(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("https://example.com/errors/test")
		details.Title("Test Error")
		details.Detail("Details here")
		details.Status(http.StatusBadRequest)
	})

	recorder := serveDetails(p, "/test", "application/json")

	assertProblemResponse(t, recorder, http.StatusBadRequest, "application/json", map[string]any{
		"title": "Test Error",
	})
}

// TestServeHTTPWithoutAcceptUsesProblemJSON verifies the documented problem behavior.
func TestServeHTTPWithoutAcceptUsesProblemJSON(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Title("Test Error")
		details.Detail("Something went wrong")
		details.Status(http.StatusBadRequest)
	})

	recorder := serveDetails(p, "/test", "")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}

	if actual := recorder.Header().Get("Content-Type"); actual != "application/problem+json" {
		t.Fatalf("expected application/problem+json, got %q", actual)
	}
}

// TestServeHTTPUnsupportedAcceptReturnsNotAcceptable verifies the documented problem behavior.
func TestServeHTTPUnsupportedAcceptReturnsNotAcceptable(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Title("Test Error")
		details.Detail("Something went wrong")
		details.Status(http.StatusBadRequest)
	})

	recorder := serveDetails(p, "/test", "text/html")

	if recorder.Code != http.StatusNotAcceptable {
		t.Fatalf("expected status %d, got %d", http.StatusNotAcceptable, recorder.Code)
	}

	if actual := recorder.Body.String(); actual != "Not Acceptable\n" {
		t.Fatalf("expected 406 body, got %q", actual)
	}
}

// TestHTTPStatus verifies the documented problem behavior.
func TestHTTPStatus(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusTeapot)
	})

	if p.HTTPStatus() != http.StatusTeapot {
		t.Fatalf("expected %d, got %d", http.StatusTeapot, p.HTTPStatus())
	}
}

// TestHTTPStatusZero verifies the documented problem behavior.
func TestHTTPStatusZero(t *testing.T) {
	t.Parallel()

	p := problem.Details{
		// Test the zero-value details.
	}

	if p.HTTPStatus() != 0 {
		t.Fatalf("expected 0, got %d", p.HTTPStatus())
	}
}

// TestServeHTTPDefaultsApplied verifies the documented problem behavior.
func TestServeHTTPDefaultsApplied(t *testing.T) {
	t.Parallel()

	p := problem.Details{
		// Test the zero-value details.
	}

	recorder := serveDetails(p, "/my/path", "application/problem+json")

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, recorder.Code)
	}

	assertJSONFields(t, responseBody(t, recorder), map[string]any{
		"type":     "about:blank",
		"title":    "Internal Server Error",
		"instance": "/my/path",
	})
}

// TestServeHTTPProblemJSONPreferredOverJSON verifies the documented problem behavior.
func TestServeHTTPProblemJSONPreferredOverJSON(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(
		"Accept",
		"application/problem+json;q=1.0, application/json;q=0.9",
	)
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")

	if ct != "application/problem+json" {
		t.Fatalf("expected content-type %q, got %q", "application/problem+json", ct)
	}
}

// TestServeHTTPJSONPreferredOverProblemJSON verifies the documented problem behavior.
func TestServeHTTPJSONPreferredOverProblemJSON(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(
		"Accept",
		"application/json;q=1.0, application/problem+json;q=0.5",
	)
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")

	if ct != "application/json" {
		t.Fatalf("expected content-type %q, got %q", "application/json", ct)
	}
}

// TestServeHTTPAllExcludedReturnsNotAcceptable verifies the documented problem behavior.
func TestServeHTTPAllExcludedReturnsNotAcceptable(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
		details.Detail("Original problem body")
	})

	recorder := serveDetails(p, "/test", "application/problem+json;q=0, application/json;q=0, text/plain;q=0")

	if recorder.Code != http.StatusNotAcceptable {
		t.Fatalf("expected status %d, got %d", http.StatusNotAcceptable, recorder.Code)
	}

	if actual := recorder.Body.String(); actual != "Not Acceptable\n" {
		t.Fatalf("expected 406 body without problem details, got %q", actual)
	}
}

// TestServeHTTPExactExclusionOverridesFullWildcard verifies the documented problem behavior.
func TestServeHTTPExactExclusionOverridesFullWildcard(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
	})

	recorder := serveDetails(p, "/test", "application/problem+json;q=0, */*;q=1")

	if actual := recorder.Header().Get("Content-Type"); actual != "application/json" {
		t.Fatalf("expected application/json, got %q", actual)
	}
}

// TestServeHTTPSpecificRangeOverridesTypeWildcard verifies the documented problem behavior.
func TestServeHTTPSpecificRangeOverridesTypeWildcard(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
	})

	recorder := serveDetails(p, "/test", "application/*;q=1, application/problem+json;q=0.1")

	if actual := recorder.Header().Get("Content-Type"); actual != "application/json" {
		t.Fatalf("expected application/json, got %q", actual)
	}
}

// TestServeHTTPApplicationWildcardUsesServerOrder verifies the documented problem behavior.
func TestServeHTTPApplicationWildcardUsesServerOrder(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
	})

	recorder := serveDetails(p, "/test", "application/*")

	if actual := recorder.Header().Get("Content-Type"); actual != "application/problem+json" {
		t.Fatalf("expected application/problem+json, got %q", actual)
	}
}

// TestMarshalJSONEmpty verifies the documented problem behavior.
func TestMarshalJSONEmpty(t *testing.T) {
	t.Parallel()

	p := problem.Details{
		// Test the zero-value details.
	}

	assertJSONFields(t, unmarshalObject(t, marshalDetails(t, p)), map[string]any{
		"type":   "",
		"status": nil,
	})
}

// TestMarshalUnmarshalRoundTrip verifies the documented problem behavior.
func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	t.Parallel()

	original := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("https://example.com/errors/test")
		details.Title("Test Error")
		details.Detail("A detailed description")
		details.Status(http.StatusConflict)
		details.Instance("/resources/42")

		details.Extension("trace_id", "abc123")
	})

	restored := unmarshalDetails(t, marshalDetails(t, original))

	assertStandardProblemDetails(t, restored, original)

	if value, found := restored.Extensions()["trace_id"]; !found || value != "abc123" {
		t.Fatalf("expected trace_id %q, got %v", "abc123", value)
	}
}

// TestProblemImplementsErrorInterface verifies the documented problem behavior.
func TestProblemImplementsErrorInterface(t *testing.T) {
	t.Parallel()

	var err error = (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
	})

	if err.Error() == "" {
		t.Fatalf("expected non-empty error string")
	}
}

// TestUnmarshalJSONPartialFields verifies the documented problem behavior.
func TestUnmarshalJSONPartialFields(t *testing.T) {
	t.Parallel()

	p := unmarshalDetails(t, []byte(`{"status": 422, "detail": "Validation failed"}`))
	expected := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Detail("Validation failed")
		details.Status(http.StatusUnprocessableEntity)
	})

	assertStandardProblemDetails(t, p, expected)
}

// TestMarshalJSONStandardFieldsCannotBeOverwritten verifies the documented problem behavior.
func TestMarshalJSONStandardFieldsCannotBeOverwritten(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("https://example.com/errors/test")
		details.Title("Test Error")
		details.Detail("Real detail")
		details.Status(http.StatusBadRequest)
		details.Instance("/real/instance")
	})

	p = p.Derive(func(details *problem.Builder) {
		details.Extension("status", 999)
	})
	p = p.Derive(func(details *problem.Builder) {
		details.Extension("type", "https://evil.com/hijack")
	})
	p = p.Derive(func(details *problem.Builder) {
		details.Extension("title", "Hijacked Title")
	})
	p = p.Derive(func(details *problem.Builder) {
		details.Extension("detail", "Hijacked detail")
	})
	p = p.Derive(func(details *problem.Builder) {
		details.Extension("instance", "/hijacked/instance")
	})

	_, err := json.Marshal(p)

	if err == nil {
		t.Fatalf("expected error for additional fields that conflict with standard members")
	}
}

// TestServeHTTPWithAcceptWildcardReturnsJSON verifies the documented problem behavior.
func TestServeHTTPWithAcceptWildcardReturnsJSON(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Type("https://example.com/errors/test")
		details.Title("Test Error")
		details.Detail("Details here")
		details.Status(http.StatusBadRequest)
	})

	recorder := serveDetails(p, "/test", "*/*")

	assertProblemResponse(t, recorder, http.StatusBadRequest, "application/problem+json", map[string]any{
		"title": "Test Error",
	})
}

// TestServeHTTPUnserializableAdditionalUsesPlainFallback verifies the documented problem behavior.
func TestServeHTTPUnserializableAdditionalUsesPlainFallback(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
		details.Detail("JSON failed")

		details.Extension("invalid", make(chan int))
	})

	recorder := serveDetails(p, "/test", "application/problem+json, text/plain")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}

	if actual := recorder.Header().Get("Content-Type"); actual != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain fallback, got %q", actual)
	}

	if actual := recorder.Body.String(); !strings.Contains(actual, "JSON failed") {
		t.Fatalf("expected plain problem body, got %q", actual)
	}
}

// TestServeHTTPUnserializableAdditionalWithoutPlainReturnsNotAcceptable verifies the documented problem behavior.
func TestServeHTTPUnserializableAdditionalWithoutPlainReturnsNotAcceptable(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Status(http.StatusBadRequest)
		details.Title("Bad Request")
		details.Detail("JSON failed")

		details.Extension("invalid", make(chan int))
	})

	recorder := serveDetails(p, "/test", "application/problem+json")

	if recorder.Code != http.StatusNotAcceptable {
		t.Fatalf("expected status %d, got %d", http.StatusNotAcceptable, recorder.Code)
	}

	if actual := recorder.Body.String(); actual != "Not Acceptable\n" {
		t.Fatalf("expected 406 body without problem details, got %q", actual)
	}
}

// TestUnmarshalJSONWrongFieldTypes verifies the documented problem behavior.
func TestUnmarshalJSONWrongFieldTypes(t *testing.T) {
	t.Parallel()

	raw := `{
		"type": 123,
		"title": true,
		"detail": null,
		"status": "not a number",
		"instance": ["array"]
	}`

	var p problem.Details

	err := json.Unmarshal([]byte(raw), &p)

	if err == nil {
		t.Fatalf("expected error for wrong field types")
	}
}

// TestIsMatchesDerivedProblems verifies derived copies match their template.
func TestIsMatchesDerivedProblems(t *testing.T) {
	t.Parallel()

	template := problem.NotFound().Derive(func(details *problem.Builder) {
		details.Type("urn:x")
	})

	derived := template.Derive(func(details *problem.Builder) {
		details.Error(errors.New("boom"))
		details.Extension("k", "v")
		details.Header("X-A", "problem")
		details.Detail("d")
	})

	if !errors.Is(derived, template) {
		t.Fatal("derived problem should match its template")
	}

	if !errors.Is(fmt.Errorf("wrap: %w", derived), template) {
		t.Fatal("wrapped problem should match its template")
	}
}

// TestIsRejectsDifferentProblems verifies type, status, title and target kind.
func TestIsRejectsDifferentProblems(t *testing.T) {
	t.Parallel()

	template := problem.NotFound()

	for name, other := range map[string]error{
		"status": template.Derive(func(details *problem.Builder) {
			details.Status(http.StatusGone)
		}),
		"title": template.Derive(func(details *problem.Builder) {
			details.Title("Other")
		}),
		"type": template.Derive(func(details *problem.Builder) {
			details.Type("urn:x")
		}),
		"plain": errors.New("Not Found"),
	} {
		if errors.Is(template, other) {
			t.Fatalf("%s: should not match", name)
		}
	}
}

// TestStatusHelperOccurrenceMatchesNewAndCause guards canonical helper identity
// through occurrence edits and wrapping without changing tuple-based matching.
func TestStatusHelperOccurrenceMatchesNewAndCause(t *testing.T) {
	t.Parallel()

	template := problem.New(func(details *problem.Builder) {
		details.Status(http.StatusNotFound)
	})
	cause := errors.New("storage lookup failed")
	occurrence := problem.NotFound().Derive(func(details *problem.Builder) {
		details.Error(cause)
		details.Detail("Order 42 was not found.")
		details.Instance("/orders/42")
		details.Extension("order_id", 42)
		details.Header("X-Request-ID", "request-42")
	})

	if !errors.Is(occurrence, template) {
		t.Fatal("helper occurrence does not match equivalent New")
	}

	if !errors.Is(fmt.Errorf("lookup: %w", occurrence), template) {
		t.Fatal("wrapped helper occurrence does not match equivalent New")
	}

	if !errors.Is(occurrence, cause) {
		t.Fatal("helper occurrence lost its cause")
	}
}

// TestIsStillWalksCause verifies the wrapped cause remains reachable.
func TestIsStillWalksCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("boom")

	if !errors.Is(problem.Conflict().Derive(func(details *problem.Builder) {
		details.Error(cause)
	}), cause) {
		t.Fatal("cause should be reachable")
	}
}

// TestNewDefaultsFromFinalStatus prevents defaulting the title before callbacks.
func TestNewDefaultsFromFinalStatus(t *testing.T) {
	t.Parallel()

	p := problem.New(func(details *problem.Builder) {
		details.Status(http.StatusNotFound)
	})

	if p.Status() != 404 || p.Title() != "Not Found" || p.Type() != "about:blank" {
		t.Fatalf("unexpected defaults: %#v", p)
	}
}

// TestNewKeepsExplicitTitle preserves application presentation after defaulting.
func TestNewKeepsExplicitTitle(t *testing.T) {
	t.Parallel()

	p := problem.New(func(details *problem.Builder) {
		details.Title("Missing order").Status(404)
	})

	if p.Title() != "Missing order" {
		t.Fatal(p.Title())
	}
}

// TestProblemCallbacksShareOneDraft verifies ordered, direct composition.
func TestProblemCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	var first *problem.Builder
	var calls []string
	p := problem.New(func(details *problem.Builder) {
		calls = append(calls, "first")
		first = details
		details.Title("first")
	}, func(details *problem.Builder) {
		calls = append(calls, "second")

		if details != first {
			t.Fatal("callbacks received different drafts")
		}

		details.Title("second")
	})

	if p.Title() != "second" {
		t.Fatal(p.Title())
	}

	if !reflect.DeepEqual(calls, []string{"first", "second"}) {
		t.Fatal(calls)
	}
}

// TestProblemNilAndEmptyCallbackSlices verifies omission, not a nil entry.
func TestProblemNilAndEmptyCallbackSlices(t *testing.T) {
	t.Parallel()

	var callbacks []func(*problem.Builder)

	assertProblemDetails(t, problem.New(callbacks...), problem.New())
	assertProblemDetails(t, problem.New([]func(*problem.Builder){}...), problem.New())
	assertProblemDetails(t, problem.New(func(details *problem.Builder) {
	}), problem.New())

	base := problem.NotFound()

	assertProblemDetails(t, base.Derive(callbacks...), base)
	assertProblemDetails(t, base.Derive([]func(*problem.Builder){}...), base)
}

// TestProblemSuppliedNilCallbackPanics preserves the ordinary Go call contract.
func TestProblemSuppliedNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	problem.New(nil)
}

// TestProblemCallbackPanicPropagates keeps application panics visible.
func TestProblemCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "application" {
			t.Fatalf("panic = %v", got)
		}
	}()

	problem.New(func(details *problem.Builder) {
		panic("application")
	})
}

// TestProblemBuilderReturnsReceiver checks every setting's fluent contract.
func TestProblemBuilderReturnsReceiver(t *testing.T) {
	t.Parallel()

	problem.New(func(details *problem.Builder) {
		returned := []*problem.Builder{
			details.Type("urn:x"), details.Title("X"), details.Status(400), details.Detail("detail"), details.Instance("/x"),
			details.Error(nil), details.Extension("x", nil), details.RemoveExtension("x"), details.Header("X", "1"),
			details.AddHeader("X", "2"), details.RemoveHeader("X"),
		}

		for _, actual := range returned {
			if actual != details {
				t.Fatal("different receiver")
			}
		}
	})
}

// TestDeriveStatusKeepsInheritedTitle prevents implicit presentation changes.
func TestDeriveStatusKeepsInheritedTitle(t *testing.T) {
	t.Parallel()

	p := problem.New().Derive(func(details *problem.Builder) {
		details.Status(404)
	})

	if p.Title() != "Internal Server Error" || p.Status() != 404 {
		t.Fatal(p)
	}
}

// TestDeriveWithoutCallbacksPreservesDecodedAbsence keeps received wire meaning.
func TestDeriveWithoutCallbacksPreservesDecodedAbsence(t *testing.T) {
	t.Parallel()

	p := unmarshalDetails(t, []byte(`{"title":"Received"}`))

	derived := p.Derive()

	if derived.Status() != 0 || derived.Type() != "" || derived.Title() != "Received" {
		t.Fatal(derived)
	}
}

// TestPlainJSONOmitsUnspecifiedStatus distinguishes wire absence from HTTP defaults.
func TestPlainJSONOmitsUnspecifiedStatus(t *testing.T) {
	t.Parallel()

	fields := unmarshalObject(t, marshalDetails(t, problem.Details{}))

	if _, exists := fields["status"]; exists {
		t.Fatal(fields)
	}
}

// TestUnspecifiedStatusRejectsExtensionCollision preserves the old status
// collision failure even though the standard member is now omitted at zero.
func TestUnspecifiedStatusRejectsExtensionCollision(t *testing.T) {
	t.Parallel()

	p := (problem.Details{}).Derive(func(details *problem.Builder) {
		details.Extension("status", 999)
	})

	if _, err := json.Marshal(p); err == nil {
		t.Fatal("extension supplied an unspecified standard status")
	}
}

// TestHTTPResponseIncludesEffectiveStatus checks the body and transport agree.
func TestHTTPResponseIncludesEffectiveStatus(t *testing.T) {
	t.Parallel()

	recorder := serveDetails(problem.Details{}, "/missing", "application/json")

	if recorder.Code != 500 || responseBody(t, recorder)["status"] != float64(500) {
		t.Fatal(recorder)
	}
}

// TestDecodeClearsOldCauseAndHeaders prevents unrelated response state retention.
func TestDecodeClearsOldCauseAndHeaders(t *testing.T) {
	t.Parallel()

	p := problem.New(func(details *problem.Builder) {
		details.Error(errors.New("old")).Header("X-Old", "old")
	})

	if err := json.Unmarshal([]byte(`{"title":"Received"}`), &p); err != nil {
		t.Fatal(err)
	}

	if p.Unwrap() != nil {
		t.Fatal("retained cause")
	}

	if serveDetails(p, "/", "").Header().Get("X-Old") != "" {
		t.Fatal("retained header")
	}
}

// TestDecodeFailureLeavesDestinationUnchanged guards transactional decoding.
func TestDecodeFailureLeavesDestinationUnchanged(t *testing.T) {
	t.Parallel()

	p := problem.New(func(details *problem.Builder) {
		details.Extension("old", 1).Header("X-Old", "old")
	})
	before := p

	if err := json.Unmarshal([]byte(`{"new":2,"title":"changed","status":"wrong"}`), &p); err == nil {
		t.Fatal("accepted wrong type")
	}

	assertProblemDetails(t, p, before)
}

// TestDecodeDoesNotMutateExistingCopy protects maps shared by value copies.
func TestDecodeDoesNotMutateExistingCopy(t *testing.T) {
	t.Parallel()

	p := problem.New(func(details *problem.Builder) {
		details.Extension("old", 1)
	})
	copy := p

	if err := json.Unmarshal([]byte(`{"new":2}`), &p); err != nil {
		t.Fatal(err)
	}

	assertAdditional(t, copy, "old", 1)

	if _, found := copy.Extension("new"); found {
		t.Fatal("copy was mutated")
	}
}

// TestRetainedProblemBuilderCannotMutatePublishedDetails covers both publication boundaries.
func TestRetainedProblemBuilderCannotMutatePublishedDetails(t *testing.T) {
	t.Parallel()

	var retained *problem.Builder
	base := problem.New(func(details *problem.Builder) {
		retained = details
		details.Extension("x", 1).Header("X", "1")
	})

	retained.Extension("x", 2).AddHeader("X", "2").Title("changed")

	assertAdditional(t, base, "x", 1)

	if base.Title() != "Internal Server Error" {
		t.Fatal(base.Title())
	}

	derived := base.Derive(func(details *problem.Builder) {
		retained = details
		details.Extension("x", 3).AddHeader("X", "3")
	})

	retained.RemoveExtension("x").RemoveHeader("X")

	assertAdditional(t, derived, "x", 3)
	assertAdditional(t, base, "x", 1)

	if got := serveDetails(derived, "/", "").Header().Values("X"); !reflect.DeepEqual(got, []string{"1", "3"}) {
		t.Fatal(got)
	}
}

// TestExtensionsReturnsIndependentOuterMap preserves the documented shallow boundary.
func TestExtensionsReturnsIndependentOuterMap(t *testing.T) {
	t.Parallel()

	nested := map[string]int{"value": 1}
	p := problem.New(func(details *problem.Builder) {
		details.Extension("nested", nested).Extension("x", 1)
	})

	values := p.Extensions()
	values["x"] = 2
	delete(values, "nested")

	assertAdditional(t, p, "x", 1)

	nested["value"] = 3

	assertAdditional(t, p, "nested", map[string]int{"value": 3})
}

// TestExtensionDistinguishesStoredNil keeps presence separate from the value.
func TestExtensionDistinguishesStoredNil(t *testing.T) {
	t.Parallel()

	p := problem.New(func(details *problem.Builder) {
		details.Extension("nil", nil)
	})

	if value, found := p.Extension("nil"); !found || value != nil {
		t.Fatal(value, found)
	}

	if _, found := p.Extension("absent"); found {
		t.Fatal("absent found")
	}
}

// TestErrorNilClearsCause verifies explicit clearing during derivation.
func TestErrorNilClearsCause(t *testing.T) {
	t.Parallel()

	base := problem.New(func(details *problem.Builder) {
		details.Error(errors.New("cause"))
	})

	if base.Derive(func(details *problem.Builder) {
		details.Error(nil)
	}).Unwrap() != nil {
		t.Fatal("cause retained")
	}

	if base.Unwrap() == nil {
		t.Fatal("base changed")
	}
}

// TestDerivedOccurrenceMatchesSentinelAndCause preserves both error-chain roles.
func TestDerivedOccurrenceMatchesSentinelAndCause(t *testing.T) {
	t.Parallel()

	sentinel := problem.New(func(details *problem.Builder) {
		details.Type("urn:example:missing").Status(404)
	})
	cause := errors.New("storage")

	occurrence := sentinel.Derive(func(details *problem.Builder) {
		details.Error(cause).Detail("No order").Extension("id", 42)
	})

	if !errors.Is(occurrence, sentinel) || !errors.Is(occurrence, cause) {
		t.Fatal(occurrence)
	}

	var value problem.Details

	if !errors.As(occurrence, &value) {
		t.Fatal("value As failed")
	}

	var pointer *problem.Details

	if errors.As(occurrence, &pointer) {
		t.Fatal("value unexpectedly matched pointer")
	}

	if !errors.As(&occurrence, &pointer) {
		t.Fatal("pointer As failed")
	}

	if errors.Is(occurrence, &sentinel) {
		t.Fatal("value unexpectedly matched pointer target")
	}
}

// TestDerivedTypeChangesSentinelMatch keeps identity edits observable.
func TestDerivedTypeChangesSentinelMatch(t *testing.T) {
	t.Parallel()

	base := problem.New()

	if errors.Is(base.Derive(func(details *problem.Builder) {
		details.Type("urn:other")
	}), base) {
		t.Fatal("matched changed type")
	}
}

// TestDerivedStatusChangesSentinelMatch keeps identity edits observable.
func TestDerivedStatusChangesSentinelMatch(t *testing.T) {
	t.Parallel()

	base := problem.New()

	if errors.Is(base.Derive(func(details *problem.Builder) {
		details.Status(404)
	}), base) {
		t.Fatal("matched changed status")
	}
}

// TestDerivedTitleChangesSentinelMatch keeps identity edits observable.
func TestDerivedTitleChangesSentinelMatch(t *testing.T) {
	t.Parallel()

	base := problem.New()

	if errors.Is(base.Derive(func(details *problem.Builder) {
		details.Title("Other")
	}), base) {
		t.Fatal("matched changed title")
	}
}

// TestDeriveCallbacksShareOneDraft verifies ordered occurrence edits independently
// of constructor normalization and prevents callbacks from being reapplied.
func TestDeriveCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	var first *problem.Builder
	var calls []string
	base := problem.New()

	derived := base.Derive(func(details *problem.Builder) {
		first = details
		calls = append(calls, "first")
		details.Extension("x", 1)
	}, func(details *problem.Builder) {
		if details != first {
			t.Fatal("different drafts")
		}

		calls = append(calls, "second")
		details.Extension("x", 2)
	})

	assertAdditional(t, derived, "x", 2)

	if !reflect.DeepEqual(calls, []string{"first", "second"}) {
		t.Fatal(calls)
	}

	if _, found := base.Extension("x"); found {
		t.Fatal("base changed")
	}
}

// TestDeriveSuppliedNilCallbackPanics checks misuse at the occurrence boundary.
func TestDeriveSuppliedNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	problem.New().Derive(nil)
}

// TestProblemCallbackPanicStopsLaterCallbacks ensures direct callback execution
// does not recover and continue after application failure.
func TestProblemCallbackPanicStopsLaterCallbacks(t *testing.T) {
	t.Parallel()

	later := false

	defer func() {
		if got := recover(); got != "application" {
			t.Fatalf("panic = %v", got)
		}

		if later {
			t.Fatal("later callback ran")
		}
	}()

	problem.New(func(details *problem.Builder) {
		panic("application")
	}, func(details *problem.Builder) {
		later = true
	})
}

// TestProblemTypedNilCallbackPanics distinguishes nil entries from nil slices.
func TestProblemTypedNilCallbackPanics(t *testing.T) {
	t.Parallel()

	var configure func(*problem.Builder)

	defer func() {
		if recover() == nil {
			t.Fatal("typed nil callback did not panic")
		}
	}()

	problem.New(configure)
}

// TestProblemNilCallbackStopsSequence preserves earlier side effects and never
// executes a later callback after the ordinary nil-function panic.
func TestProblemNilCallbackStopsSequence(t *testing.T) {
	t.Parallel()

	var calls []string

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}

		if !reflect.DeepEqual(calls, []string{"first"}) {
			t.Fatal(calls)
		}
	}()

	problem.New(func(details *problem.Builder) {
		calls = append(calls, "first")
	}, nil, func(details *problem.Builder) {
		calls = append(calls, "later")
	})
}
