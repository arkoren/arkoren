package restateworkflow

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/restate"
)

// Runner serves the workflows of a [workflow.Registry] as Restate workflow
// services, one per workflow. Create it with [NewRunner] before the host
// starts.
// Handler functions stay shared through the immutable registry. The runner
// publishes no workflow-completion telemetry; actual step attempts are observed
// by the registry independently of replayed orchestration.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	worker, err := NewRunner(restate.New(), registry)
//	if err != nil {
//		panic(err)
//	}
//	_ = worker.Services()
type Runner struct {
	// config is the configuration with defaults applied.
	config settings

	// registry holds the handlers.
	registry *workflow.Registry

	// namer maps workflow names to service names.
	namer serviceNamer

	// active holds the runtime of each attempt that runs in this process,
	// by service and key. The Cancel handler uses it.
	active sync.Map
}

// NewRunner binds one Restate workflow service per workflow of registry to
// host. The service is named from the prefix and the workflow name, for
// example "ArkorenWf_onboard", and has three handlers: Run executes the
// workflow, Signal resolves durable signal promises, and Cancel interrupts a
// local active attempt. Signal and Cancel are shared handlers.
// Steps run inline in the Run handler as durable Run actions of
// Restate and need no service. It returns [restate.ErrStarted] when host is
// already started.
//
// Cancellation, MaxDuration, and step retries follow the rules in the package
// documentation.
// Callbacks run once in order on one fresh [RunnerBuilder]; nil entries panic.
// Published settings are independent of a retained draft. Nil hosts and
// registries remain construction errors, not optional settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	runner, err := NewRunner(restate.New(), registry, func(runner *RunnerBuilder) {
//		runner.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = runner.Services()
func NewRunner(host *restate.Host, registry *workflow.Registry, configure ...RunnerBuilderFunc) (*Runner, error) {
	if host == nil {
		return nil, errors.New("new restate workflow runner: nil host")
	}

	if registry == nil {
		return nil, errors.New("new restate workflow runner: nil registry")
	}

	config := runnerSettings(configure...)
	config.Prefix = cmp.Or(config.Prefix, "arkoren")
	config.Logger = cmp.Or(config.Logger, slog.Default())

	runner := &Runner{
		config:   config,
		registry: registry,
		namer:    newServiceNamer(config.Prefix),
	}

	for _, handler := range registry.Workflows() {
		definition := runner.definition(handler)

		if err := host.Bind(definition); err != nil {
			return nil, fmt.Errorf("bind %s: %w", definition.Name(), err)
		}
	}

	return runner, nil
}

// Services returns the Restate service names the runner serves, in workflow
// name order.
// The returned slice is independently owned; changing it cannot rename bound
// services. An empty registry returns an empty slice and binds no endpoint.
//
// Example:
//
//	for _, name := range worker.Services() {
//		fmt.Println(name)
//	}
func (runner *Runner) Services() []string {
	handlers := runner.registry.Workflows()
	names := make([]string, len(handlers))

	for index, handler := range handlers {
		names[index] = runner.namer.service(handler.Name)
	}

	return names
}
