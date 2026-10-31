package temporalworkflow

import (
	"context"
	"time"

	"arkoren.dev/core/service/workflow"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// startRequest is one workflow start.
type startRequest struct {
	// name is the Temporal workflow type.
	name string

	// id is the workflow ID.
	id string

	// input is the workflow input.
	input payload

	// timeout is the execution timeout. Zero means none.
	timeout time.Duration

	// delay postpones the first workflow task.
	delay time.Duration

	// memo is the memo of the run.
	memo map[string]any
}

// backend is the part of the Temporal client the driver uses. Tests replace
// it with a fake.
type backend interface {
	// start starts a run and fails when the ID is taken.
	start(ctx context.Context, request startRequest) error

	// result waits for the run and returns its output.
	result(ctx context.Context, id string) (payload, error)

	// describe verifies the native workflow type before returning run status.
	describe(ctx context.Context, id, workflowType string) (enumspb.WorkflowExecutionStatus, error)

	// cancel requests the cancellation of the run.
	cancel(ctx context.Context, id string) error

	// signal sends a signal to the run.
	signal(ctx context.Context, id, name string, v payload) error
}

// temporalBackend implements backend over a real client.
type temporalBackend struct {
	// client returns the Temporal client, or nil before the host started.
	client func() client.Client

	// taskQueue is the task queue of the runs.
	taskQueue string
}

// conn returns the client or [errNotStarted].
func (adapter temporalBackend) conn() (client.Client, error) {
	conn := adapter.client()

	if conn == nil {
		return nil, errNotStarted
	}

	return conn, nil
}

// start submits the workflow with conflicting and previously completed IDs
// rejected by native policy. A missing host client or SDK failure is returned
// unchanged for the driver to translate.
func (adapter temporalBackend) start(ctx context.Context, request startRequest) error {
	conn, err := adapter.conn()

	if err != nil {
		return err
	}

	_, err = conn.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       request.id,
		TaskQueue:                                adapter.taskQueue,
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
		WorkflowExecutionTimeout:                 request.timeout,
		StartDelay:                               request.delay,
		Memo:                                     request.memo,
	}, request.name, request.input)

	return err
}

// result waits for the native workflow and decodes its opaque payload.
// Connection and execution failures are returned without changing their error
// identity.
func (adapter temporalBackend) result(ctx context.Context, id string) (payload, error) {
	conn, err := adapter.conn()

	if err != nil {
		return nil, err
	}

	var out payload

	if err := conn.GetWorkflow(ctx, id, "").Get(ctx, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// describe checks the workflow's native type before exposing its status. IDs
// remain namespace-wide, but the public handle is still bound to a named
// workflow; a mismatched type therefore has the same meaning as a missing run.
func (adapter temporalBackend) describe(
	ctx context.Context,
	id, workflowType string,
) (enumspb.WorkflowExecutionStatus, error) {
	conn, err := adapter.conn()

	if err != nil {
		return 0, err
	}

	description, err := conn.DescribeWorkflow(ctx, id, "")

	if err != nil {
		return 0, err
	}

	if description.WorkflowType.Name != workflowType {
		return 0, workflow.ErrNotFound
	}

	return description.Status, nil
}

// cancel requests cancellation of the addressed engine resource. Missing-
// resource handling follows the surrounding gateway contract.
func (adapter temporalBackend) cancel(ctx context.Context, id string) error {
	conn, err := adapter.conn()

	if err != nil {
		return err
	}

	return conn.CancelWorkflow(ctx, id, "")
}

// signal sends the signal request to the addressed workflow and returns the
// translated native failure, if any.
func (adapter temporalBackend) signal(ctx context.Context, id, name string, v payload) error {
	conn, err := adapter.conn()

	if err != nil {
		return err
	}

	return conn.SignalWorkflow(ctx, id, "", name, v)
}
