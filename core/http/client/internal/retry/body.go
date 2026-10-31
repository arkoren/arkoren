package retry

import (
	"context"
	"io"
	"sync"
)

// cancelBody keeps native reads unchanged and closes the body exactly once
// before canceling the attempt. EOF and I/O failures do not transfer ownership.
type cancelBody struct {
	// ReadCloser preserves native reads and closure.
	io.ReadCloser
	// cancel releases the attempt context after native closure.
	cancel context.CancelFunc
	// once publishes closure and its result to concurrent callers.
	once sync.Once
	// err is accessed only through Close's synchronization.
	err error
}

// writableCancelBody retains writing only when the native body supports it.
type writableCancelBody struct {
	// cancelBody owns reads, once-only closure, and attempt cancellation.
	*cancelBody
	// Writer forwards native writes without changing read or cleanup ownership.
	io.Writer
}

// Close returns the native closure result to every caller, even if it fails.
func (body *cancelBody) Close() error {
	body.once.Do(func() {
		body.err = body.ReadCloser.Close()
		body.cancel()
	})

	return body.err
}
