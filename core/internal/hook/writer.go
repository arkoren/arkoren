package hook

import (
	"bufio"
	"net"
	"net/http"
	"sync/atomic"
)

// ResponseWriter dispatches lifecycle callbacks around writes to an underlying
// HTTP response. Before-header hooks run for the final status only, including
// 101; other informational statuses do not commit the final response. Successful
// hijacking also suppresses later HTTP fallback writes. Unwrap lets response
// controllers reach native capabilities without hiding their errors.
// Do not copy a ResponseWriter after first use.
//
// Example:
//
//	registry := hook.New()
//	registry.BeforeWriteHeader(func(writer http.ResponseWriter, status int) {
//		writer.Header().Set("X-Service", "example")
//	})
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), registry)
//	writer.WriteHeader(http.StatusNoContent)
type ResponseWriter struct {
	// ResponseWriter is the underlying writer that receives delegated writes.
	// Accessing it directly bypasses this wrapper's callbacks and commitment
	// tracking; use Unwrap-based controllers only for native operations.
	//
	// Example:
	//
	//	native := httptest.NewRecorder()
	//	writer := hook.NewResponseWriter(native, hook.New())
	//	fmt.Println(writer.ResponseWriter == native) // true
	http.ResponseWriter

	// registry dispatches response lifecycle callbacks.
	registry *Registry

	// writeHeaderCalled records whether a final response status was written.
	// Native middleware may inspect commitment while a timeout handler writes
	// its response on another goroutine; the underlying writer still owns the
	// synchronization required for concurrent writes.
	writeHeaderCalled atomic.Bool
}

// NewResponseWriter wraps a native writer and live registry without copying
// either. It performs no writes until called; neither the native writer nor the
// registry may be nil. The owner must serialize writes as net/http requires.
//
// Example:
//
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), hook.New())
//	writer.WriteHeader(http.StatusNoContent)
//	fmt.Println(writer.WriteHeaderCalled()) // true
func NewResponseWriter(writer http.ResponseWriter, registry *Registry) *ResponseWriter {
	return &ResponseWriter{
		ResponseWriter: writer,
		registry:       registry,
	}
}

// WriteHeaderCalled reports whether a final response has started or the
// connection was successfully hijacked. Informational statuses other than 101
// leave it false. Reading this state is safe while another goroutine writes;
// this does not make arbitrary concurrent ResponseWriter writes safe.
//
// Example:
//
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), hook.New())
//	writer.WriteHeader(http.StatusNoContent)
//	fmt.Println(writer.WriteHeaderCalled()) // true
func (writer *ResponseWriter) WriteHeaderCalled() bool {
	return writer.writeHeaderCalled.Load()
}

// Lookup returns the hook writer in writer's chain, looking through wrappers
// that expose Unwrap. It returns the nearest hook writer, or nil when none is
// reachable. Opaque wrappers stop the search; Unwrap chains must be acyclic.
//
// Example:
//
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), hook.New())
//	fmt.Println(hook.Lookup(writer) == writer) // true
func Lookup(writer http.ResponseWriter) *ResponseWriter {
	for writer != nil {
		if hooked, ok := writer.(*ResponseWriter); ok {
			return hooked
		}

		unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })

		if !ok {
			return nil
		}

		writer = unwrapper.Unwrap()
	}

	return nil
}

// WriteHeader sends status. Before-header callbacks run once, for the final
// status only; informational 1xx statuses pass straight through.
// Status 101 is final, matching net/http's protocol-switch behavior. As with
// net/http, callers must serialize writes and supply a valid status code.
//
// Example:
//
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), hook.New())
//	writer.WriteHeader(http.StatusNoContent)
func (writer *ResponseWriter) WriteHeader(status int) {
	if writer.writeHeaderCalled.Load() {
		return
	}

	if !responseStarted(status) {
		writer.ResponseWriter.WriteHeader(status)

		return
	}

	writer.writeHeaderCalled.Store(true)
	writer.registry.RunBeforeWriteHeader(writer.ResponseWriter, status)
	writer.ResponseWriter.WriteHeader(status)
}

// responseStarted reports whether status starts the final response.
func responseStarted(status int) bool {
	return status < http.StatusContinue || status >= http.StatusOK || status == http.StatusSwitchingProtocols
}

// Unwrap returns the retained native writer so http.ResponseController can
// discover its capabilities. Direct writes through the result bypass hooks and
// commitment tracking, so normal HTTP output must use this wrapper instead.
//
// Example:
//
//	native := httptest.NewRecorder()
//	writer := hook.NewResponseWriter(native, hook.New())
//	fmt.Println(writer.Unwrap() == native) // true
func (writer *ResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

// Write commits an implicit 200 when needed, invokes body callbacks, and
// delegates content to the native writer. Native byte counts and errors are
// returned unchanged. Content is borrowed for this call, not retained here.
//
// Example:
//
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), hook.New())
//	if _, err := writer.Write([]byte("hello")); err != nil {
//		return err
//	}
func (writer *ResponseWriter) Write(content []byte) (int, error) {
	if !writer.WriteHeaderCalled() {
		writer.WriteHeader(http.StatusOK)
	}

	writer.registry.RunBeforeWrite(writer.ResponseWriter, content)

	return writer.ResponseWriter.Write(content)
}

// Flush implements http.Flusher by committing an implicit 200 and flushing the
// native writer through its controller. The interface cannot return failures;
// use FlushError when the caller needs to handle unsupported or failed flushes.
//
// Example:
//
//	native := httptest.NewRecorder()
//	writer := hook.NewResponseWriter(native, hook.New())
//	writer.Flush()
//	fmt.Println(native.Flushed) // true
func (writer *ResponseWriter) Flush() {
	// http.Flusher cannot report errors; callers needing them can use FlushError.
	_ = writer.FlushError()
}

// FlushError commits an implicit 200 when needed and flushes the native writer
// through http.ResponseController. It returns native errors unchanged, including
// errors matching http.ErrNotSupported; the response may already be committed.
//
// Example:
//
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), hook.New())
//	if err := writer.FlushError(); err != nil {
//		return err
//	}
func (writer *ResponseWriter) FlushError() error {
	if !writer.WriteHeaderCalled() {
		writer.WriteHeader(http.StatusOK)
	}

	return http.NewResponseController(writer.ResponseWriter).Flush()
}

// Hijack delegates connection hijacking through native Unwrap layers and marks
// the response as started only on success. Failures preserve the underlying
// error and leave an unwritten response available for ordinary error handling.
// The caller owns and must close a successfully hijacked connection.
//
// Example:
//
//	writer := hook.NewResponseWriter(native, hook.New()) // native is the server writer
//	connection, _, err := writer.Hijack()
//	if err != nil {
//		return err
//	}
//	defer connection.Close()
func (writer *ResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, readWriter, err := http.NewResponseController(writer.ResponseWriter).Hijack()

	if err == nil {
		writer.writeHeaderCalled.Store(true)
	}

	return connection, readWriter, err
}
