package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"arkoren.dev/core/service/workflow"
)

// runtime is the [workflow.Runtime] of one run.
type runtime struct {
	// driver supplies registrations and starts child or detached runs.
	driver *Engine

	// run owns this execution's cancellation, identity, and signal queues.
	run *run
}

// ID returns the run identifier.
func (workflowRuntime *runtime) ID() string {
	return workflowRuntime.run.id
}

// Name returns the workflow name.
func (workflowRuntime *runtime) Name() string {
	return workflowRuntime.run.name
}

// Now returns the current time.
func (workflowRuntime *runtime) Now() time.Time {
	return time.Now()
}

// UUID returns a random version 4 UUID.
func (workflowRuntime *runtime) UUID() string {
	var id [16]byte

	// crypto/rand.Read fills the buffer or terminates the process; it never returns an error.
	_, _ = rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:])
}

// Logger returns the driver logger with the workflow and run.
func (workflowRuntime *runtime) Logger() *slog.Logger {
	return workflowRuntime.run.logger
}

// Done is closed when the run ends early.
func (workflowRuntime *runtime) Done() <-chan struct{} {
	return workflowRuntime.run.ctx.Done()
}

// Raw returns the internal run state.
func (workflowRuntime *runtime) Raw() any {
	return workflowRuntime.run
}

// Err returns [workflow.ErrTimeout] or [workflow.ErrCanceled] once the run
// ended, otherwise nil.
func (workflowRuntime *runtime) Err() error {
	if workflowRuntime.run.ctx.Err() == nil {
		return nil
	}

	if cause := context.Cause(workflowRuntime.run.ctx); errors.Is(cause, workflow.ErrTimeout) {
		return cause
	}

	return workflow.ErrCanceled
}

// Sleep waits for d on the system clock.
func (workflowRuntime *runtime) Sleep(d time.Duration) error {
	if err := workflowRuntime.Err(); err != nil {
		return err
	}

	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-workflowRuntime.run.ctx.Done():
		return workflowRuntime.Err()
	}
}
