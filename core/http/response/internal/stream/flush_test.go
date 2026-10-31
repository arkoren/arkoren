package stream_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/server"
)

// seq returns chunks in order and then the supplied terminal error.
func seq(err error, chunks ...string) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		for _, chunk := range chunks {
			if !yield([]byte(chunk), nil) {
				return
			}
		}

		if err != nil {
			yield(nil, err)
		}
	}
}

// nonFlusher hides the Flush method of the wrapped recorder.
type nonFlusher struct {
	// ResponseWriter exposes only ordinary response writes.
	http.ResponseWriter
}

// failingFlushWriter exposes the standard FlushError protocol alongside the
// recorder's legacy Flush method. Scripted errors model failures after a write
// has been accepted, without depending on socket timing or private hooks.
type failingFlushWriter struct {
	// ResponseRecorder records status, body writes, and flush attempts.
	*httptest.ResponseRecorder

	// outcomes supplies results for error-reporting flushes in call order.
	// Flushes after the script is exhausted succeed, exposing continued writes.
	outcomes []error
}

// FlushError records a flush attempt and returns its next scripted result.
// A failure can occur after commitment and does not undo accepted body bytes.
//
// Example:
//
//	writer := &failingFlushWriter{
//		ResponseRecorder: httptest.NewRecorder(),
//		outcomes:         []error{io.ErrClosedPipe},
//	}
//	if err := http.NewResponseController(writer).Flush(); !errors.Is(err, io.ErrClosedPipe) {
//		t.Fatalf("flush error = %v", err)
//	}
func (writer *failingFlushWriter) FlushError() error {
	writer.Flush()

	if len(writer.outcomes) == 0 {
		return nil
	}

	err := writer.outcomes[0]
	writer.outcomes = writer.outcomes[1:]

	return err
}

// TestStreamReturnsInitialFlushFailure checks that header-flush failure stops
// data writes, preserves its cause, and joins the started iterator producer.
func TestStreamReturnsInitialFlushFailure(t *testing.T) {
	t.Parallel()

	writer := &failingFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{fmt.Errorf("flush headers: %w", io.ErrClosedPipe)},
	}
	exited := make(chan struct{})
	values := func(yield func([]byte, error) bool) {
		defer close(exited)
		yield([]byte("chunk"), nil)
	}

	err := response.Stream(writer, httptest.NewRequest(http.MethodGet, "/", nil), values)

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error = %v, want header-flush cause", err)
	}

	if writer.Code != http.StatusOK || writer.Body.Len() != 0 {
		t.Fatalf("response = %d %q, want committed status without data", writer.Code, writer.Body.String())
	}

	select {
	case <-exited:
	default:
		t.Fatal("producer still running after Stream returned")
	}
}

// TestSSEReturnsLaterFlushFailure checks that an accepted event is retained
// when its flush fails, but the next iterator value is not written.
func TestSSEReturnsLaterFlushFailure(t *testing.T) {
	t.Parallel()

	writer := &failingFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{nil, fmt.Errorf("flush event: %w", io.ErrClosedPipe)},
	}

	err := response.SSE(writer, httptest.NewRequest(http.MethodGet, "/", nil), seq(nil, "first", "second"))

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error = %v, want event-flush cause", err)
	}

	if writer.Body.String() != "data: first\n\n" {
		t.Fatalf("body = %q, want only the accepted event", writer.Body.String())
	}
}

// TestStreamChanReturnsUnsupportedInitialFlush distinguishes an unsupported
// flush from an unsupported deadline: flushing is required, not optional.
func TestStreamChanReturnsUnsupportedInitialFlush(t *testing.T) {
	t.Parallel()

	writer := &failingFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{http.ErrNotSupported},
	}
	values := make(chan []byte, 1)
	values <- []byte("chunk")
	close(values)

	err := response.StreamChan(writer, httptest.NewRequest(http.MethodGet, "/", nil), values)

	if !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("error = %v, want unsupported flush", err)
	}

	if writer.Body.Len() != 0 || len(values) != 1 {
		t.Fatalf("body = %q, unread values = %d, want no data consumed", writer.Body.String(), len(values))
	}
}

// TestSSEChanReturnsUnsupportedLaterFlush checks errors.Is semantics for a
// wrapped capability failure and stops before reading another channel value.
func TestSSEChanReturnsUnsupportedLaterFlush(t *testing.T) {
	t.Parallel()

	writer := &failingFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{nil, fmt.Errorf("flush event: %w", http.ErrNotSupported)},
	}
	values := make(chan []byte, 2)
	values <- []byte("first")
	values <- []byte("second")
	close(values)

	err := response.SSEChan(writer, httptest.NewRequest(http.MethodGet, "/", nil), values)

	if !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("error = %v, want wrapped unsupported flush", err)
	}

	if writer.Body.String() != "data: first\n\n" || len(values) != 1 {
		t.Fatalf("body = %q, unread values = %d, want one accepted event", writer.Body.String(), len(values))
	}
}

