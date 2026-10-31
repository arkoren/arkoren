package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"arkoren.dev/core/service/workflow"
)

// Step runs the step with retries, backoff, and a timeout per attempt.
func (workflowRuntime *runtime) Step(name string, in []byte, o workflow.StepOptions) ([]byte, error) {
	handler, ok := workflowRuntime.driver.registry.Step(name)

	if !ok {
		return nil, fmt.Errorf("step %s: %w", name, workflow.ErrNotFound)
	}

	attempts := max(o.Attempts, 1)

	var failure error

	for attempt := 1; ; attempt++ {
		if err := workflowRuntime.Err(); err != nil {
			return nil, err
		}

		out, err := workflowRuntime.attempt(handler, in, o.Timeout)

		if err == nil {
			return out, nil
		}

		failure = err

		if workflowRuntime.Err() != nil {
			return nil, workflowRuntime.Err()
		}

		if attempt >= attempts || errors.Is(err, workflow.ErrPermanent) {
			break
		}

		workflowRuntime.Logger().DebugContext(workflowRuntime.run.ctx, "step failed, retrying", slog.String("step", name), slog.Int("attempt", attempt), slog.Any("error", err))

		if err := workflowRuntime.Sleep(o.Backoff(attempt)); err != nil {
			return nil, err
		}
	}

	return nil, workflow.DecodeError(workflow.EncodeError(failure))
}

// attempt supervises one registered step invocation. A zero timeout leaves the
// wait limited only by run cancellation. Timing out cancels the handler context
// but cannot terminate the handler; Engine.Stop owns and joins its goroutine.
func (workflowRuntime *runtime) attempt(handler workflow.StepHandler, in []byte, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithCancel(workflowRuntime.run.ctx)
	defer cancel()

	// result separates actual handler completion from the supervisor's timeout
	// decision. The buffered channel permits a late handler return without a reader.
	type result struct {
		// out carries the step's encoded output to its supervising goroutine.
		out []byte

		// err carries the step failure without converting its error identity.
		err error
	}

	finished := make(chan result, 1)

	workflowRuntime.driver.wg.Add(1)

	go func() {
		defer workflowRuntime.driver.wg.Done()

		attemptCtx := workflowRuntime.driver.registry.NewTraceContext(ctx, workflowRuntime.run.trace)

		out, err := handler.Handle(attemptCtx, in)
		finished <- result{
			out: out,
			err: err,
		}
	}()

	var expired <-chan time.Time

	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		expired = timer.C
	}

	select {
	case res := <-finished:
		return res.out, res.err
	case <-expired:
		return nil, fmt.Errorf("step %s timed out after %s: %w", handler.Name, timeout, workflow.ErrTimeout)
	case <-workflowRuntime.run.ctx.Done():
		return nil, workflowRuntime.Err()
	}
}
