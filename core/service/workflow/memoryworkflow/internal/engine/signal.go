package engine

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"arkoren.dev/core/service/workflow"
)

// maxQueuedSignals caps the buffered values per signal name and run.
const maxQueuedSignals = 1024

// Signal buffers a copy of v for the run, so a signal sent before the run
// waits is not lost and callers may reuse v after the call. It returns
// [workflow.ErrNotFound] for an unknown or finished run and [ErrQueueFull] when
// the signal's queue is full. Copying does not validate the encoded payload.
//
//	value := []byte(`{"v":true}`)
//	if err := driver.Signal(ctx, "onboard", "user-42", "approved", value); err != nil {
//		return err
//	}
//	clear(value) // The buffered signal is unchanged.
func (driver *Engine) Signal(_ context.Context, name, id, signal string, v []byte) error {
	r, err := driver.lookup(name, id)

	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status != workflow.Running {
		return fmt.Errorf("signal %s %s: run has ended: %w", name, id, workflow.ErrNotFound)
	}

	if len(r.signals[signal]) >= maxQueuedSignals {
		return fmt.Errorf("signal %s %s %s: %w", name, id, signal, ErrQueueFull)
	}

	r.signals[signal] = append(r.signals[signal], slices.Clone(v))
	close(r.changed)

	r.changed = make(chan struct{})

	return nil
}

// timeoutChannel returns the channel of a timer for d, a stop function, and
// a nil channel when d is not positive.
func (workflowRuntime *runtime) timeoutChannel(d time.Duration) (<-chan time.Time, func()) {
	if d <= 0 {
		return nil, func() {
		}
	}

	timer := time.NewTimer(d)

	return timer.C, func() {
		timer.Stop()
	}
}

// Wait blocks for the next value of signal.
func (workflowRuntime *runtime) Wait(signal string, timeout time.Duration) ([]byte, error) {
	expired, stop := workflowRuntime.timeoutChannel(timeout)
	defer stop()

	for {
		value, changed, ok := workflowRuntime.run.take(signal)

		if ok {
			return value, nil
		}

		select {
		case <-changed:
		case <-expired:
			return nil, fmt.Errorf("signal %s: %w", signal, workflow.ErrTimeout)
		case <-workflowRuntime.run.ctx.Done():
			return nil, workflowRuntime.Err()
		}
	}
}

// Select blocks until a buffered or arriving signal, or a timer, fires.
func (workflowRuntime *runtime) Select(cases []workflow.RawCase) (int, []byte, error) {
	fired, stop := workflowRuntime.selectTimers(cases)
	defer stop()

	for {
		changed := workflowRuntime.run.arrival()

		if index, value := workflowRuntime.takeCase(cases); index >= 0 {
			return index, value, nil
		}

		select {
		case <-changed:
		case index := <-fired:
			return index, nil, nil
		case <-workflowRuntime.run.ctx.Done():
			return 0, nil, workflowRuntime.Err()
		}
	}
}

// selectTimers owns the timer alternatives for one Select. Each timer sends at
// most once into a channel sized for all cases, so cleanup can stop the timers
// and join their workers without depending on a live result receiver.
func (workflowRuntime *runtime) selectTimers(cases []workflow.RawCase) (<-chan int, func()) {
	quit := make(chan struct{})
	fired := make(chan int, len(cases))
	var workers sync.WaitGroup
	var stops []func()

	for index, option := range cases {
		if option.Signal != "" {
			continue
		}

		expired, stop := workflowRuntime.timeoutChannel(max(option.After, 1))
		stops = append(stops, stop)

		workers.Go(func() {
			select {
			case <-expired:
				fired <- index
			case <-quit:
			}
		})
	}

	return fired, func() {
		close(quit)

		for _, stop := range stops {
			stop()
		}

		workers.Wait()
	}
}

// takeCase consumes the first buffered signal in declaration order, or returns
// -1 when none is ready. Select reads arrival before scanning so a concurrent
// signal cannot be lost between the scan and its subsequent wait.
func (workflowRuntime *runtime) takeCase(cases []workflow.RawCase) (int, []byte) {
	for index, option := range cases {
		if option.Signal == "" {
			continue
		}

		if value, _, ok := workflowRuntime.run.take(option.Signal); ok {
			return index, value
		}
	}

	return -1, nil
}

// arrival returns the channel that closes when the next signal arrives.
func (run *run) arrival() <-chan struct{} {
	run.mu.Lock()
	defer run.mu.Unlock()

	return run.changed
}

// take pops the oldest buffered value of signal. It also returns the channel
// that closes when any signal arrives, read in the same critical section so
// no arrival is missed.
func (run *run) take(signal string) ([]byte, <-chan struct{}, bool) {
	run.mu.Lock()
	defer run.mu.Unlock()

	queue := run.signals[signal]

	if len(queue) == 0 {
		return nil, run.changed, false
	}

	run.signals[signal] = queue[1:]

	return queue[0], run.changed, true
}
