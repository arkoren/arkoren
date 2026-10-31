package hatchetschedule

import (
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	hatchethost "arkoren.dev/driver/hatchet"
)

// register returns a registration for the standalone task called name.
func (driver *Driver) register(name string, exclusive bool) hatchethost.Registration {
	return func(client *hatchet.Client) (hatchet.WorkflowBase, error) {
		options := []hatchet.StandaloneTaskOption{hatchet.WithExecutionTimeout(driver.config.MaxRunTime)}

		if exclusive {
			maxRuns := int32(1)
			strategy := hatchet.CancelNewest

			options = append(options, hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
				Expression:    "input.name",
				MaxRuns:       &maxRuns,
				LimitStrategy: &strategy,
			}))
		}

		return client.NewStandaloneTask(name, func(ctx hatchet.Context, input taskInput) (taskOutput, error) {
			return driver.handle(ctx, input)
		}, options...), nil
	}
}

// hostClients returns the clients of the started host. Host.Start creates
// them, so these calls only read.
func (driver *Driver) hostClients() (cronAPI, scheduleAPI, error) {
	client := driver.host.Client()

	if client == nil {
		return nil, nil, hatchethost.ErrNotStarted
	}

	return client.Crons(), client.Schedules(), nil
}
