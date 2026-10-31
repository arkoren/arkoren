package restateworkflow

import (
	"log/slog"
)

// settings retains the optional construction policy for a [Runner] and a [Driver].
// Construction publishes this private value after applying its role callbacks;
// live dependencies and SDK internals retain their existing ownership.
type settings struct {
	// Prefix names the Restate services: "arkoren" and the workflow
	// "onboard" give the service ArkorenWf_onboard. Dots, dashes, and other
	// separators of the prefix are dropped and the next letter is
	// upper-cased. The runner and every driver that talks to it must use the
	// same prefix. The default is "arkoren".
	Prefix string

	// Logger receives log lines. Nil uses [slog.Default].
	Logger *slog.Logger
}

// DriverBuilder edits the construction settings of [NewDriver]. Callbacks share one
// fresh draft and run once in order; a nil callback panics. Retaining a draft
// does not permit changes to the published component. Dependencies and native
// SDK internals retain their existing ownership.
//
// Example:
//
//	component, err := NewDriver(restate.New(), func(driver *DriverBuilder) {
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
		Prefix: "arkoren",
	}
	driver := &DriverBuilder{
		config: config,
	}

	for _, configure := range configure {
		configure(driver)
	}

	return driver.config
}

// Prefix names the Restate services: "arkoren" and the workflow
// "onboard" give the service ArkorenWf_onboard. Dots, dashes, and other
// separators of the prefix are dropped and the next letter is
// upper-cased. The runner and every driver that talks to it must use the
// same prefix. The default is "arkoren".
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(restate.New(), func(driver *DriverBuilder) {
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

// Logger receives log lines. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(restate.New(), func(driver *DriverBuilder) {
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
//	component, err := NewRunner(restate.New(), registry, func(runner *RunnerBuilder) {
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
		Prefix: "arkoren",
	}
	runner := &RunnerBuilder{
		config: config,
	}

	for _, configure := range configure {
		configure(runner)
	}

	return runner.config
}

// Prefix names the Restate services: "arkoren" and the workflow
// "onboard" give the service ArkorenWf_onboard. Dots, dashes, and other
// separators of the prefix are dropped and the next letter is
// upper-cased. The runner and every driver that talks to it must use the
// same prefix. The default is "arkoren".
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(restate.New(), registry, func(runner *RunnerBuilder) {
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

// Logger receives log lines. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(restate.New(), registry, func(runner *RunnerBuilder) {
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

// DriverBuilderFunc is an ordinary callback alias for client-driver settings.
// NewDriver invokes callbacks once in order after validating the host; nil
// entries panic and retained drafts cannot mutate the published driver.
//
// Example:
//
//	policies := []DriverBuilderFunc{func(driver *DriverBuilder) {
//		driver.Prefix("orders")
//	}}
//	_, err := NewDriver(restate.New(), policies...)
//	if err != nil {
//		panic(err)
//	}
type DriverBuilderFunc = func(*DriverBuilder)

// RunnerBuilderFunc is an ordinary callback alias for worker settings.
// NewRunner invokes callbacks once in order after checking host and registry;
// nil entries panic. Native services, loggers, and closures remain shared.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	_, err = NewRunner(restate.New(), registry, func(runner *RunnerBuilder) {
//		runner.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
type RunnerBuilderFunc = func(*RunnerBuilder)
