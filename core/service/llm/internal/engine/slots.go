package engine

import (
	"context"
	"sync"
)

// Calls owns concurrency admission and joins every launched callback.
// Starts run synchronously after admission. Execution, including capability
// rechecks and decoding, cannot precede admission. Panics are not recovered;
// consumer panics still cancel and join callbacks cooperatively.
func Calls[Call, Result any](
	ctx context.Context,
	calls []Call,
	concurrency int,
	stop error,
	start func(Call) bool,
	execute func(context.Context, Call) (Result, error),
) ([]Result, error) {
	results := make([]Result, len(calls))
	toolCtx, cancel := context.WithCancelCause(ctx)

	var group sync.WaitGroup
	defer func() {
		cancel(nil)
		group.Wait()
	}()

	limit := len(calls)

	if concurrency > 0 {
		limit = min(limit, concurrency)
	}

	slots := make(chan struct{}, limit)

	stopped := false

	for index, call := range calls {
		select {
		case slots <- struct{}{}:
		case <-toolCtx.Done():
		}

		if toolCtx.Err() != nil {
			break
		}

		if !start(call) {
			stopped = true
			cancel(stop)

			break
		}

		group.Go(func() {
			defer func() {
				<-slots
			}()
			result, err := execute(toolCtx, call)
			results[index] = result

			if err != nil {
				cancel(err)
			}
		})
	}

	group.Wait()

	if stopped {
		return nil, stop
	}

	if err := context.Cause(toolCtx); err != nil {
		return nil, err
	}

	return results, nil
}
