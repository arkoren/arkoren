package workflow

import (
	"context"
	"fmt"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// Run is a handle to one run of a workflow that produces Out. Get it from
// [Client.Start] or [Client.Run].
// A handle stores identity and shared client dependencies, not execution state;
// each observation queries the driver and can fail if the run no longer exists.
//
//	run := client.Run(Onboard, "user-42")
//	output, err := run.Result(ctx)
//	if err != nil {
//		return err
//	}
//	_ = output
type Run[Out any] struct {
	// telemetry is inherited from the client, not captured from a start context.
	telemetry *telemetry.Telemetry
	// name is the workflow name.
	name string

	// id is the run identifier.
	id string

	// driver serves the run.
	driver Driver

	// codec decodes the output.
	codec Codec

	// events receives [RunCanceled]; nil publishes nothing.
	events *event.Bus
}

// ID returns the run identifier.
// This is the exact caller-supplied identity, not a new engine execution ID.
//
//	run := client.Run(Onboard, "user-42")
//	fmt.Println(run.ID()) // user-42
func (run *Run[Out]) ID() string {
	return run.id
}

// Result waits for the run to end and returns its output, or the failure of
// the run: [ErrCanceled] after a cancel, [ErrTimeout] after
// [Builder.MaxDuration], or the error of the workflow. ctx bounds only the wait; when
// it ends Result returns its error and the run continues.
// Telemetry measures this wait, including decoding, not workflow execution.
// Repeated waits are separate observations and never emit terminal events.
//
//	output, err := run.Result(ctx)
//	if err != nil {
//		return err
//	}
//	_ = output
func (run *Run[Out]) Result(ctx context.Context) (_ Out, err error) {
	ctx, observation := beginObservation(ctx, run.telemetry, "result", telemetry.Client)
	defer finishClientObservation(ctx, observation, &err, endObservation)
	var out Out

	if !validID(run.id) {
		return out, fmt.Errorf("result %s: %w", run.name, ErrInvalidID)
	}

	raw, err := run.driver.Result(ctx, run.name, run.id)

	if err != nil {
		return out, fmt.Errorf("result %s %s: %w", run.name, run.id, err)
	}

	if err := DecodeValue(run.codec, raw, &out); err != nil {
		return out, fmt.Errorf("result %s %s: %w", run.name, run.id, err)
	}

	return out, nil
}

// Cancel stops the run. Canceling a finished run is not an error.
// Telemetry observes the cancel call, not confirmation of engine termination.
// The existing RunCanceled topic reports the successful call only.
//
//	if err := run.Cancel(ctx); err != nil {
//		return err
//	}
func (run *Run[Out]) Cancel(ctx context.Context) (err error) {
	ctx, observation := beginObservation(ctx, run.telemetry, "cancel", telemetry.Client)
	defer finishClientObservation(ctx, observation, &err, endObservation)

	if !validID(run.id) {
		return fmt.Errorf("cancel %s: %w", run.name, ErrInvalidID)
	}

	if err := run.driver.Cancel(ctx, run.name, run.id); err != nil {
		return fmt.Errorf("cancel %s %s: %w", run.name, run.id, err)
	}

	run.events.Emit(ctx, RunCanceled, RunEvent{
		Workflow: run.name,
		RunID:    run.id,
	})

	return nil
}
