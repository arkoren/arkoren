package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"arkoren.dev/core/service/workflow"
)

// runKey identifies a run.
type runKey struct {
	// name identifies the registered workflow definition.
	name string

	// id distinguishes runs of that workflow.
	id string
}

// run is the state of one run.
type run struct {
	// trace is the detached start carrier, forwarded without orchestration I/O.
	trace map[string]string
	// name identifies the workflow handler used by this run.
	name string

	// id is the caller-supplied identity within that workflow.
	id string

	// logger carries the workflow name and run ID on execution messages.
	logger *slog.Logger

	// ctx ends when the run is canceled or timed out; cancel ends it with a
	// cause.
	ctx context.Context

	// cancel ends ctx with the cancellation or timeout cause.
	cancel context.CancelCauseFunc

	// done is closed after status, result, and failure are final.
	done chan struct{}

	// mu guards the fields below.
	mu sync.Mutex

	// status is Running until finish records the terminal outcome.
	status workflow.Status

	// result holds the encoded output once execution succeeds.
	result []byte

	// failure holds the encoded error once execution fails or is canceled.
	failure []byte

	// signals buffers received values per signal name; changed is closed and
	// replaced whenever one arrives.
	signals map[string][][]byte

	// changed wakes signal waiters and is replaced on each arrival.
	changed chan struct{}
}

// outcome returns a detached encoded output or the decoded failure of a
// finished run. Both Result and child joins use it, so consumers cannot mutate
// the retained result through either publication path.
func (run *run) outcome() ([]byte, error) {
	run.mu.Lock()
	defer run.mu.Unlock()

	if run.failure != nil {
		return nil, workflow.DecodeError(run.failure)
	}

	return slices.Clone(run.result), nil
}

// finish records the final state and wakes everyone waiting.
func (run *run) finish(status workflow.Status, result []byte, failure error) {
	run.mu.Lock()

	run.status = status
	run.result = result
	run.failure = workflow.EncodeError(failure)
	run.signals = nil
	run.mu.Unlock()

	close(run.done)
}

// lookup returns the run called id of workflow name.
func (driver *Engine) lookup(name, id string) (*run, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	r, ok := driver.runs[runKey{
		name: name,
		id:   id,
	}]

	if !ok {
		return nil, fmt.Errorf("run %s %s: %w", name, id, workflow.ErrNotFound)
	}

	return r, nil
}

// Start begins a run in a new goroutine. It returns
// [workflow.ErrAlreadyStarted] for an existing run and
// [workflow.ErrNotFound] for an unregistered workflow. It copies in before
// returning, so callers may reuse the input buffer during a delayed start or
// execution. Copying does not validate or change the encoded payload.
//
//	input := []byte(`{"v":"welcome"}`)
//	if err := driver.Start(ctx, "onboard", "user-42", input, workflow.StartOptions{}); err != nil {
//		return err
//	}
//	clear(input) // The run keeps its own encoded input.
func (driver *Engine) Start(ctx context.Context, name, id string, in []byte, o workflow.StartOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start %s %s: %w", name, id, err)
	}

	_, err := driver.startRun(driver.root, name, id, in, o)

	return err
}

