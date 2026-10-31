// Package response writes HTTP responses from handlers: JSON, XML, HTML, redirects, streams, and
// server-sent events.
//
// It provides focused helpers for common response types while keeping standard
// net/http response semantics.
//
// [Stream] and [SSE] copy yielded bytes; [SSEJSON] encodes each value before
// releasing the yield callback. They re-raise producer panics on the calling
// goroutine. They stop iteration on cancellation, shutdown, or write or flush
// failure, then wait for the producer to exit. Code blocked outside yield must
// arrange its own bounded exit; graceful shutdown does not cancel the request
// context. See [Stream] for cancellation and shutdown responsibilities.
// Flush errors exposed by the writer are returned after commitment; the router
// cannot append a replacement problem response to the stream.
//
// Example:
//
//	func ready(w http.ResponseWriter, req *http.Request) error {
//		chunks := func(yield func([]byte, error) bool) {
//			yield([]byte("ready"), nil)
//		}
//		return response.Stream(w, req, chunks)
//	}
//
// # Initialization behavior
//
// This package has no global setup and can be used directly from handlers.
package response
