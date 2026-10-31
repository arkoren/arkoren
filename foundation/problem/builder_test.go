package problem_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"arkoren.dev/foundation/problem"
)

// TestHeaderCopiesInputValues prevents caller slices from changing responses.
func TestHeaderCopiesInputValues(t *testing.T) {
	t.Parallel()

	values := []string{"first"}
	p := problem.New(func(details *problem.Builder) {
		details.Header("x-owned", values...)
	})

	values[0] = "changed"

	if got := serveDetails(p, "/", "").Header().Get("X-Owned"); got != "first" {
		t.Fatal(got)
	}
}

// TestHeaderWithoutValuesRemovesHeader preserves replacement/removal semantics.
func TestHeaderWithoutValuesRemovesHeader(t *testing.T) {
	t.Parallel()

	base := problem.New(func(details *problem.Builder) {
		details.Header("x-owned", "first")
	})

	p := base.Derive(func(details *problem.Builder) {
		details.Header("X-Owned")
	})

	if got := serveDetails(p, "/", "").Header().Get("X-Owned"); got != "" {
		t.Fatal(got)
	}

	if got := serveDetails(base, "/", "").Header().Get("X-Owned"); got != "first" {
		t.Fatal(got)
	}
}

// TestBuilderHeaderReplacesValuesWithoutMutatingInput verifies the documented problem behavior.
func TestBuilderHeaderReplacesValuesWithoutMutatingInput(t *testing.T) {
	t.Parallel()

	values := []string{"first", "second"}
	result := problem.BadRequest().Derive(func(details *problem.Builder) {
		details.Header("X-Trace", "old")
		details.Header("x-trace", values...)
	})

	values[0] = "modified"

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept", "application/problem+json")
	recorder := httptest.NewRecorder()

	result.ServeHTTP(recorder, request)

	expected := []string{"first", "second"}

	if actual := recorder.Header().Values("X-Trace"); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("expected header values %#v, got %#v", expected, actual)
	}
}

// TestBuilderAddedHeaderAppendsWithoutMutatingOriginal verifies the documented problem behavior.
func TestBuilderAddedHeaderAppendsWithoutMutatingOriginal(t *testing.T) {
	t.Parallel()

	original := problem.BadRequest().Derive(func(details *problem.Builder) {
		details.Header("Vary", "Accept")
	})

	result := original.Derive(func(details *problem.Builder) {
		details.AddHeader("Vary", "Accept-Language")
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept", "application/problem+json")
	originalRecorder := httptest.NewRecorder()
	resultRecorder := httptest.NewRecorder()

	original.ServeHTTP(originalRecorder, request)
	result.ServeHTTP(resultRecorder, request)

	if actual := originalRecorder.Header().Values("Vary"); !reflect.DeepEqual(actual, []string{"Accept"}) {
		t.Fatalf("expected original header values %#v, got %#v", []string{"Accept"}, actual)
	}

	expected := []string{"Accept", "Accept-Language"}

	if actual := resultRecorder.Header().Values("Vary"); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("expected header values %#v, got %#v", expected, actual)
	}
}

// TestBuilderoutHeaderRemovesStoredHeader verifies the documented problem behavior.
func TestBuilderoutHeaderRemovesStoredHeader(t *testing.T) {
	t.Parallel()

	result := problem.BadRequest().Derive(func(details *problem.Builder) {
		details.Header("Cache-Control", "no-store")
		details.RemoveHeader("cache-control")
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept", "application/problem+json")
	recorder := httptest.NewRecorder()

	result.ServeHTTP(recorder, request)

	if value := recorder.Header().Get("Cache-Control"); value != "" {
		t.Fatalf("expected Cache-Control to be removed, got %q", value)
	}
}