// startRun registers and launches a run whose context derives from parent. It
// snapshots in before launching execution, including child and spawned runs.
// It returns the existing run together with [workflow.ErrAlreadyStarted].
// The duplicate check and insertion share one lock: a joining Child never
// replaces the existing run's context or acquires cancellation ownership.
func (driver *Engine) startRun(parent context.Context, name, id string, in []byte, o workflow.StartOptions) (*run, error) {
	if id == "" {
		return nil, fmt.Errorf("start %s: %w", name, workflow.ErrInvalidID)
	}

	handler, ok := driver.registry.Workflow(name)

	if !ok {
		return nil, fmt.Errorf("start %s: %w", name, workflow.ErrNotFound)
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.stopped {
		return nil, fmt.Errorf("start %s %s: %w", name, id, ErrStopped)
	}

	key := runKey{
		name: name,
		id:   id,
	}

	if existing, ok := driver.runs[key]; ok {
		return existing, fmt.Errorf("start %s %s: %w", name, id, workflow.ErrAlreadyStarted)
	}

	ctx, cancel := context.WithCancelCause(parent)
	r := &run{
		trace:   maps.Clone(o.Trace),
		name:    name,
		id:      id,
		logger:  driver.logger.With(slog.String("workflow", name), slog.String("run", id)),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		status:  workflow.Running,
		signals: map[string][][]byte{},
		changed: make(chan struct{}),
	}
	driver.runs[key] = r

	driver.wg.Add(1)

	go driver.execute(r, handler, slices.Clone(in), o.Delay)

	return r, nil
}

// complete publishes cancellation before considering a handler's late return.
// Handler success after observing Done cannot turn cancellation into completion;
// a result already published remains immutable even if Cancel is called later.
func (run *run) complete(out []byte, err error) {
	switch {
	case errors.Is(context.Cause(run.ctx), workflow.ErrTimeout):
		run.finish(workflow.Failed, nil, fmt.Errorf("workflow %s %s: %w", run.name, run.id, workflow.ErrTimeout))
		run.logger.WarnContext(run.ctx, "workflow timed out")
	case run.ctx.Err() != nil:
		run.finish(workflow.Canceled, nil, workflow.ErrCanceled)
		run.logger.DebugContext(run.ctx, "workflow canceled")
	case err != nil:
		run.finish(workflow.Failed, nil, err)
		run.logger.WarnContext(run.ctx, "workflow failed", slog.Any("error", err))
	default:
		run.finish(workflow.Completed, out, nil)
		run.logger.DebugContext(run.ctx, "workflow completed")
	}
}

// Result waits for the run to end or ctx to finish. Successful reads return
// independently owned encoded output; mutating it cannot change later reads.
// It returns [workflow.ErrNotFound] for an unknown or expired run. Canceling
// ctx ends only this wait, not the run.
//
//	output, err := driver.Result(ctx, "onboard", "user-42")
//	if err != nil {
//		return err
//	}
//	var greeting string
//	if err := workflow.DecodeValue(nil, output, &greeting); err != nil {
//		return err
//	}
//	_ = greeting
func (driver *Engine) Result(ctx context.Context, name, id string) ([]byte, error) {
	r, err := driver.lookup(name, id)

	if err != nil {
		return nil, err
	}

	select {
	case <-r.done:
		return r.outcome()
	default:
	}

	select {
	case <-r.done:
		return r.outcome()
	case <-ctx.Done():
		return nil, fmt.Errorf("result %s %s: %w", name, id, ctx.Err())
	}
}

// Status returns the state of the run.
// It observes the terminal supervisor decision, not whether every handler
// goroutine has returned. Unknown or expired runs return workflow.ErrNotFound.
//
//	status, err := driver.Status(ctx, "onboard", "user-42")
//	if err != nil {
//		return err
//	}
//	fmt.Println(status)
func (driver *Engine) Status(_ context.Context, name, id string) (workflow.Status, error) {
	r, err := driver.lookup(name, id)

	if err != nil {
		return "", err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.status, nil
}

// Cancel ends the run with [workflow.ErrCanceled] and waits until it has
// stopped or ctx is done. A finished run is left as it is.
// Waiting here observes the terminal run record, not termination of arbitrary
// handlers. Stop joins workflow and step-attempt goroutines during cleanup.
//
//	if err := driver.Cancel(ctx, "onboard", "user-42"); err != nil {
//		return err
//	}
//	_, err := driver.Result(ctx, "onboard", "user-42")
//	if err != nil && !errors.Is(err, workflow.ErrCanceled) {
//		return err
//	}
func (driver *Engine) Cancel(ctx context.Context, name, id string) error {
	r, err := driver.lookup(name, id)

	if err != nil {
		return err
	}

	r.cancel(workflow.ErrCanceled)

	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("cancel %s %s: %w", name, id, ctx.Err())
	}
}
