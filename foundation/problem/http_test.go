package problem_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/foundation/problem"
)

// TestServeHTTPInvalidStatusUsesServerError verifies the transport rejects
// out-of-range statuses without changing the template or exposing its cause.
func TestServeHTTPInvalidStatusUsesServerError(t *testing.T) {
	t.Parallel()

	for _, status := range []int{-1, 99, 1000} {
		cause := errors.New("private upstream diagnostic")
		failure := problem.New(func(details *problem.Builder) {
			details.Type("urn:example:upstream").Status(status).Error(cause)
		})

		recorder := serveDetails(failure, "/orders", "application/problem+json")

		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("HTTP status = %d, want 500", recorder.Code)
		}

		if responseBody(t, recorder)["status"] != float64(500) {
			t.Fatalf("body does not contain effective status: %s", recorder.Body.String())
		}

		if failure.Status() != status || !errors.Is(failure, cause) {
			t.Fatal("rendering changed stored status or cause")
		}

		if strings.Contains(recorder.Body.String(), cause.Error()) {
			t.Fatal("rendering exposed the private cause")
		}
	}
}

// TestServeHTTPVariesByAccept prevents caches from reusing a negotiated 404
// for a later request that accepts a different response representation.
func TestServeHTTPVariesByAccept(t *testing.T) {
	t.Parallel()

	recorder := serveDetails(problem.NotFound(), "/orders", "application/json")

	if value := recorder.Header().Get("Vary"); value != "Accept" {
		t.Fatalf("Vary = %q, want Accept", value)
	}
}

// TestServeHTTPIgnoresMalformedQuality verifies an invalid quality cannot
// become maximum preference over another syntactically valid media range.
func TestServeHTTPIgnoresMalformedQuality(t *testing.T) {
	t.Parallel()

	for _, quality := range []string{"bogus", "2", "-0.1", "1e0", "0.0001", "+0.5"} {
		recorder := serveDetails(problem.BadRequest(), "/orders",
			"application/json;q="+quality+", text/plain;q=0.5")

		if actual := recorder.Header().Get("Content-Type"); actual != "text/plain; charset=utf-8" {
			t.Fatalf("quality %q selected %q, want plain text", quality, actual)
		}
	}
}

// TestServeHTTPRejectsUnsupportedCharset verifies a valid media range cannot
// select UTF-8 text when it requires a different charset.
func TestServeHTTPRejectsUnsupportedCharset(t *testing.T) {
	t.Parallel()

	recorder := serveDetails(problem.NotFound(), "/orders", "text/plain;charset=iso-8859-1")

	if recorder.Code != http.StatusNotAcceptable {
		t.Fatalf("HTTP status = %d, want 406", recorder.Code)
	}

	if recorder.Body.String() != "Not Acceptable\n" {
		t.Fatalf("406 exposed the original problem: %s", recorder.Body.String())
	}
}

// TestServeHTTPDecodedInvalidStatusKeepsIdentity verifies transport fallback
// does not normalize decoded fields, matching, or subsequent plain encoding.
func TestServeHTTPDecodedInvalidStatusKeepsIdentity(t *testing.T) {
	t.Parallel()

	failure := unmarshalDetails(t, []byte(`{"type":"urn:example:stored","status":1000,"title":"Stored title"}`))
	original := failure.Derive()

	recorder := serveDetails(failure, "/orders", "application/json")

	if recorder.Code != 500 || responseBody(t, recorder)["status"] != float64(500) {
		t.Fatalf("unexpected HTTP fallback: %d %s", recorder.Code, recorder.Body.String())
	}

	if !errors.Is(failure, original) || failure.HTTPStatus() != 1000 {
		t.Fatal("rendering changed stored problem identity")
	}

	if failure.Defaulted(nil).Status() != 1000 {
		t.Fatal("HTTP fallback leaked into ordinary defaulting")
	}

	if !strings.Contains(string(marshalDetails(t, failure)), `"status":1000`) {
		t.Fatal("rendering changed subsequent plain JSON")
	}
}

// TestServeHTTPPreservesExistingAcceptVary verifies token matching is
// case-insensitive and does not rewrite or duplicate caller-supplied values.
func TestServeHTTPPreservesExistingAcceptVary(t *testing.T) {
	t.Parallel()

	failure := problem.NotFound().Derive(func(details *problem.Builder) {
		details.Header("Vary", "Accept-Language, accept", "Origin")
	})

	recorder := serveDetails(failure, "/orders", "application/json")
	want := []string{"Accept-Language, accept", "Origin"}

	if actual := recorder.Header().Values("Vary"); !reflect.DeepEqual(actual, want) {
		t.Fatalf("Vary = %v, want %v", actual, want)
	}
}

// TestServeHTTPPreservesWildcardVary verifies a wildcard already protects
// negotiation, including the 406 path, without an additional Accept token.
func TestServeHTTPPreservesWildcardVary(t *testing.T) {
	t.Parallel()

	failure := problem.NotFound().Derive(func(details *problem.Builder) {
		details.Header("Vary", "Origin", "*")
	})

	recorder := serveDetails(failure, "/orders", "image/png")
	want := []string{"Origin", "*"}

	if actual := recorder.Header().Values("Vary"); !reflect.DeepEqual(actual, want) {
		t.Fatalf("Vary = %v, want %v", actual, want)
	}
}

