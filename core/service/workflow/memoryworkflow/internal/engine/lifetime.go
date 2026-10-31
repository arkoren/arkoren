package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/core/service/workflow"
)

// Stop cancels every run and waits for their goroutines, or until ctx is
// done. Later calls to Start return [ErrStopped]. There is no Start method
// to call first: the driver is live after [New].
//
// A run is only as stoppable as its code: a workflow function or step that
// ignores its context keeps its goroutine after a cancel or a
// [workflow.Builder.MaxDuration] timeout, although the run itself is already
// reported as canceled or failed. Stop waits for such goroutines until they
// return or ctx ends, and in the latter case returns the error of ctx and
// leaves them running. Give Stop a context with a deadline.
// A later Stop can still join handlers that were running when an earlier
// cleanup deadline expired; no call can forcibly terminate application code.
//
//	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
//	defer cancel()
//	if err := driver.Stop(cleanup); err != nil {
//		return err // Some application handlers may still be running.
//	}
func (driver *Engine) Stop(ctx context.Context) error {
	driver.mu.Lock()

	driver.stopped = true
	driver.mu.Unlock()

	driver.cancel()

	waited := make(chan struct{})

	go func() {
		driver.wg.Wait()
		close(waited)
	}()

	select {
	case <-waited:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop workflow driver: %w", ctx.Err())
	}
}

// execute runs one run to its end, then keeps it for its retention.
func (driver *Engine) execute(r *run, handler workflow.WorkflowHandler, in []byte, delay time.Duration) {
	defer driver.wg.Done()
	defer r.cancel(nil)

	if delay > 0 {
		timer := time.NewTimer(delay)

		select {
		case <-timer.C:
		case <-r.ctx.Done():
			timer.Stop()
		}
	}

	out, err := driver.invoke(r, handler, in)
	r.complete(out, err)
	r.cancel(nil)
	driver.forgetAfter(r, handler.Options.Retention)
}

// invoke supervises one workflow handler without forcibly terminating its code.
// Cancellation and MaxDuration end the wait; the tracked handler may return
// later, and Stop still joins it. The buffered result also tolerates Goexit.
func (driver *Engine) invoke(r *run, handler workflow.WorkflowHandler, in []byte) ([]byte, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, context.Cause(r.ctx)
	}

	limit := time.NewTimer(handler.Options.MaxDuration)
	defer limit.Stop()

	// result preserves handler completion independently of cancellation of the
	// supervising wait, including the default failure when the handler calls Goexit.
	type result struct {
		// out carries the handler's encoded output to the supervising goroutine.
		out []byte

		// err carries the handler failure, including an unexpected goroutine exit.
		err error
	}

	finished := make(chan result, 1)

	driver.wg.Add(1)

	go func() {
		defer driver.wg.Done()

		res := result{err: errors.New("workflow goroutine exited")}
		defer func() {
			finished <- res
		}()
		res.out, res.err = handler.Handle(&runtime{driver: driver, run: r}, in)
	}()

	select {
	case res := <-finished:
		return res.out, res.err
	case <-limit.C:
		r.cancel(workflow.ErrTimeout)

		return nil, workflow.ErrTimeout
	case <-r.ctx.Done():
		return nil, context.Cause(r.ctx)
	}
}
