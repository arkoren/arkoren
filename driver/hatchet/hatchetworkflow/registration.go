package hatchetworkflow

import (
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
	hatchethost "arkoren.dev/driver/hatchet"
)

// registerMarker returns the registration of the signal marker workflow. A run
// of it does nothing: its input holds the first value of one signal, and its
// physical-execution idempotency key lets only the first sender create it.
// The TTL remains Lookback, independent of the workflow's reusable logical key;
// applications must retain markers through the last possible signal/replay.
func (runner *Runner) registerMarker() hatchethost.Registration {
	return func(client *hatchet.Client) (hatchet.WorkflowBase, error) {
		return client.NewStandaloneTask(runner.driver.signalWorkflow(),
			func(hatchet.Context, wire) (wire, error) {
				return wire{}, nil
			},
			hatchet.WithExecutionTimeout(time.Minute),
			hatchet.WithScheduleTimeout(runner.config.ScheduleTimeout),
			hatchet.WithWorkflowIdempotency(hatchet.IdempotencyConfig{
				Expression: "input.key",
				Method:     hatchet.IdempotencyMethodTTL,
				TTL:        runner.config.Lookback,
			}),
		), nil
	}
}

// registerStep returns the registration of one step.
func (runner *Runner) registerStep(handler workflow.StepHandler) hatchethost.Registration {
	return func(client *hatchet.Client) (hatchet.WorkflowBase, error) {
		options := handler.Options
		attempts := max(options.Attempts, 1)

		task := client.NewStandaloneTask(runner.config.Prefix+"-step-"+handler.Name,
			runner.stepFunc(handler),
			hatchet.WithRetries(attempts-1),
			hatchet.WithRetryBackoff(float32(max(options.Factor, 1)), ceilSeconds(options.Max)),
			hatchet.WithExecutionTimeout(stepTimeout(options.Timeout)),
			hatchet.WithScheduleTimeout(runner.config.ScheduleTimeout),
		)

		runner.mutex.Lock()
		runner.steps[handler.Name] = task
		runner.mutex.Unlock()

		return task, nil
	}
}

// registerWorkflow returns the registration of one workflow.
func (runner *Runner) registerWorkflow(handler workflow.WorkflowHandler) hatchethost.Registration {
	return func(client *hatchet.Client) (hatchet.WorkflowBase, error) {
		options := handler.Options
		retention := options.Retention

		if retention <= 0 {
			retention = runner.config.Lookback
		}

		taskOptions := []hatchet.StandaloneTaskOption{
			hatchet.WithExecutionTimeout(wholeSeconds(options.MaxDuration + runner.config.TimeoutMargin)),
			hatchet.WithScheduleTimeout(runner.config.ScheduleTimeout),
			hatchet.WithEvictionPolicy(&hatchet.EvictionPolicy{
				TTL:                   runner.config.EvictionTTL,
				AllowCapacityEviction: true,
			}),
			hatchet.WithWorkflowIdempotency(hatchet.IdempotencyConfig{
				Expression: "input.key",
				Method:     hatchet.IdempotencyMethodTTL,
				TTL:        options.MaxDuration + retention,
			}),
		}

		if options.Description != "" {
			taskOptions = append(taskOptions, hatchet.WithWorkflowDescription(options.Description))
		}

		task := client.NewStandaloneDurableTask(runner.driver.workflowName(handler.Name),
			runner.workflowFunc(handler), taskOptions...)

		return task, nil
	}
}
