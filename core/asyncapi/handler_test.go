package asyncapi_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/asyncapi"
)

// ExampleDocument_Handler shows the public handler can serve a HEAD response
// without opening a listener or connecting to a broker.
func ExampleDocument_Handler() {
	document := asyncapi.New("Events", "1")
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodHead, "/asyncapi.json", nil)

	if err := document.Handler()(response, request); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(response.Header().Get("Content-Type"), response.Body.Len())
	// Output: application/json 0
}

// TestHandlerServesCachedJSON checks explicit HTTP serving shares the document
// cache, media type and exact deterministic representation.
func TestHandlerServesCachedJSON(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1")

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	writer := httptest.NewRecorder()

	document.Handler().ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/asyncapi.json", nil))

	if writer.Code != http.StatusOK || writer.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected response: %d %v", writer.Code, writer.Header())
	}

	if writer.Body.String() != string(data) {
		t.Fatalf("handler differs from export: %s", writer.Body)
	}
}

// TestHandlerFailsBeforeCommit verifies direct handler invocation leaves the
// response untouched so the router can render its normal error response.
func TestHandlerFailsBeforeCommit(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()

	err := asyncapi.New("", "1").Handler()(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if err == nil {
		t.Fatal("invalid document served successfully")
	}

	if writer.Body.Len() != 0 || len(writer.Header()) != 0 {
		t.Fatalf("response committed before validation: %v %s", writer.Header(), writer.Body)
	}
}

// TestHandlerHEAD writes no body even for a direct ResponseRecorder request.
func TestHandlerHEAD(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()

	err := asyncapi.New("Events", "1").Handler()(writer, httptest.NewRequest(http.MethodHead, "/", nil))

	if err != nil {
		t.Fatal(err)
	}

	if writer.Body.Len() != 0 || writer.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected HEAD response: %v %s", writer.Header(), writer.Body)
	}
}

// failingWriter provides deterministic write failure at the HTTP boundary.
type failingWriter struct {
	// header retains handler metadata without committing to a network transport.
	header http.Header
}

// Header returns the mutable response headers used by the handler.
func (writer *failingWriter) Header() http.Header {
	return writer.header
}

// WriteHeader satisfies http.ResponseWriter; the test inspects returned errors.
func (*failingWriter) WriteHeader(int) {}

// Write simulates an interrupted response without real sockets or sleeps.
func (*failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

// TestHandlerWriteError preserves the transport write failure for the router.
func TestHandlerWriteError(t *testing.T) {
	t.Parallel()

	writer := &failingWriter{header: make(http.Header)}

	err := asyncapi.New("Events", "1").Handler()(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error lost: %v", err)
	}
}
