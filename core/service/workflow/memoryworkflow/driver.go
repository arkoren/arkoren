package memoryworkflow

import (
	"context"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow/internal/engine"
)

// ErrStopped is returned by Start after [Driver.Stop].
// A stopped instance cannot be restarted; construct another instance instead.
//
//	if errors.Is(err, memoryworkflow.ErrStopped) {
//		return fmt.Errorf("workflow worker is shutting down: %w", err)
//	}
var ErrStopped = engine.ErrStopped

// ErrQueueFull is returned by Signal when a run already buffers the maximum
// number of undelivered values of one signal.
// It does not discard the existing queue; the rejected value is not enqueued.
//
//	if errors.Is(err, memoryworkflow.ErrQueueFull) {
//		return fmt.Errorf("signal was not accepted: %w", err)
//	}
var ErrQueueFull = engine.ErrQueueFull

// Driver implements [workflow.Driver] and runs the definitions of a
// [workflow.Registry] in goroutines of the process.
//
// It is single-process only: runs are lost on restart, and finished runs stay
// in memory for [workflow.WorkflowOptions.Retention], one hour when zero, and
// are then forgotten. A run with a Delay is Running while it waits. Each
// signal name buffers at most 1024 values per run; Signal then returns
// [ErrQueueFull].
// Call Stop at the acquiring owner's cleanup boundary, even after canceled
// runs: handler goroutines may still be completing cooperative cleanup.
//
//	driver := memoryworkflow.New(registry)
//	client := workflow.NewClient(driver)
//	defer func() {
//		if err := driver.Stop(ctx); err != nil {
//			logger.ErrorContext(ctx, "workflow cleanup incomplete", "error", err)
//		}
//	}()
//	_ = client.Driver()
type Driver struct {
	// engine owns all run state and joins its cooperative worker goroutines.
	engine *engine.Engine
}

// New creates a driver that runs the handlers of registry. A nil registry
// runs nothing. It never fails or invokes handlers during construction.
// Callbacks run directly once in order on one fresh draft. Nil entries panic;
// expanded nil/empty slices omit edits. Retaining the draft cannot change it.
//
//	driver := memoryworkflow.New(registry, func(driver *memoryworkflow.Builder) {
//		driver.Logger(slog.Default())
//	})
func New(registry *workflow.Registry, configure ...BuilderFunc) *Driver {
	draft := &Builder{}

	for _, configure := range configure {
		configure(draft)
	}

	return &Driver{engine: engine.NewEngine(registry, draft.logger)}
}

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
func (driver *Driver) Stop(ctx context.Context) error {
	return driver.engine.Stop(ctx)
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
func (driver *Driver) Start(ctx context.Context, name, id string, in []byte, o workflow.StartOptions) error {
	return driver.engine.Start(ctx, name, id, in, o)
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
func (driver *Driver) Result(ctx context.Context, name, id string) ([]byte, error) {
	return driver.engine.Result(ctx, name, id)
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
func (driver *Driver) Status(ctx context.Context, name, id string) (workflow.Status, error) {
	return driver.engine.Status(ctx, name, id)
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
func (driver *Driver) Cancel(ctx context.Context, name, id string) error {
	return driver.engine.Cancel(ctx, name, id)
}

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
func (driver *Driver) Signal(ctx context.Context, name, id, signal string, v []byte) error {
	return driver.engine.Signal(ctx, name, id, signal, v)
}
