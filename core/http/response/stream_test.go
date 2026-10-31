package response_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/server"
)

// failingDeadlineWriter scripts native deadline outcomes in producer tests.
type failingDeadlineWriter struct {
	// ResponseRecorder records response commitment and accepted bytes.
	*httptest.ResponseRecorder
	// outcomes supplies one result per deadline refresh.
	outcomes []error
}

// SetWriteDeadline returns the next scripted deadline outcome.
func (writer *failingDeadlineWriter) SetWriteDeadline(_ time.Time) error {
	err := writer.outcomes[0]
	writer.outcomes = writer.outcomes[1:]

	return err
}

// ExampleSSE preserves the final payload newline with an empty data field.
func ExampleSSE() {
	values := func(yield func([]byte, error) bool) {
		yield([]byte("first\n"), nil)
	}

	writer := httptest.NewRecorder()

	err := response.SSE(writer, httptest.NewRequest(http.MethodGet, "/", nil), values)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Printf("%q\n", writer.Body.String())
	// Output: "data: first\ndata: \n\n"
}

// TestStreamSendsDataFromChannel verifies streamed data reaches the response.
func TestStreamSendsDataFromChannel(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()
	values := make(chan []byte, 1)
	values <- []byte("chunk")
	close(values)

	if err := response.StreamChan(writer, httptest.NewRequest(http.MethodGet, "/", nil), values); err != nil {
		t.Fatal(err)
	}

	if writer.Body.String() != "chunk" {
		t.Fatalf("got %q", writer.Body.String())
	}
}

// TestStreamWorksInsideArkorenHandler checks that streaming through the router
// response wrapper preserves both the successful status and the channel
// payload.
func TestStreamWorksInsideArkorenHandler(t *testing.T) {
	t.Parallel()

	handler := router.Handler(func(writer http.ResponseWriter, request *http.Request) error {
		values := make(chan []byte, 1)
		values <- []byte("chunk")
		close(values)

		return response.StreamChan(writer, request, values)
	})

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK || recorder.Body.String() != "chunk" {
		t.Fatalf("response = %d %q, want 200 chunk", recorder.Code, recorder.Body.String())
	}
}

// TestSSEFramesEachLine checks SSE framing for multiline data and ensures the
// response does not add a hop-by-hop Connection header.
func TestSSEFramesEachLine(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()
	values := make(chan []byte, 1)
	values <- []byte("a\nb")
	close(values)

	if err := response.SSEChan(writer, httptest.NewRequest(http.MethodGet, "/", nil), values); err != nil {
		t.Fatal(err)
	}

	if got := writer.Body.String(); got != "data: a\ndata: b\n\n" {
		t.Fatalf("body = %q", got)
	}

	if got := writer.Header().Get("Connection"); got != "" {
		t.Fatalf("connection header = %q, want none", got)
	}
}

// sseBody sends value through [response.SSEChan] and returns the written stream.
func sseBody(t *testing.T, value string) string {
	t.Helper()

	writer := httptest.NewRecorder()
	values := make(chan []byte, 1)
	values <- []byte(value)
	close(values)

	if err := response.SSEChan(writer, httptest.NewRequest(http.MethodGet, "/", nil), values); err != nil {
		t.Fatal(err)
	}

	return writer.Body.String()
}

// TestSSEBareCarriageReturnCannotInjectFields verifies that "\r" splits data
// lines instead of starting a field.
func TestSSEBareCarriageReturnCannotInjectFields(t *testing.T) {
	t.Parallel()

	if got := sseBody(t, "a\rid: 1\revent: x"); got != "data: a\ndata: id: 1\ndata: event: x\n\n" {
		t.Fatalf("body = %q", got)
	}
}

// TestSSECarriageReturnLineFeedIsOneBreak verifies that "\r\n" yields one line.
func TestSSECarriageReturnLineFeedIsOneBreak(t *testing.T) {
	t.Parallel()

	if got := sseBody(t, "a\r\nb"); got != "data: a\ndata: b\n\n" {
		t.Fatalf("body = %q", got)
	}
}

