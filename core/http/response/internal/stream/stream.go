// Package stream owns producer joining and streaming response I/O.
package stream

import (
	"bytes"
	"context"
	"iter"
	"net/http"
	"time"

	"arkoren.dev/core/http/server"
)

// Policy supplies the outer package's error identity and per-call settings.
type Policy struct {
	// Timeout resolves options only after capability checks and producer startup.
	Timeout func() time.Duration
	// NonFlushable is the public error returned by the legacy capability gate.
	NonFlushable error
}

// Identity returns value unchanged for plain streams.
func Identity(value []byte) []byte {
	return value
}

// Sequence copies borrowed slices before yield returns. Every started producer
// is joined, including cleanup, before its panic is re-raised on the caller.
func Sequence(
	writer http.ResponseWriter,
	request *http.Request,
	values iter.Seq2[[]byte, error],
	encode func([]byte) []byte,
	policy Policy,
) error {
	if _, ok := writer.(http.Flusher); !ok {
		return policy.NonFlushable
	}

	items := make(chan []byte)
	done := make(chan error, 1)
	stop := make(chan struct{})
	exited := make(chan struct{})
	// panicValue belongs to the producer until exited closes. Joining it
	// synchronizes access even when iterator cleanup panics.
	var panicValue any

	go func() {
		defer close(exited)
		var result error
		defer func() {
			panicValue = recover()
			done <- result
		}()

		for value, err := range values {
			if err != nil {
				result = err

				return
			}

			select {
			case items <- bytes.Clone(value):
			case <-stop:
				return
			}
		}
	}()
	defer func() {
		close(stop)
		<-exited

		if panicValue != nil {
			panic(panicValue)
		}
	}()

	return Channel(writer, request, items, done, encode, policy)
}

// streamValues preserves terminal errors and stops before consuming another
// value after a write failure. Nil done and shutdown channels disable cases.
func streamValues(ctx context.Context, values <-chan []byte, done <-chan error, write func([]byte) error) error {
	shuttingDown := server.ShuttingDown(ctx)

	for {
		select {
		case <-shuttingDown:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return err
		case value, open := <-values:
			if !open {
				return nil
			}

			if err := write(value); err != nil {
				return err
			}
		}
	}
}
