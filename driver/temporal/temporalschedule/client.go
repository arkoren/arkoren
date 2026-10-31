package temporalschedule

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/client"
)

// backend is the part of the Temporal client the driver uses. Tests replace it
// with a fake.
type backend interface {
	// createSchedule creates a schedule.
	createSchedule(ctx context.Context, options client.ScheduleOptions) error

	// updateSchedule applies update to the schedule with the given ID.
	updateSchedule(ctx context.Context, id string, update func(client.ScheduleUpdateInput) (*client.ScheduleUpdate, error)) error

	// listScheduleIDs returns the IDs of all schedules.
	listScheduleIDs(ctx context.Context) ([]string, error)

	// deleteSchedule deletes the schedule with the given ID.
	deleteSchedule(ctx context.Context, id string) error

	// startWorkflow starts a workflow and returns its ID.
	startWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow string, in input) (string, error)

	// terminateWorkflow terminates the workflow with the given ID.
	terminateWorkflow(ctx context.Context, id, reason string) error
}

// temporalBackend implements backend over a real client.
type temporalBackend struct {
	// client is the Temporal client.
	client client.Client
}

// createSchedule creates a native schedule from the supplied options and
// returns the SDK failure unchanged for reconciliation.
func (adapter temporalBackend) createSchedule(ctx context.Context, options client.ScheduleOptions) error {
	_, err := adapter.client.ScheduleClient().Create(ctx, options)

	return err
}

// updateSchedule applies the supplied update callback through the native
// schedule handle. The SDK controls callback retries and error propagation.
func (adapter temporalBackend) updateSchedule(
	ctx context.Context, id string, update func(client.ScheduleUpdateInput) (*client.ScheduleUpdate, error),
) error {
	return adapter.client.ScheduleClient().GetHandle(ctx, id).Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: update,
	})
}

// listScheduleIDs iterates all native schedule pages and returns their
// identifiers. Listing and iterator failures stop reconciliation rather than
// returning a partial result.
func (adapter temporalBackend) listScheduleIDs(ctx context.Context) ([]string, error) {
	iterator, err := adapter.client.ScheduleClient().List(ctx, client.ScheduleListOptions{})

	if err != nil {
		return nil, err
	}

	var ids []string

	for iterator.HasNext() {
		entry, err := iterator.Next()

		if err != nil {
			return nil, fmt.Errorf("next schedule: %w", err)
		}

		ids = append(ids, entry.ID)
	}

	return ids, nil
}

// deleteSchedule deletes the native schedule identified by id and leaves error
// interpretation to reconciliation.
func (adapter temporalBackend) deleteSchedule(ctx context.Context, id string) error {
	return adapter.client.ScheduleClient().GetHandle(ctx, id).Delete(ctx)
}

// startWorkflow starts a native workflow with the supplied execution options
// and input, returning its workflow identifier or the SDK failure.
func (adapter temporalBackend) startWorkflow(
	ctx context.Context, options client.StartWorkflowOptions, workflow string, in input,
) (string, error) {
	run, err := adapter.client.ExecuteWorkflow(ctx, options, workflow, in)

	if err != nil {
		return "", err
	}

	return run.GetID(), nil
}

// terminateWorkflow requests termination of the workflow identified by id. The
// caller decides whether a missing workflow counts as success.
func (adapter temporalBackend) terminateWorkflow(ctx context.Context, id, reason string) error {
	return adapter.client.TerminateWorkflow(ctx, id, "", reason)
}
