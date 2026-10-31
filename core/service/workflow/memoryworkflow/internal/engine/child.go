package engine

import (
	"errors"

	"arkoren.dev/core/service/workflow"
)

// Child atomically creates or joins a run and waits within the parent's budget.
// startRun derives a context only for its newly inserted run; an existing run
// retains its original parent, input, and cancellation ownership unchanged.
//
//	output, err := execution.Child("fulfill", "order-42", input)
//	if err != nil {
//		return nil, err
//	}
//	_ = output
func (workflowRuntime *runtime) Child(name, id string, in []byte) ([]byte, error) {
	child, err := workflowRuntime.driver.startRun(workflowRuntime.run.ctx, name, id, in, workflow.StartOptions{Trace: workflowRuntime.run.trace})

	if err != nil && !errors.Is(err, workflow.ErrAlreadyStarted) {
		return nil, err
	}

	select {
	case <-child.done:
		return child.outcome()
	case <-workflowRuntime.run.ctx.Done():
		return nil, workflowRuntime.Err()
	}
}

// Spawn starts a workflow that outlives this run. An existing run yields
// [workflow.ErrAlreadyStarted].
func (workflowRuntime *runtime) Spawn(name, id string, in []byte) error {
	_, err := workflowRuntime.driver.startRun(workflowRuntime.driver.root, name, id, in, workflow.StartOptions{Trace: workflowRuntime.run.trace})

	return err
}