// TestServeHTTPMalformedQualityOnlyUsesServerOrder preserves the documented
// default when every supplied Accept range is syntactically invalid.
func TestServeHTTPMalformedQualityOnlyUsesServerOrder(t *testing.T) {
	t.Parallel()

	recorder := serveDetails(problem.BadRequest(), "/orders", "application/json;q=bogus")

	if actual := recorder.Header().Get("Content-Type"); actual != "application/problem+json" {
		t.Fatalf("malformed header selected %q", actual)
	}
}

// TestServeHTTPAcceptsHTTPQualityGrammar covers positive endpoint and fractional
// weights, including HTTP's permitted empty fractional part.
func TestServeHTTPAcceptsHTTPQualityGrammar(t *testing.T) {
	t.Parallel()

	for _, quality := range []string{"0.001", "0.5", "1", "1.", "1.000"} {
		recorder := serveDetails(problem.BadRequest(), "/orders",
			"application/json;q="+quality+", text/plain;q=0")

		if actual := recorder.Header().Get("Content-Type"); actual != "application/json" {
			t.Fatalf("valid quality %q selected %q", quality, actual)
		}
	}
}

// TestServeHTTPCharsetExclusionOverridesPlainRange requires a parameterized
// q=0 match to override a less-specific text/plain range with higher quality.
func TestServeHTTPCharsetExclusionOverridesPlainRange(t *testing.T) {
	t.Parallel()

	recorder := serveDetails(problem.NotFound(), "/orders",
		"text/plain;charset=UTF-8;q=0, text/plain;q=1")

	if recorder.Code != http.StatusNotAcceptable {
		t.Fatalf("HTTP status = %d, want 406", recorder.Code)
	}

	if recorder.Header().Get("Vary") != "Accept" {
		t.Fatal("406 did not retain negotiation variance")
	}
}

// TestServeHTTPAcceptsUTF8Charset verifies charset names are matched without
// case sensitivity against the content type actually emitted by the renderer.
func TestServeHTTPAcceptsUTF8Charset(t *testing.T) {
	t.Parallel()

	recorder := serveDetails(problem.NotFound(), "/orders", "text/plain;charset=UTF-8")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("HTTP status = %d, want 404", recorder.Code)
	}

	if actual := recorder.Header().Get("Content-Type"); actual != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected representation %q", actual)
	}
}

// TestServeHTTPRejectsUnprovidedJSONProfile verifies unknown media parameters
// remain requirements instead of being silently discarded as extensions.
func TestServeHTTPRejectsUnprovidedJSONProfile(t *testing.T) {
	t.Parallel()

	recorder := serveDetails(problem.NotFound(), "/orders", `application/json;profile="urn:example:profile"`)

	if recorder.Code != http.StatusNotAcceptable {
		t.Fatalf("HTTP status = %d, want 406", recorder.Code)
	}
}

// TestServeHTTPEncodingFailureDoesNotUseExcludedCharset keeps encoding fallback
// pre-commit and prevents the original problem from leaking through a 406 body.
func TestServeHTTPEncodingFailureDoesNotUseExcludedCharset(t *testing.T) {
	t.Parallel()

	failure := problem.BadRequest().Derive(func(details *problem.Builder) {
		details.Detail("client-safe occurrence").Extension("unsupported", make(chan int))
	})

	recorder := serveDetails(failure, "/orders",
		"application/json, text/plain;charset=iso-8859-1")

	if recorder.Code != http.StatusNotAcceptable || recorder.Body.String() != "Not Acceptable\n" {
		t.Fatalf("unexpected fallback: %d %s", recorder.Code, recorder.Body.String())
	}
}

// failedResponseWriter exposes only HTTP commitment and failed-write counts.
// It substitutes for a disconnected client without involving a real network.
type failedResponseWriter struct {
	// ResponseRecorder retains the public HTTP headers and status.
	*httptest.ResponseRecorder

	// commits counts attempted status commitments.
	commits int

	// writes counts body writes after commitment.
	writes int
}

// WriteHeader records a single HTTP commitment before delegating to the native
// recorder; a second attempt indicates an unsafe replacement response.
func (writer *failedResponseWriter) WriteHeader(status int) {
	writer.commits++
	writer.ResponseRecorder.WriteHeader(status)
}

// Write fails like a disconnected client and records the attempt. Returning
// the cause lets ServeHTTP stop without retrying another representation.
func (writer *failedResponseWriter) Write([]byte) (int, error) {
	writer.writes++

	return 0, errors.New("client disconnected")
}

// TestServeHTTPWriteFailureDoesNotReplaceCommittedResponse verifies a failed
// body write cannot trigger a second status or a fallback body after commitment.
func TestServeHTTPWriteFailureDoesNotReplaceCommittedResponse(t *testing.T) {
	t.Parallel()

	writer := &failedResponseWriter{ResponseRecorder: httptest.NewRecorder()}

	problem.BadRequest().ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/orders", nil))

	if writer.commits != 1 || writer.writes != 1 {
		t.Fatalf("commits/writes = %d/%d, want 1/1", writer.commits, writer.writes)
	}

	if writer.Code != http.StatusBadRequest {
		t.Fatalf("committed status = %d, want 400", writer.Code)
	}
}