// TestSSETrailingNewlinePreservesPayload checks the empty final data field
// needed for EventSource to retain a newline at the end of the event payload.
func TestSSETrailingNewlinePreservesPayload(t *testing.T) {
	t.Parallel()

	if got := sseBody(t, "first\n"); got != "data: first\ndata: \n\n" {
		t.Fatalf("body = %q, want a trailing empty data field", got)
	}
}

// TestSSEPreservesTrailingCRLFEmptyLines checks normalization without dropping
// the final empty field that preserves both trailing newlines in EventSource.
func TestSSEPreservesTrailingCRLFEmptyLines(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()
	values := seq(nil, "first\r\n\r\n")

	if err := response.SSE(writer, httptest.NewRequest(http.MethodGet, "/", nil), values); err != nil {
		t.Fatal(err)
	}

	if got := writer.Body.String(); got != "data: first\ndata: \ndata: \n\n" {
		t.Fatalf("body = %q, want both trailing line breaks", got)
	}
}

// TestSSEEmptyValueStillEmitsEvent protects the zero-length payload when line
// splitting and empty trailing fields share the same framing path.
func TestSSEEmptyValueStillEmitsEvent(t *testing.T) {
	t.Parallel()

	if got := sseBody(t, ""); got != "data: \n\n" {
		t.Fatalf("body = %q, want one empty data field", got)
	}
}

// deadlineRecorder is a response writer that records every write deadline set
// through [http.ResponseController].
type deadlineRecorder struct {
	// ResponseRecorder captures status, headers, and streamed bytes while
	// deadlines are recorded separately.
	*httptest.ResponseRecorder
	// deadlines records every applied write deadline in call order.
	deadlines []time.Time
}

// SetWriteDeadline records deadline.
func (recorder *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	recorder.deadlines = append(recorder.deadlines, deadline)

	return nil
}

// TestStreamRefreshesWriteDeadlinePerEvent checks that each streamed event
// refreshes the write deadline instead of inheriting a stale initial deadline.
func TestStreamRefreshesWriteDeadlinePerEvent(t *testing.T) {
	t.Parallel()

	writer := &deadlineRecorder{
		ResponseRecorder: httptest.NewRecorder(),
	}
	values := make(chan []byte, 2)
	values <- []byte("a")
	values <- []byte("b")
	close(values)

	before := time.Now()

	if err := response.StreamChan(writer, httptest.NewRequest(http.MethodGet, "/", nil), values); err != nil {
		t.Fatal(err)
	}

	// One deadline for the header flush plus one per event.
	if len(writer.deadlines) != 3 {
		t.Fatalf("deadlines = %d, want 3", len(writer.deadlines))
	}

	for _, deadline := range writer.deadlines {
		if deadline.IsZero() || deadline.Before(before.Add(25*time.Second)) {
			t.Fatalf("deadline = %v, want about 30s from now", deadline)
		}
	}
}

// TestSSEWriteTimeoutOption checks that a per-call stream timeout determines
// the write deadlines applied to SSE events.
func TestSSEWriteTimeoutOption(t *testing.T) {
	t.Parallel()

	writer := &deadlineRecorder{
		ResponseRecorder: httptest.NewRecorder(),
	}
	values := make(chan []byte, 1)
	values <- []byte("a")
	close(values)

	before := time.Now()

	if err := response.SSEChan(writer, httptest.NewRequest(http.MethodGet, "/", nil), values, response.WithWriteTimeout(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if len(writer.deadlines) != 2 || writer.deadlines[1].Before(before.Add(59*time.Minute)) {
		t.Fatalf("deadlines = %v, want 2 about an hour out", writer.deadlines)
	}
}

// seq returns a sequence of the given chunks that ends with err when non-nil.
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

// TestStreamSendsSequenceValues checks that iterator chunks are written in
// sequence without extra framing or separators.
func TestStreamSendsSequenceValues(t *testing.T) {
	t.Parallel()

	writer := httptest.NewRecorder()

	if err := response.Stream(writer, httptest.NewRequest(http.MethodGet, "/", nil), seq(nil, "a", "b")); err != nil {
		t.Fatal(err)
	}

	if writer.Body.String() != "ab" {
		t.Fatalf("got %q", writer.Body.String())
	}
}

// TestStreamReturnsSequenceError checks that a producer error is returned
// after previously yielded bytes have been written.
func TestStreamReturnsSequenceError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	writer := httptest.NewRecorder()

	err := response.Stream(writer, httptest.NewRequest(http.MethodGet, "/", nil), seq(boom, "a"))

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}

	if writer.Body.String() != "a" {
		t.Fatalf("got %q", writer.Body.String())
	}
}

