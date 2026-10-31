package hatchetworkflow

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
	hatchethost "arkoren.dev/driver/hatchet"
)

// Runner registers the workflows and steps of a [workflow.Registry] on a
// Hatchet host. Create it before host.Start; the worker runs them. Definitions
// and completed settings are retained without invoking application handlers.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		return err
//	}
//	runner, err := NewRunner(hatchethost.New(), registry)
//	if err != nil {
//		return err
//	}
//	client := workflow.NewClient(runner.Driver())
//	_ = client
type Runner struct {
	// registry owns worker-side telemetry and carrier extraction.
	registry *workflow.Registry
	// config retains the completed construction settings used by this instance.
	config settings
	// logger receives operational records without changing the engine's execution
	// state.
	logger *slog.Logger
	// driver submits and resolves runs using the same settings as this runner.
	driver *Driver

	// mutex serializes access to the mutable state below it.
	mutex sync.RWMutex
	// steps indexes the native standalone tasks registered for step execution.
	steps map[string]*hatchet.StandaloneTask
}

// NewRunner registers every workflow and step of registry on host as a
// standalone task. Call it before host.Start. The host must run a worker, and
// the Hatchet engine must support durable eviction (v0.80.0 or newer); Start
// checks it. It performs no I/O.
// Callbacks run once in order on one fresh [RunnerBuilder]; nil entries panic.
// The runner and its driver publish the completed settings without replaying
// callbacks. Required nil hosts and registries remain construction errors.
// Unsupported native numeric ranges and unlimited registered step timeouts return
// workflow.ErrUnsupported before registration. Every workflow needs MaxDuration
// in its core definition. Registered retry delays use RetryAfter on engine
// v0.111.0+; see the package caveats for older engines and server-side caps.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	runner, err := NewRunner(hatchethost.New(), registry, func(runner *RunnerBuilder) {
//		runner.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = runner.Driver()
func NewRunner(host *hatchethost.Host, registry *workflow.Registry, configure ...RunnerBuilderFunc) (*Runner, error) {
	if host == nil {
		return nil, errors.New("new hatchet workflow runner: nil host")
	}

	if registry == nil {
		return nil, errors.New("new hatchet workflow runner: nil registry")
	}

	if !host.HasWorker() {
		return nil, errors.New("new hatchet workflow runner: host runs without a worker")
	}

	driver, err := newHostDriver(host, runnerSettings(configure...))

	if err != nil {
		return nil, err
	}

	if err := validateRegistry(registry, driver.config); err != nil {
		return nil, err
	}

	runner := &Runner{
		registry: registry,
		config:   driver.config,
		logger:   driver.logger,
		driver:   driver,
		steps:    map[string]*hatchet.StandaloneTask{},
	}

	if err := runner.register(host); err != nil {
		return nil, err
	}

	return runner, nil
}

// register publishes the validated declaration set on the host before startup.
// Native tasks are constructed later by Host.Start; no SDK I/O occurs here.
func (runner *Runner) register(host *hatchethost.Host) error {
	if err := host.Register(runner.registerMarker()); err != nil {
		return fmt.Errorf("register signal marker: %w", err)
	}

	for _, handler := range runner.registry.Steps() {
		if err := host.Register(runner.registerStep(handler)); err != nil {
			return fmt.Errorf("register step %s: %w", handler.Name, err)
		}
	}

	for _, handler := range runner.registry.Workflows() {
		if err := host.Register(runner.registerWorkflow(handler)); err != nil {
			return fmt.Errorf("register workflow %s: %w", handler.Name, err)
		}
	}

	return nil
}

// Driver returns the driver that starts and queries the runs of this runner.
// It shares the runner's host and completed settings; it owns no separate native
// connection and must not be independently stopped.
//
// Example:
//
//	client := workflow.NewClient(runner.Driver())
//	_ = client
func (runner *Runner) Driver() *Driver {
	return runner.driver
}

// stepTask returns the registered task of a step.
func (runner *Runner) stepTask(name string) (*hatchet.StandaloneTask, bool) {
	runner.mutex.RLock()
	defer runner.mutex.RUnlock()

	task, ok := runner.steps[name]

	return task, ok
}
