// Package execution owns queue attempt execution and settlement policy.
// Public job definitions, errors, deliveries, and events stay in queue.
package execution

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
)

// Handler captures the completed execution policy of one registration.
type Handler struct {
	// Attempts limits ordinary failures when the transport reports attempts.
	Attempts int
	// Backoff chooses the delay before a retry; nil delegates to the driver.
	Backoff func(int) time.Duration
	// Timeout bounds execution independently of settlement.
	Timeout time.Duration
	// Run decodes and invokes the typed application handler.
	Run func(context.Context, []byte) error
}

// Attempt releases its execution cancellation before settlement can begin.
func Attempt(ctx context.Context, handler Handler, payload []byte) (error, bool) {
	attemptContext, cancelAttempt := ctx, context.CancelFunc(func() {
	})

	if handler.Timeout > 0 {
		attemptContext, cancelAttempt = context.WithTimeout(ctx, handler.Timeout)
	}

	err := protect(func() error {
		return handler.Run(attemptContext, payload)
	})

	timedOut := err != nil && errors.Is(attemptContext.Err(), context.DeadlineExceeded)
	cancelAttempt()

	return err, timedOut
}

// protect retains the historical panic message and stack-bearing job error.
func protect(fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("job panic: %v\n%s", recovered, debug.Stack())
		}
	}()

	return fn()
}
