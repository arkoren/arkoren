package temporalworkflow

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	tworkflow "go.temporal.io/sdk/workflow"

	"arkoren.dev/core/service/workflow"
	temporalhost "arkoren.dev/driver/temporal"
	"arkoren.dev/driver/temporal/temporalworkflow/internal/execution"
)

// Runner serves the workflows and steps of a [workflow.Registry] on the
// worker of a temporal.Host (package arkoren.dev/driver/temporal). Create it
// with [NewRunner] before the host starts.
// It shares the registry's handlers and native host; it does not own that host's
// lifecycle. Workflow functions replay and must remain deterministic.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		return err
//	}
//	runner, err := NewRunner(temporalhost.New(), registry)
//	if err != nil {
//		return err
//	}
//	_ = workflow.NewClient(runner.Driver())
type Runner struct {
	// config is the configuration with defaults applied.
	config settings

	// registry holds the handlers.
	registry *workflow.Registry

	// driver starts runs and joins existing ones.
	driver *Driver
}

// NewRunner registers every workflow of registry as the Temporal workflow
// "<prefix>-wf-<name>" and every step as the activity "<prefix>-step-<name>"
// on host. It returns [temporalhost.ErrStarted] when host is already started.
// The workflows run on the task queue of the host.
//
// Cancellation, MaxDuration, and step retries follow the rules in the package
// documentation.
// Callbacks run once in order on one fresh [RunnerBuilder]; nil entries panic.
// The required registry always supplies the runner's driver registry; the
// runner draft has no Registry setting. Nil hosts and registries remain errors.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	runner, err := NewRunner(temporalhost.New(), registry, func(runner *RunnerBuilder) {
//		runner.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = runner.Driver()
func NewRunner(host *temporalhost.Host, registry *workflow.Registry, configure ...RunnerBuilderFunc) (*Runner, error) {
	if host == nil {
		return nil, errors.New("new temporal workflow runner: nil host")
	}

	if registry == nil {
		return nil, errors.New("new temporal workflow runner: nil registry")
	}

	if host.WorkerDisabled() {
		return nil, errors.New("new temporal workflow runner: host has no worker (NoWorker)")
	}

	config := runnerSettings(configure...)
	config.Registry = registry

	runner := newRunner(registry, temporalBackend{
		client:    host.Client,
		taskQueue: host.TaskQueue(),
	}, config)

	if err := host.Register(runner.register); err != nil {
		return nil, fmt.Errorf("register temporal workflows: %w", err)
	}

	return runner, nil
}

// newRunner builds a Runner whose driver uses backend.
func newRunner(registry *workflow.Registry, backend backend, config settings) *Runner {
	driver := newDriver(backend, config)

	return &Runner{
		config:   driver.config,
		registry: registry,
		driver:   driver,
	}
}

// Driver returns the [workflow.Driver] of the runner. It starts runs on the
// same host and sees the workflows of the registry. The returned pointer is
// shared and safe for concurrent command use; this transfers no host ownership.
//
// Example:
//
//	runner, err := NewRunner(host, registry)
//	if err != nil {
//		return err
//	}
//	client := workflow.NewClient(runner.Driver())
//	_ = client
func (runner *Runner) Driver() *Driver {
	return runner.driver
}

// register adds the workflows and activities to the worker.
func (runner *Runner) register(target worker.Registry) {
	for _, handler := range runner.registry.Workflows() {
		target.RegisterWorkflowWithOptions(runner.workflowFunc(handler), tworkflow.RegisterOptions{
			Name: runner.config.workflowType(handler.Name),
		})
	}

	target.RegisterActivityWithOptions(runner.join, activity.RegisterOptions{
		Name: runner.config.joinType(),
	})

	for _, handler := range runner.registry.Steps() {
		target.RegisterActivityWithOptions(runner.activityFunc(handler), activity.RegisterOptions{
			Name: runner.config.stepType(handler.Name),
		})
	}
}

// environment binds registry, names and native observation without sharing Runner.
func (runner *Runner) environment() execution.Environment {
	return execution.Environment{Registry: runner.registry, WorkflowType: runner.config.workflowType,
		StepType: runner.config.stepType, SignalName: runner.config.signalName,
		JoinType: runner.config.joinType, Result: func(ctx context.Context, id string) (payload, error) {
			return runner.driver.backend.result(ctx, id)
		}}
}

// workflowFunc binds one registered handler to the deterministic executor.
func (runner *Runner) workflowFunc(handler workflow.WorkflowHandler) func(tworkflow.Context, payload) (payload, error) {
	return runner.environment().Workflow(handler)
}

// activityFunc binds one registered step to its native attempt boundary.
func (runner *Runner) activityFunc(handler workflow.StepHandler) func(context.Context, payload) (payload, error) {
	return runner.environment().Activity(handler)
}

// join observes an existing run without taking its cancellation ownership.
func (runner *Runner) join(ctx context.Context, id string) (payload, error) {
	return runner.environment().Join(ctx, id)
}