// TestStreamPropagatesProducerPanic checks that a producer panic retains its
// identity and can be recovered by the goroutine calling Stream.
func TestStreamPropagatesProducerPanic(t *testing.T) {
	t.Parallel()

	cause := "producer panic"
	values := func(func([]byte, error) bool) {
		panic(cause)
	}

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = response.Stream(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), values)
	}()

	if recovered != cause {
		t.Fatalf("panic = %v, want original producer panic", recovered)
	}
}

// TestSSEProducerPanicReachesRouterRecovery checks that a panic after a value
// reaches request middleware without replacing the already committed event.
func TestSSEProducerPanicReachesRouterRecovery(t *testing.T) {
	t.Parallel()

	cause := errors.New("producer panic")
	exited := make(chan struct{})
	values := func(yield func([]byte, error) bool) {
		defer close(exited)

		if yield([]byte("first"), nil) {
			panic(cause)
		}
	}

	var recovered error
	handler := router.Recover(func(recovery *router.RecoverBuilder) {
		recovery.Handler(func(value any) error {
			recovered, _ = value.(error)

			return cause
		})
	})(func(writer http.ResponseWriter, request *http.Request) error {
		return response.SSE(writer, request, values)
	})

	writer := httptest.NewRecorder()

	err := handler(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(recovered, cause) || !errors.Is(err, cause) {
		t.Fatalf("panic = %v, error = %v, want original producer panic", recovered, err)
	}

	if writer.Body.String() != "data: first\n\n" {
		t.Fatalf("body = %q, want only the completed event", writer.Body.String())
	}

	select {
	case <-exited:
	default:
		t.Fatal("producer still running after recovery")
	}
}

// TestStreamPropagatesProducerCleanupPanic checks that an iterator's deferred
// panic during early termination is not lost behind a write failure.
func TestStreamPropagatesProducerCleanupPanic(t *testing.T) {
	t.Parallel()

	cause := "producer cleanup panic"
	values := func(yield func([]byte, error) bool) {
		defer func() {
			panic(cause)
		}()

		for yield([]byte("chunk"), nil) {
		}
	}

	writer := &failingDeadlineWriter{
		ResponseRecorder: httptest.NewRecorder(),
		outcomes:         []error{nil, errors.New("write deadline failed")},
	}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = response.Stream(writer, httptest.NewRequest(http.MethodGet, "/", nil), values)
	}()

	if recovered != cause {
		t.Fatalf("panic = %v, want original cleanup panic", recovered)
	}
}

// reusedBufferRecorder delays each data deadline until the producer has mutated
// its yielded storage. This orders reuse before encoding without a data race.
type reusedBufferRecorder struct {
	// ResponseRecorder captures the bytes consumed after the buffer is reused.
	*httptest.ResponseRecorder

	// reused closes after the producer has changed its yielded slice.
	reused <-chan struct{}

	// deadlines distinguishes the initial header deadline from data deadlines.
	deadlines int
}

// SetWriteDeadline allows the header flush immediately, but waits for producer
// reuse before a data write can encode or consume the yielded slice.
//
// Example:
//
//	reused := make(chan struct{})
//	close(reused)
//	writer := &reusedBufferRecorder{ResponseRecorder: httptest.NewRecorder(), reused: reused}
//	err := writer.SetWriteDeadline(time.Time{})
func (writer *reusedBufferRecorder) SetWriteDeadline(time.Time) error {
	writer.deadlines++

	if writer.deadlines > 1 {
		<-writer.reused
	}

	return nil
}

