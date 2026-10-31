package workflow

import (
	"context"
	"fmt"

	"arkoren.dev/core/service/telemetry"
)

// Status is the persisted, lowercase state of a run. Its empty zero value is
// invalid and does not identify a running workflow.
//
// Example: _ = workflow.Status("running") == workflow.Running
type Status string

// The states of a run.
const (
	// Running means the run has not ended.
	//
	// Example: _ = workflow.Status("running") == workflow.Running
	Running Status = "running"

	// Completed means the run returned an output.
	//
	// Example: _ = workflow.Completed.String() // "completed"
	Completed Status = "completed"

	// Failed means the run returned an error or timed out.
	//
	// Example: _ = workflow.Failed.String() // "failed"
	Failed Status = "failed"

	// Canceled means the run was canceled.
	//
	// Example: _ = workflow.Canceled.String() // "canceled"
	Canceled Status = "canceled"
)

// String returns the status's persisted value, including empty and unknown
// values. It does not validate a value received from storage.
//
// Example: _ = workflow.Completed.String() // "completed"
func (status Status) String() string {
	return string(status)
}

// Status returns the state of the run.
// Telemetry measures the query only; it never emits a terminal event.
//
//	status, err := run.Status(ctx)
//	if err != nil {
//		return err
//	}
//	_ = status == workflow.Completed
func (run *Run[Out]) Status(ctx context.Context) (_ Status, err error) {
	ctx, observation := beginObservation(ctx, run.telemetry, "status", telemetry.Client)
	defer finishClientObservation(ctx, observation, &err, endObservation)

	if !validID(run.id) {
		return "", fmt.Errorf("status %s: %w", run.name, ErrInvalidID)
	}

	status, err := run.driver.Status(ctx, run.name, run.id)

	if err != nil {
		return "", fmt.Errorf("status %s %s: %w", run.name, run.id, err)
	}

	return status, nil
}
