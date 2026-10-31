package response

import (
	"cmp"
	"encoding/json/v2"
	"errors"
	"iter"
	"net/http"
	"time"

	"arkoren.dev/core/http/response/internal/stream"
)

// ErrNonFlushableWriter is returned when the http.ResponseWriter does not
// implement the http.Flusher interface, which is required for streaming responses.
// This capability check runs before a stream commits or starts its producer.
// An advertised capability that later fails returns that flush error instead.
//
// Example:
//
//	err := response.StreamChan(w, req, chunks)
//	if errors.Is(err, response.ErrNonFlushableWriter) {
//		return response.String(w, http.StatusNotImplemented, "streaming unavailable")
//	}
//	return err
var ErrNonFlushableWriter = errors.New("non-flushable response writer")

// defaultStreamWriteTimeout is how long one write or flush of a stream may
// block on a client that has stopped reading.
const defaultStreamWriteTimeout = 30 * time.Second

// streamConfig holds the per-call settings of [Stream], [SSE] and their channel forms.
type streamConfig struct {
	// writeTimeout bounds each write and flush; zero or negative uses
	// [defaultStreamWriteTimeout].
	writeTimeout time.Duration
}

// StreamOption changes one setting of a [Stream] or [SSE] call.
// Obtain options through [WithWriteTimeout]; they apply to that call only and
// do not alter the producer or cancel its captured request context.
//
// Example:
//
//	options := []response.StreamOption{response.WithWriteTimeout(time.Minute)}
//	return response.SSE(w, req, events, options...)
type StreamOption func(*streamConfig)

// WithWriteTimeout sets how long each write and flush of the stream, and the
// initial header flush, may block before the connection write fails. The
// deadline is renewed before every write rather than bounding the whole stream.
// Iterator forms still wait for the producer to exit, as described by [Stream];
// a write deadline cannot interrupt producer code blocked outside yield.
// Zero or negative uses 30 seconds. It has no effect on writers without write
// deadline support, which keep the server's own WriteTimeout. Deadline-setting
// errors matching [http.ErrNotSupported] are tolerated; other deadline-setting
// errors end the stream without losing their identity. Flush errors are always
// returned, including errors matching [http.ErrNotSupported].
//
// Example:
//
//	if err := response.SSE(w, req, events, response.WithWriteTimeout(time.Minute)); err != nil {
//		return err
//	}
func WithWriteTimeout(d time.Duration) StreamOption {
	return func(config *streamConfig) {
		config.writeTimeout = d
	}
}

// Stream sends the values of a sequence to an HTTP client using streaming.
// Headers are sent immediately and each value is flushed as it is produced.
// The server write deadline is replaced by a fresh one before every write
// (see [WithWriteTimeout], 30 seconds by default), so a stream outlives the
// server WriteTimeout. The deadline bounds supported network writes, not the
// time spent waiting for the producer to exit.
//
// The sequence is drained in a goroutine so the stream can still react to the
// request context and to [server.ShuttingDown]. Each yielded slice is copied
// before yield returns; the producer may then reuse its storage. Producer
// panics are re-raised on the calling goroutine after the producer exits, so
// request recovery middleware can handle them.
//
// Stream returns nil when the sequence ends or the serving [server.Server]
// starts shutting down, the context's error when the request is canceled, the
// sequence's error when it yields one, and the error of a failed write or
// flush, including by deadline. It returns [ErrNonFlushableWriter] if the
// writer does not implement [http.Flusher]. Once started, the producer is
// always joined.
// A deadline-setting error other than [http.ErrNotSupported] is returned before
// the affected flush or write. An initial deadline error leaves the response
// uncommitted; a later error does not append a replacement error response.
//
// Flushes use [http.ResponseController.Flush], preserving errors exposed by a
// writer's FlushError method, including [http.ErrNotSupported]. Writers exposing
// only the errorless Flush method cannot report flush failures. The initial
// flush follows WriteHeader, so even an initial flush failure occurs after
// commitment and cannot be replaced by a router error response. A failed data
// flush does not undo bytes already accepted by Write.
//
// On early termination, yield returns false and Stream joins the producer; it
// cannot interrupt code blocked outside yield. Such producers must watch both
// request.Context().Done() and server.ShuttingDown(request.Context()), or have
// another bounded way to exit. Graceful shutdown does not cancel the request
// context, and a write or flush failure does not cancel it either. A producer
// that does not finish can therefore keep Stream waiting indefinitely. Because
// headers are already sent when the sequence fails, the router cannot render
// the returned error; it can only log it.
//
// Example:
//
//	func letters(w http.ResponseWriter, req *http.Request) error {
//		chunks := func(yield func([]byte, error) bool) {
//			for _, chunk := range [][]byte{[]byte("a"), []byte("b")} {
//				if !yield(chunk, nil) {
//					return
//				}
//			}
//		}
//		return response.Stream(w, req, chunks)
//	}
func Stream(writer http.ResponseWriter, request *http.Request, values iter.Seq2[[]byte, error], opts ...StreamOption) error {
	return stream.Sequence(writer, request, values, stream.Identity, streamPolicy(opts))
}