// TestStreamCopiesYieldedBuffer checks that a producer can reuse its storage
// immediately after yield returns, even when the consumer has not written it.
func TestStreamCopiesYieldedBuffer(t *testing.T) {
	t.Parallel()

	reused := make(chan struct{})
	values := func(yield func([]byte, error) bool) {
		buffer := []byte("first")
		yield(buffer, nil)
		copy(buffer, "later")
		close(reused)
	}

	writer := &reusedBufferRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		reused:           reused,
	}

	if err := response.Stream(writer, httptest.NewRequest(http.MethodGet, "/", nil), values); err != nil {
		t.Fatal(err)
	}

	if writer.Body.String() != "first" {
		t.Fatalf("body = %q, want yielded bytes", writer.Body.String())
	}
}

// TestSSECopiesYieldedBuffer checks that SSE framing also consumes an owned
// snapshot instead of a slice that the producer has already reused.
func TestSSECopiesYieldedBuffer(t *testing.T) {
	t.Parallel()

	reused := make(chan struct{})
	values := func(yield func([]byte, error) bool) {
		buffer := []byte("first")
		yield(buffer, nil)
		copy(buffer, "later")
		close(reused)
	}

	writer := &reusedBufferRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		reused:           reused,
	}

	if err := response.SSE(writer, httptest.NewRequest(http.MethodGet, "/", nil), values); err != nil {
		t.Fatal(err)
	}

	if writer.Body.String() != "data: first\n\n" {
		t.Fatalf("body = %q, want yielded event", writer.Body.String())
	}
}

// TestStreamStopsProducerOnCancel checks that request cancellation stops
// iteration and the producer has exited before Stream returns.
func TestStreamStopsProducerOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	var exited atomic.Bool
	values := func(yield func([]byte, error) bool) {
		defer exited.Store(true)

		for yield([]byte("x"), nil) {
			cancel()
		}
	}

	err := response.Stream(httptest.NewRecorder(), request, values)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if !exited.Load() {
		t.Fatal("producer still running after Stream returned")
	}
}

// TestStreamStopsProducerOnShutdown checks that server shutdown ends the
// streaming producer rather than leaving iteration running after serving
// stops.
func TestStreamStopsProducerOnShutdown(t *testing.T) {
	t.Parallel()

	var exited atomic.Bool
	values := func(yield func([]byte, error) bool) {
		defer exited.Store(true)

		for yield([]byte("x"), nil) {
		}
	}

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := response.Stream(writer, request, values); err != nil {
			t.Error(err)
		}
	})

	srv := server.New(handler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)

	go func() {
		served <- srv.Serve(ctx)
	}()

	reply, err := http.Get("http://" + srv.Addr() + "/")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = reply.Body.Close()
	})

	if _, err := reply.Body.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}

	cancel()

	if err := <-served; err != nil {
		t.Fatal(err)
	}

	if !exited.Load() {
		t.Fatal("producer still running after shutdown")
	}
}

// TestStreamJoinsProducerWaitingForShutdown checks that the documented shutdown
// signal releases an iterator blocked outside yield without canceling requests.
func TestStreamJoinsProducerWaitingForShutdown(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		listener := server.New(http.NotFoundHandler())
		ctx := listener.HTTP().BaseContext(nil)
		started := make(chan struct{})
		exited := make(chan struct{})
		values := func(func([]byte, error) bool) {
			defer close(exited)
			close(started)
			<-server.ShuttingDown(ctx)
		}

		finished := make(chan error, 1)

		go func() {
			request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			finished <- response.Stream(httptest.NewRecorder(), request, values)
		}()

		<-started

		if err := listener.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}

		if err := <-finished; err != nil {
			t.Fatal(err)
		}

		if ctx.Err() != nil {
			t.Fatalf("request context canceled by graceful shutdown: %v", ctx.Err())
		}

		select {
		case <-exited:
		default:
			t.Fatal("producer still running after Stream returned")
		}
	})
}

