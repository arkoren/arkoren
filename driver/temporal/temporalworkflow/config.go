package temporalworkflow

import (
	"log/slog"

	"arkoren.dev/core/service/workflow"
)

// DefaultPrefix is the name prefix used when [DriverBuilder.Prefix] is empty.
//
// Example:
//
//	component, err := NewDriver(temporal.New(), func(driver *DriverBuilder) {
//		driver.Prefix(DefaultPrefix)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
const DefaultPrefix = "arkoren"

// settings retains the optional construction policy for a [Runner] and a [Driver].
// Construction publishes this private value after applying its role callbacks;
// live dependencies and SDK internals retain their existing ownership.
type settings struct {
	// Prefix names the Temporal workflow types ("<prefix>-wf-<name>"), the
	// activity types ("<prefix>-step-<name>"), and the signals
	// ("<prefix>.sig.<name>"). Empty uses [DefaultPrefix]. A runner and the
	// drivers that talk to it must use the same prefix.
	Prefix string

	// Logger receives log lines of the driver. Nil uses [slog.Default].
	Logger *slog.Logger

	// Registry lets a [Driver] know the workflows: it sets the Temporal
	// execution timeout from MaxDuration and returns [workflow.ErrNotFound]
	// for an unregistered name. A [Runner] fills it in for its own driver.
	// Without it the run has no server-side execution timeout; the runner
	// still enforces MaxDuration inside the workflow.
	Registry *workflow.Registry
}

// DriverBuilder edits the construction settings of [NewDriver]. Callbacks share one
// fresh draft and run once in order; a nil callback panics. Retaining a draft
// does not permit changes to the published component. Dependencies and native
// SDK internals retain their existing ownership.
//
// Example:
//
//	component, err := NewDriver(temporal.New(), func(driver *DriverBuilder) {
//		driver.Logger(slog.Default())
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
type DriverBuilder struct {
	// config holds this draft's optional settings until publication.
	config settings
}

// driverSettings applies construction callbacks directly and publishes a detached
// settings root. An empty list preserves defaults; nil entries panic.
func driverSettings(configure ...DriverBuilderFunc) settings {
	config := settings{
		Prefix: DefaultPrefix,
	}
	driver := &DriverBuilder{
		config: config,
	}

	for _, configure := range configure {
		configure(driver)
	}

	return driver.config
}

// Prefix names the Temporal workflow types ("<prefix>-wf-<name>"), the
// activity types ("<prefix>-step-<name>"), and the signals
// ("<prefix>.sig.<name>"). Empty uses [DefaultPrefix]. A runner and the
// drivers that talk to it must use the same prefix.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(temporal.New(), func(driver *DriverBuilder) {
//		driver.Prefix("example")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (driver *DriverBuilder) Prefix(prefix string) *DriverBuilder {
	driver.config.Prefix = prefix

	return driver
}

// Logger receives log lines of the driver. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(temporal.New(), func(driver *DriverBuilder) {
//		driver.Logger(slog.Default())
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (driver *DriverBuilder) Logger(logger *slog.Logger) *DriverBuilder {
	driver.config.Logger = logger

	return driver
}

// Registry lets a [Driver] know the workflows: it sets the Temporal
// execution timeout from MaxDuration and returns [workflow.ErrNotFound]
// for an unregistered name. A [Runner] fills it in for its own driver.
// Without it the run has no server-side execution timeout; the runner
// still enforces MaxDuration inside the workflow.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(temporal.New(), func(driver *DriverBuilder) {
//		driver.Registry(nil)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (driver *DriverBuilder) Registry(registry *workflow.Registry) *DriverBuilder {
	driver.config.Registry = registry

	return driver
}

// RunnerBuilder edits the construction settings of [NewRunner]. Callbacks share one
// fresh draft and run once in order; a nil callback panics. Retaining a draft
// does not permit changes to the published component. Dependencies and native
// SDK internals retain their existing ownership.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(temporal.New(), registry, func(runner *RunnerBuilder) {
//		runner.Logger(slog.Default())
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
type RunnerBuilder struct {
	// config holds this draft's optional settings until publication.
	config settings
}

// runnerSettings applies construction callbacks directly and publishes a detached
// settings root. An empty list preserves defaults; nil entries panic.
func runnerSettings(configure ...RunnerBuilderFunc) settings {
	config := settings{
		Prefix: DefaultPrefix,
	}
	runner := &RunnerBuilder{
		config: config,
	}

	for _, configure := range configure {
		configure(runner)
	}

	return runner.config
}

// Prefix names the Temporal workflow types ("<prefix>-wf-<name>"), the
// activity types ("<prefix>-step-<name>"), and the signals
// ("<prefix>.sig.<name>"). Empty uses [DefaultPrefix]. A runner and the
// drivers that talk to it must use the same prefix.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(temporal.New(), registry, func(runner *RunnerBuilder) {
//		runner.Prefix("example")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (runner *RunnerBuilder) Prefix(prefix string) *RunnerBuilder {
	runner.config.Prefix = prefix

	return runner
}

// Logger receives log lines of the driver. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(temporal.New(), registry, func(runner *RunnerBuilder) {
//		runner.Logger(slog.Default())
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (runner *RunnerBuilder) Logger(logger *slog.Logger) *RunnerBuilder {
	runner.config.Logger = logger

	return runner
}

// DriverBuilderFunc configures a fresh client-driver draft during NewDriver.
// Callbacks run once synchronously in order; nil entries panic. Native host and
// registry dependencies remain shared after settings publication.
//
// Example:
//
//	policies := []DriverBuilderFunc{
//		func(driver *DriverBuilder) {
//			driver.Prefix("orders")
//		},
//	}
//	driver, err := NewDriver(temporal.New(), policies...)
//	if err != nil {
//		return err
//	}
//	_ = workflow.NewClient(driver)
type DriverBuilderFunc = func(*DriverBuilder)

// RunnerBuilderFunc configures a fresh worker-runner draft during NewRunner.
// Callbacks run once synchronously in order; nil entries panic. The positional
// registry is authoritative for both runner execution and its client driver.
//
// Example:
//
//	policies := []RunnerBuilderFunc{
//		func(runner *RunnerBuilder) {
//			runner.Prefix("orders")
//		},
//	}
//	registry, err := workflow.Handlers()
//	if err != nil {
//		return err
//	}
//	runner, err := NewRunner(temporal.New(), registry, policies...)
//	if err != nil {
//		return err
//	}
//	_ = runner.Driver()
type RunnerBuilderFunc = func(*RunnerBuilder)
