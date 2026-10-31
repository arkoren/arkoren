package request_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/request"
	"arkoren.dev/foundation/problem"
)

// TestXMLNilBodyReturnsInvalidXML checks the stable empty-body problem.
func TestXMLNilBodyReturnsInvalidXML(t *testing.T) {
	t.Parallel()

	r, err := http.NewRequest(http.MethodPost, "http://example.test/", nil)

	if err != nil {
		t.Fatal(err)
	}

	_, err = request.XML[struct{}](r)

	if !errors.Is(err, request.ErrInvalidXML) {
		t.Fatalf("error = %v, want invalid empty XML body", err)
	}
}

// TestXMLPreservesBodyReadFailure checks operational failure identity.
func TestXMLPreservesBodyReadFailure(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", errReader{err: io.ErrClosedPipe})

	_, err := request.XML[struct{}](r)

	if !errors.Is(err, io.ErrClosedPipe) || errors.Is(err, request.ErrInvalidXML) {
		t.Fatalf("error = %v, want retained operational failure", err)
	}
}

// TestXMLPreservesExplicitOperationalProblem keeps a preclassified failure.
func TestXMLPreservesExplicitOperationalProblem(t *testing.T) {
	t.Parallel()

	failure := problem.New(func(details *problem.Builder) {
		details.Type("tag:example.test,2026:body-read-failure")
		details.Title("Body read failed")
		details.Status(http.StatusInternalServerError)
		details.Error(io.EOF)
	})

	r := httptest.NewRequest(http.MethodPost, "/", errReader{err: failure})

	_, err := request.XML[struct{}](r)
	details := requireBodyProblemDetails(t, err)

	if details.Status() != http.StatusInternalServerError || details.Type() != failure.Type() {
		t.Fatalf("problem = %v, want the explicit operational problem", details)
	}
}

// TestXMLIgnoresSecondElement preserves first-element streaming decoding.
func TestXMLIgnoresSecondElement(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`<first></first><second></second>`))

	_, err := request.XML[struct{}](r)

	if err != nil {
		t.Fatalf("error = %v, want first-element decoding", err)
	}
}