// TestStreamShutdownWaitsForContextOnlyProducer establishes the lifetime limit:
// shutdown alone cannot interrupt code that watches only request cancellation.
func TestStreamShutdownWaitsForContextOnlyProducer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		listener := server.New(http.NotFoundHandler())
		ctx, cancel := context.WithCancel(listener.HTTP().BaseContext(nil))
		t.Cleanup(cancel)

		started := make(chan struct{})
		exited := make(chan struct{})
		values := func(func([]byte, error) bool) {
			defer close(exited)
			close(started)
			<-ctx.Done()
		}

		finished := make(chan error, 1)

		go func() {
			request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			finished <- response.Stream(httptest.NewRecorder(), request, values)
		}()

		<-started

		if err := listener.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		select {
		case err := <-finished:
			t.Fatalf("Stream abandoned its blocked producer: %v", err)
		default:
		}

		cancel()

		if err := <-finished; err != nil {
			t.Fatalf("shutdown result = %v, want nil after producer exits", err)
		}

		<-exited
	})
}

// TestStreamWriteFailureWaitsForBlockedProducer establishes that an early I/O
// error does not abandon an iterator or cancel its captured request context.
func TestStreamWriteFailureWaitsForBlockedProducer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		cause := errors.New("write deadline failed")
		writer := &failingDeadlineWriter{
			ResponseRecorder: httptest.NewRecorder(),
			outcomes:         []error{nil, cause},
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

		if err := <-finished; !errors.Is(err, cause) {
			t.Fatalf("error = %v, want original write failure", err)
		}

		<-exited
	})
}

// TestSSEJSONWritesOneEventPerValue checks that each iterator value becomes
// one JSON SSE event with the event-stream media type.
func TestSSEJSONWritesOneEventPerValue(t *testing.T) {
	t.Parallel()

	type tick struct {
		// N supplies a distinct JSON payload for each generated event.
		N int `json:"n"`
	}
	values := func(yield func(tick, error) bool) {
		_ = yield(tick{
			N: 1,
		}, nil) && yield(tick{
			N: 2,
		}, nil)
	}

	writer := httptest.NewRecorder()

	if err := response.SSEJSON(writer, httptest.NewRequest(http.MethodGet, "/", nil), values); err != nil {
		t.Fatal(err)
	}

	if got := writer.Body.String(); got != "data: {\"n\":1}\n\ndata: {\"n\":2}\n\n" {
		t.Fatalf("body = %q", got)
	}

	if got := writer.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
}

// TestSSEJSONReturnsSequenceError checks that a JSON event producer error is
// returned rather than encoded as a successful event.
func TestSSEJSONReturnsSequenceError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	values := func(yield func(int, error) bool) {
		yield(0, boom)
	}

	err := response.SSEJSON(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), values)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

// TestSSEJSONPropagatesProducerPanic checks that the JSON adapter preserves
// producer panics rather than converting them into encoding or stream errors.
func TestSSEJSONPropagatesProducerPanic(t *testing.T) {
	t.Parallel()

	cause := "JSON producer panic"
	values := func(func(int, error) bool) {
		panic(cause)
	}

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = response.SSEJSON(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), values)
	}()

	if recovered != cause {
		t.Fatalf("panic = %v, want original producer panic", recovered)
	}
}

// TestStreamRejectsNonFlushableWriter checks that streaming reports the
// unsupported writer capability instead of silently buffering the response.
func TestStreamRejectsNonFlushableWriter(t *testing.T) {
	t.Parallel()

	err := response.Stream(nonFlusher{
		httptest.NewRecorder(),
	}, httptest.NewRequest(http.MethodGet, "/", nil), seq(nil))

	if !errors.Is(err, response.ErrNonFlushableWriter) {
		t.Fatalf("err = %v", err)
	}
}

// nonFlusher hides the Flush method of the wrapped recorder.
type nonFlusher struct {
	// ResponseWriter intentionally exposes no Flush method, exercising the stream
	// capability error.
	http.ResponseWriter
}