// SSE streams the values of a sequence as server-sent events. Each value
// becomes one event: every line of the value is sent as a "data:" field.
// CRLF and bare CR are normalized to LF; all LF characters, including trailing
// ones, are preserved in the EventSource data. Empty values emit an empty event.
// Options, deadline, error and shutdown behavior are those of [Stream].
//
// Example:
//
//	if err := response.SSE(w, req, events); err != nil {
//		return err
//	}
func SSE(writer http.ResponseWriter, request *http.Request, values iter.Seq2[[]byte, error], opts ...StreamOption) error {
	writer.Header().Set("Content-Type", "text/event-stream")

	return stream.Sequence(writer, request, values, stream.Event, streamPolicy(opts))
}

// SSEJSON streams the values of a sequence as server-sent events, encoding
// each value as JSON (encoding/json/v2) into the "data:" field of one event.
// A value that cannot be encoded ends the stream with that error. Everything
// else behaves as [SSE].
//
// Example:
//
//	type tick struct{ N int }
//	ticks := func(yield func(tick, error) bool) {
//		for n := range 3 {
//			if !yield(tick{N: n}, nil) {
//				return
//			}
//		}
//	}
//	err := response.SSEJSON(w, req, ticks)
//	if err != nil {
//		return err
//	}
func SSEJSON[T any](writer http.ResponseWriter, request *http.Request, values iter.Seq2[T, error], opts ...StreamOption) error {
	encoded := func(yield func([]byte, error) bool) {
		for value, err := range values {
			if err != nil {
				yield(nil, err)

				return
			}

			data, err := json.Marshal(value)

			if !yield(data, err) || err != nil {
				return
			}
		}
	}

	return SSE(writer, request, encoded, opts...)
}

// StreamChan is [Stream] for a channel of values. It returns when the channel
// is closed, the request context is canceled, or the serving [server.Server]
// starts shutting down (a clean end with a nil error). The producer must stop
// on its own, for example on the request context, so it does not block on
// send.
//
// Example:
//
//	err := response.StreamChan(w, req, chunks)
//	if err != nil {
//		return err
//	}
func StreamChan(writer http.ResponseWriter, request *http.Request, values <-chan []byte, opts ...StreamOption) error {
	return stream.Channel(writer, request, values, nil, stream.Identity, streamPolicy(opts))
}

// SSEChan is [SSE] for a channel of values, ending as [StreamChan] does.
//
// Example:
//
//	err := response.SSEChan(w, req, events)
//	if err != nil {
//		return err
//	}
func SSEChan(writer http.ResponseWriter, request *http.Request, values <-chan []byte, opts ...StreamOption) error {
	writer.Header().Set("Content-Type", "text/event-stream")

	return stream.Channel(writer, request, values, nil, stream.Event, streamPolicy(opts))
}

// streamPolicy defers option application until the engine's original setup
// boundary, after capability checks and, for iterators, producer startup.
func streamPolicy(opts []StreamOption) stream.Policy {
	return stream.Policy{
		NonFlushable: ErrNonFlushableWriter,
		Timeout: func() time.Duration {
			var config streamConfig

			for _, opt := range opts {
				opt(&config)
			}

			return cmp.Or(max(config.writeTimeout, 0), defaultStreamWriteTimeout)
		},
	}
}