// TestSSEJSONTimestampFeedStopsOnFlushFailure compiles the guide's timestamp
// handler and exercises its cooperative producer exit after a flush failure.
// The ticker runs on synctest's clock, so the scenario has no wall-clock wait.
func TestSSEJSONTimestampFeedStopsOnFlushFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		timestamps := func(w http.ResponseWriter, r *http.Request) error {
			ticks := func(yield func(time.Time, error) bool) {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()

				for {
					var tick time.Time

					select {
					case <-r.Context().Done():
						return
					case <-server.ShuttingDown(r.Context()):
						return
					case tick = <-ticker.C:
					}

					if !yield(tick, nil) {
						return
					}
				}
			}

			return response.SSEJSON(w, r, ticks)
		}

		writer := &failingFlushWriter{
			ResponseRecorder: httptest.NewRecorder(),
			outcomes:         []error{nil, io.ErrClosedPipe},
		}
		// Three ticks bound a regressed stream that ignores the failed flush.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)

		request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)

		err := timestamps(writer, request)

		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("error = %v, want timestamp flush failure", err)
		}
	})
}

// TestStreamFlushFailureWaitsForBlockedProducer checks that flush failure does
// not abandon an iterator or cancel the request context captured by its work.
func TestStreamFlushFailureWaitsForBlockedProducer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		writer := &failingFlushWriter{
			ResponseRecorder: httptest.NewRecorder(),
			outcomes:         []error{nil, io.ErrClosedPipe},
		}
		exited := make(chan struct{})
		values := func(yield func([]byte, error) bool) {
			defer close(exited)
			yield([]byte("chunk"), nil)
			<-ctx.Done()
		}

		finished := make(chan error, 1)

		go func() {
			request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			finished <- response.Stream(writer, request, values)
		}()

		synctest.Wait()

		select {
		case err := <-finished:
			t.Fatalf("Stream abandoned its blocked producer: %v", err)
		default:
		}

		cancel()

		if err := <-finished; !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("error = %v, want original flush failure", err)
		}

		<-exited
	})
}

// TestRouterDoesNotAppendProblemAfterInitialFlushFailure exercises the router's
// error-reporting writer through its public handler seam. Even the first flush
// follows WriteHeader, so returning its failure cannot replace the response.
func TestRouterDoesNotAppendProblemAfterInitialFlushFailure(t *testing.T) {
	t.Parallel()

	writer := &failingFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{io.ErrClosedPipe},
	}
	var err error
	handler := router.Handler(func(writer http.ResponseWriter, request *http.Request) error {
		err = response.Stream(writer, request, seq(nil, "chunk"))

		return err
	})

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error = %v, want header-flush cause through router", err)
	}

	if writer.Code != http.StatusOK || writer.Body.Len() != 0 {
		t.Fatalf("response = %d %q, want committed status without a problem", writer.Code, writer.Body.String())
	}
}

// TestRouterDoesNotAppendProblemAfterLaterFlushFailure verifies that a flush
// error survives the router's wrapper without adding a problem to SSE data.
func TestRouterDoesNotAppendProblemAfterLaterFlushFailure(t *testing.T) {
	t.Parallel()

	writer := &failingFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{nil, io.ErrClosedPipe},
	}
	var err error
	handler := router.Handler(func(writer http.ResponseWriter, request *http.Request) error {
		err = response.SSE(writer, request, seq(nil, "first", "second"))

		return err
	})

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error = %v, want event-flush cause through router", err)
	}

	if writer.Code != http.StatusOK || writer.Body.String() != "data: first\n\n" {
		t.Fatalf("response = %d %q, want only the accepted event", writer.Code, writer.Body.String())
	}
}

// TestStreamReturnsRouterUnderlyingUnsupportedFlush checks that a router wrapper
// advertising Flush cannot turn a genuinely unsupported underlying flush into
// success. The public legacy capability check still passes for the wrapper.
func TestStreamReturnsRouterUnderlyingUnsupportedFlush(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()
	var err error
	handler := router.Handler(func(writer http.ResponseWriter, request *http.Request) error {
		err = response.Stream(writer, request, seq(nil, "chunk"))

		return err
	})

	handler.ServeHTTP(nonFlusher{ResponseWriter: writer}, httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("error = %v, want underlying unsupported flush", err)
	}

	if writer.Code != http.StatusOK || writer.Body.Len() != 0 {
		t.Fatalf("response = %d %q, want committed status without data", writer.Code, writer.Body.String())
	}
}

// flushErrorOnlyWriter exposes only the error-reporting flush protocol. The
// embedded interface deliberately hides the recorder's legacy Flush method.
type flushErrorOnlyWriter struct {
	// ResponseWriter delegates ordinary writes without advertising http.Flusher.
	http.ResponseWriter
}

// FlushError delegates to the native controller while intentionally leaving
// this writer outside the response package's legacy http.Flusher contract.
//
// Example:
//
//	writer := &flushErrorOnlyWriter{ResponseWriter: httptest.NewRecorder()}
//	if err := http.NewResponseController(writer).Flush(); err != nil {
//		t.Fatal(err)
//	}
func (writer *flushErrorOnlyWriter) FlushError() error {
	return http.NewResponseController(writer.ResponseWriter).Flush()
}

// TestStreamRejectsFlushErrorOnlyWriter preserves the existing public capability
// check even though ResponseController itself accepts the FlushError protocol.
func TestStreamRejectsFlushErrorOnlyWriter(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	writer := &flushErrorOnlyWriter{ResponseWriter: recorder}

	err := response.Stream(writer, httptest.NewRequest(http.MethodGet, "/", nil), seq(nil, "chunk"))

	if !errors.Is(err, response.ErrNonFlushableWriter) {
		t.Fatalf("error = %v, want existing non-flushable-writer error", err)
	}

	if recorder.Flushed || recorder.Body.Len() != 0 {
		t.Fatalf("response changed: flushed=%t body=%q", recorder.Flushed, recorder.Body.String())
	}
}
