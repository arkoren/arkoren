// Package httptrack implements HTTP observation policy without telemetry state.
package httptrack

import (
	"errors"
	"io"
	"sync/atomic"
)

// body reports response completion once, on exhaustion, failure, or close.
type body struct {
	// ReadCloser is the original response body.
	io.ReadCloser

	// finish receives the read error, nil for EOF or Close.
	finish func(error)

	// done guards finish independently of concurrent Read and Close calls.
	done atomic.Bool
}

// NewBody observes completion without changing read results or close errors.
// Close errors are returned to the caller, not reported as read failures.
func NewBody(original io.ReadCloser, finish func(error)) io.ReadCloser {
	return &body{ReadCloser: original, finish: finish}
}

// Read forwards the read and completes on EOF or error.
func (body *body) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)

	if errors.Is(err, io.EOF) {
		body.complete(nil)
	} else if err != nil {
		body.complete(err)
	}

	return count, err
}

// Close closes the body and completes even when the original close fails.
func (body *body) Close() error {
	err := body.ReadCloser.Close()
	body.complete(nil)

	return err
}

// complete admits exactly one completion callback.
func (body *body) complete(err error) {
	if body.done.CompareAndSwap(false, true) {
		body.finish(err)
	}
}
