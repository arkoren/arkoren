package stream_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
)

// failingDeadlineWriter scripts deadline outcomes without replacing the real
// stream loop. ResponseRecorder tracks commitment and the bytes already sent.
type failingDeadlineWriter struct {
	// ResponseRecorder records headers, flushes, and body writes.
	*httptest.ResponseRecorder
	// outcomes supplies one result per deadline refresh in call order.
	outcomes []error
}

// SetWriteDeadline consumes the next scripted result. Tests provide one result
// for the initial flush and for each value that the stream can receive.
func (writer *failingDeadlineWriter) SetWriteDeadline(_ time.Time) error {
	err := writer.outcomes[0]
	writer.outcomes = writer.outcomes[1:]

	return err
}

// TestStreamUnsupportedDeadlineContinues preserves streaming on writers that
// implement deadlines but report that this capability is unavailable.
func TestStreamUnsupportedDeadlineContinues(t *testing.T) {
	t.Parallel()

	writer := &failingDeadlineWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{http.ErrNotSupported, http.ErrNotSupported},
	}

	err := response.Stream(writer, httptest.NewRequest(http.MethodGet, "/", nil), seq(nil, "chunk"))

	if err != nil {
		t.Fatal(err)
	}

	if writer.Body.String() != "chunk" {
		t.Fatalf("body = %q", writer.Body.String())
	}
}

// TestStreamWrappedUnsupportedDeadlineContinues checks errors.Is semantics,
// rather than requiring an exact sentinel from a response-writer wrapper.
func TestStreamWrappedUnsupportedDeadlineContinues(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("deadline capability: %w", http.ErrNotSupported)
	writer := &failingDeadlineWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{err, err},
	}

	if err := response.SSE(writer, httptest.NewRequest(http.MethodGet, "/", nil), seq(nil, "chunk")); err != nil {
		t.Fatal(err)
	}

	if writer.Body.String() != "data: chunk\n\n" {
		t.Fatalf("body = %q", writer.Body.String())
	}
}

// TestStreamInitialDeadlineFailureDoesNotCommit ensures a genuine setup error
// remains available to the caller before any status, flush, or data is sent.
func TestStreamInitialDeadlineFailureDoesNotCommit(t *testing.T) {
	t.Parallel()

	cause := errors.New("deadline failed")
	writer := &failingDeadlineWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{fmt.Errorf("set deadline: %w", cause), nil},
	}
	writer.Code = 0

	err := response.Stream(writer, httptest.NewRequest(http.MethodGet, "/", nil), seq(nil, "chunk"))

	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want deadline cause", err)
	}

	if writer.Flushed || writer.Body.Len() != 0 || writer.Code != 0 {
		t.Fatalf("response committed: flushed=%t body=%q", writer.Flushed, writer.Body.String())
	}
}

// TestStreamLaterDeadlineFailureStopsBeforeNextValue checks that bytes already
// sent are retained without appending another event or a replacement problem.
func TestStreamLaterDeadlineFailureStopsBeforeNextValue(t *testing.T) {
	t.Parallel()

	cause := errors.New("deadline failed")
	writer := &failingDeadlineWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{nil, nil, fmt.Errorf("set deadline: %w", cause)},
	}

	err := response.SSE(writer, httptest.NewRequest(http.MethodGet, "/", nil), seq(nil, "first", "second"))

	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want deadline cause", err)
	}

	if !writer.Flushed || writer.Body.String() != "data: first\n\n" {
		t.Fatalf("response = %q, flushed=%t", writer.Body.String(), writer.Flushed)
	}
}

// TestRouterRendersInitialStreamDeadlineFailure checks a genuine setup failure
// follows the normal error path, before the stream commits its success status.
func TestRouterRendersInitialStreamDeadlineFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("deadline failed")
	writer := &failingDeadlineWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{cause},
	}
	handler := router.Handler(func(writer http.ResponseWriter, request *http.Request) error {
		return response.Stream(writer, request, seq(nil, "chunk"))
	})

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if writer.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", writer.Code)
	}
}

// TestRouterDoesNotAppendProblemAfterStreamDeadlineFailure protects the
// existing commitment boundary: a later error leaves only completed events.
func TestRouterDoesNotAppendProblemAfterStreamDeadlineFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("deadline failed")
	writer := &failingDeadlineWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{nil, nil, cause},
	}
	handler := router.Handler(func(writer http.ResponseWriter, request *http.Request) error {
		return response.SSE(writer, request, seq(nil, "first", "second"))
	})

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if writer.Code != http.StatusOK || writer.Body.String() != "data: first\n\n" {
		t.Fatalf("response = %d %q", writer.Code, writer.Body.String())
	}
}
