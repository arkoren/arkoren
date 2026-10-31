package hatchetworkflow

import (
	"fmt"
	"log/slog"
	"time"

	"arkoren.dev/driver/hatchet"
)

// Default configuration values.
const (
	// DefaultPrefix is the name prefix used when [DriverBuilder.Prefix] is empty.
	//
	// Example:
	//
	//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
	//		driver.Prefix(DefaultPrefix)
	//	})
	//	if err != nil {
	//		panic(err)
	//	}
	//	_ = component
	DefaultPrefix = "arkoren"

	// DefaultLookback is the run history searched when [DriverBuilder.Lookback] is
	// zero.
	//
	// Example:
	//
	//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
	//		driver.Lookback(DefaultLookback)
	//	})
	//	if err != nil {
	//		panic(err)
	//	}
	//	_ = component
	DefaultLookback = 30 * 24 * time.Hour

	// DefaultTimeoutMargin is the margin added to MaxDuration when
	// [DriverBuilder.TimeoutMargin] is zero.
	//
	// Example:
	//
	//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
	//		driver.TimeoutMargin(DefaultTimeoutMargin)
	//	})
	//	if err != nil {
	//		panic(err)
	//	}
	//	_ = component
	DefaultTimeoutMargin = time.Minute

	// DefaultScheduleTimeout is how long a run may wait for a worker slot
	// when [DriverBuilder.ScheduleTimeout] is zero.
	//
	// Example:
	//
	//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
	//		driver.ScheduleTimeout(DefaultScheduleTimeout)
	//	})
	//	if err != nil {
	//		panic(err)
	//	}
	//	_ = component
	DefaultScheduleTimeout = 24 * time.Hour

	// DefaultEvictionTTL is how long a durable wait must last before the run
	// leaves its worker slot, when [DriverBuilder.EvictionTTL] is zero.
	//
	// Example:
	//
	//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
	//		driver.EvictionTTL(DefaultEvictionTTL)
	//	})
	//	if err != nil {
	//		panic(err)
	//	}
	//	_ = component
	DefaultEvictionTTL = time.Minute

	// DefaultPollInterval caps the delay between status polls of
	// [Driver.Result] when [DriverBuilder.PollInterval] is zero.
	//
	// Example:
	//
	//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
	//		driver.PollInterval(DefaultPollInterval)
	//	})
	//	if err != nil {
	//		panic(err)
	//	}
	//	_ = component
	DefaultPollInterval = 5 * time.Second

	// DefaultLookupGrace is how long a run that is not yet listed may
	// appear when [DriverBuilder.LookupGrace] is zero.
	//
	// Example:
	//
	//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
	//		driver.LookupGrace(DefaultLookupGrace)
	//	})
	//	if err != nil {
	//		panic(err)
	//	}
	//	_ = component
	DefaultLookupGrace = 1500 * time.Millisecond
)

// settings retains the optional construction policy for a [Runner] and a [Driver].
// Construction publishes this private value after applying its role callbacks;
// live dependencies and SDK internals retain their existing ownership.
type settings struct {
	// Prefix names the Hatchet workflows ("<prefix>-wf-<name>" and
	// "<prefix>-step-<name>"), the signal events, and the idempotency keys.
	// Empty uses [DefaultPrefix]. A runner and the driver that starts its
	// runs must use the same prefix and effective host namespace. Namespace
	// and prefix are independently encoded in tenant-wide execution keys.
	Prefix string

	// Lookback is how far back [Driver] searches for a run it did not start
	// itself. Runs older than that are unknown to it. Zero uses
	// [DefaultLookback]; keep it above the required run-history interval,
	// including queueing, execution, retention, and recovery. It also
	// retains signal-marker idempotency, measured from the first signal.
	// Keep it longer than the first-signal-to-last-signal/replay interval,
	// including queued time, and retain engine events and marker inputs for
	// that interval. Complete physical-scope matching cannot recover deleted
	// data. Expiry can otherwise replace or lose a run's first signal value.
	Lookback time.Duration

	// TimeoutMargin is added to MaxDuration to form the Hatchet execution
	// timeout, which is only a backstop: the runtime enforces MaxDuration
	// itself. Zero uses [DefaultTimeoutMargin].
	TimeoutMargin time.Duration

	// ScheduleTimeout is how long a run may wait for a worker slot. Zero uses
	// [DefaultScheduleTimeout].
	ScheduleTimeout time.Duration

	// EvictionTTL is how long a durable wait lasts before the run leaves its
	// worker slot. Zero uses [DefaultEvictionTTL].
	EvictionTTL time.Duration

	// PollInterval caps the delay between status polls of Result. Zero uses
	// [DefaultPollInterval].
	PollInterval time.Duration

	// LookupGrace is how long the driver waits for a run that Hatchet does
	// not list yet, because listing lags behind starting. A negative value
	// turns the wait off. Zero uses [DefaultLookupGrace]. Start verifies the
	// returned physical ID before caching it; if this wait expires, it can
	// return an error even though the trigger already started work. Cold
	// lookups of unknown logical IDs list once rather than waiting.
	LookupGrace time.Duration

	// Logger receives log records. Nil uses [slog.Default].
	Logger *slog.Logger
}

// validate rejects negative native durations after zero defaults are resolved.
// LookupGrace is intentionally excluded: negative disables consistency retries.
// No native client or workflow registration is touched on failure.
func (config settings) validate() error {
	durations := [...]struct {
		// name identifies the builder setting in a construction error.
		name string
		// value is the normalized native duration, in nanoseconds.
		value time.Duration
	}{
		{"Lookback", config.Lookback},
		{"TimeoutMargin", config.TimeoutMargin},
		{"ScheduleTimeout", config.ScheduleTimeout},
		{"EvictionTTL", config.EvictionTTL},
		{"PollInterval", config.PollInterval},
	}

	for _, duration := range durations {
		if duration.value < 0 {
			return fmt.Errorf("hatchet workflow %s must not be negative: %w", duration.name, hatchet.ErrInvalidConfig)
		}
	}

	return nil
}

// DriverBuilder edits the construction settings of [NewDriver]. Callbacks share one
// fresh draft and run once in order; a nil callback panics. Retaining a draft
// does not permit changes to the published component. Dependencies and native
// SDK internals retain their existing ownership.
//
// Example:
//
//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
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

// Prefix names the Hatchet workflows ("<prefix>-wf-<name>" and
// "<prefix>-step-<name>"), the signal events, and the idempotency keys.
// Empty uses [DefaultPrefix]. A runner and the driver that starts its
// runs must use the same prefix and effective host namespace. Namespace
// and prefix are independently encoded in tenant-wide execution keys.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
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

// Lookback is how far back [Driver] searches for a run it did not start
// itself. Runs older than that are unknown to it. Zero uses
// [DefaultLookback]; keep it above the required run-history interval,
// including queueing, execution, retention, and recovery. It also
// retains signal-marker idempotency, measured from the first signal.
// Keep it longer than the first-signal-to-last-signal/replay interval,
// including queued time, and retain engine events and marker inputs for
// that interval. Complete physical-scope matching cannot recover deleted
// data. Expiry can otherwise replace or lose a run's first signal value.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
//		driver.Lookback(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (driver *DriverBuilder) Lookback(d time.Duration) *DriverBuilder {
	driver.config.Lookback = d

	return driver
}

// TimeoutMargin is added to MaxDuration to form the Hatchet execution
// timeout, which is only a backstop: the runtime enforces MaxDuration
// itself. Zero uses [DefaultTimeoutMargin].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
//		driver.TimeoutMargin(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (driver *DriverBuilder) TimeoutMargin(d time.Duration) *DriverBuilder {
	driver.config.TimeoutMargin = d

	return driver
}

// ScheduleTimeout is how long a run may wait for a worker slot. Zero uses
// [DefaultScheduleTimeout].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
//		driver.ScheduleTimeout(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (driver *DriverBuilder) ScheduleTimeout(d time.Duration) *DriverBuilder {
	driver.config.ScheduleTimeout = d

	return driver
}

// EvictionTTL is how long a durable wait lasts before the run leaves its
// worker slot. Zero uses [DefaultEvictionTTL].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
//		driver.EvictionTTL(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (driver *DriverBuilder) EvictionTTL(d time.Duration) *DriverBuilder {
	driver.config.EvictionTTL = d

	return driver
}

// PollInterval caps the delay between status polls of Result. Zero uses
// [DefaultPollInterval].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
//		driver.PollInterval(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (driver *DriverBuilder) PollInterval(d time.Duration) *DriverBuilder {
	driver.config.PollInterval = d

	return driver
}

// LookupGrace is how long the driver waits for a run that Hatchet does
// not list yet, because listing lags behind starting. A negative value
// turns the wait off. Zero uses [DefaultLookupGrace]. Start verifies the
// returned physical ID before caching it; if this wait expires, it can
// return an error even though the trigger already started work. Cold
// lookups of unknown logical IDs list once rather than waiting.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
//		driver.LookupGrace(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (driver *DriverBuilder) LookupGrace(d time.Duration) *DriverBuilder {
	driver.config.LookupGrace = d

	return driver
}

// Logger receives log records. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
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
//	component, err := NewRunner(hatchet.New(), registry, func(runner *RunnerBuilder) {
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

// Prefix names the Hatchet workflows ("<prefix>-wf-<name>" and
// "<prefix>-step-<name>"), the signal events, and the idempotency keys.
// Empty uses [DefaultPrefix]. A runner and the driver that starts its
// runs must use the same prefix and effective host namespace. Namespace
// and prefix are independently encoded in tenant-wide execution keys.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(hatchet.New(), registry, func(runner *RunnerBuilder) {
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

// Lookback is how far back [Driver] searches for a run it did not start
// itself. Runs older than that are unknown to it. Zero uses
// [DefaultLookback]; keep it above the required run-history interval,
// including queueing, execution, retention, and recovery. It also
// retains signal-marker idempotency, measured from the first signal.
// Keep it longer than the first-signal-to-last-signal/replay interval,
// including queued time, and retain engine events and marker inputs for
// that interval. Complete physical-scope matching cannot recover deleted
// data. Expiry can otherwise replace or lose a run's first signal value.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(hatchet.New(), registry, func(runner *RunnerBuilder) {
//		runner.Lookback(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (runner *RunnerBuilder) Lookback(d time.Duration) *RunnerBuilder {
	runner.config.Lookback = d

	return runner
}

// TimeoutMargin is added to MaxDuration to form the Hatchet execution
// timeout, which is only a backstop: the runtime enforces MaxDuration
// itself. Zero uses [DefaultTimeoutMargin].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(hatchet.New(), registry, func(runner *RunnerBuilder) {
//		runner.TimeoutMargin(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (runner *RunnerBuilder) TimeoutMargin(d time.Duration) *RunnerBuilder {
	runner.config.TimeoutMargin = d

	return runner
}

// ScheduleTimeout is how long a run may wait for a worker slot. Zero uses
// [DefaultScheduleTimeout].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(hatchet.New(), registry, func(runner *RunnerBuilder) {
//		runner.ScheduleTimeout(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (runner *RunnerBuilder) ScheduleTimeout(d time.Duration) *RunnerBuilder {
	runner.config.ScheduleTimeout = d

	return runner
}

// EvictionTTL is how long a durable wait lasts before the run leaves its
// worker slot. Zero uses [DefaultEvictionTTL].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(hatchet.New(), registry, func(runner *RunnerBuilder) {
//		runner.EvictionTTL(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (runner *RunnerBuilder) EvictionTTL(d time.Duration) *RunnerBuilder {
	runner.config.EvictionTTL = d

	return runner
}

// PollInterval caps the delay between status polls of Result. Zero uses
// [DefaultPollInterval].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(hatchet.New(), registry, func(runner *RunnerBuilder) {
//		runner.PollInterval(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (runner *RunnerBuilder) PollInterval(d time.Duration) *RunnerBuilder {
	runner.config.PollInterval = d

	return runner
}

// LookupGrace is how long the driver waits for a run that Hatchet does
// not list yet, because listing lags behind starting. A negative value
// turns the wait off. Zero uses [DefaultLookupGrace]. Start verifies the
// returned physical ID before caching it; if this wait expires, it can
// return an error even though the trigger already started work. Cold
// lookups of unknown logical IDs list once rather than waiting.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(hatchet.New(), registry, func(runner *RunnerBuilder) {
//		runner.LookupGrace(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (runner *RunnerBuilder) LookupGrace(d time.Duration) *RunnerBuilder {
	runner.config.LookupGrace = d

	return runner
}

// Logger receives log records. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	registry, err := workflow.Handlers()
//	if err != nil {
//		panic(err)
//	}
//	component, err := NewRunner(hatchet.New(), registry, func(runner *RunnerBuilder) {
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

// DriverBuilderFunc edits client-side workflow settings before publication.
// Policies run synchronously in order; a nil entry panics. Use RunnerBuilderFunc
// when the process also owns workflow and step registration.
//
// Example:
//
//	policies := []DriverBuilderFunc{func(driver *DriverBuilder) {
//		driver.Prefix("orders")
//	}}
//	driver, err := NewDriver(hatchet.New(), policies...)
//	if err != nil {
//		return err
//	}
//	_ = driver
type DriverBuilderFunc = func(*DriverBuilder)

// RunnerBuilderFunc edits settings shared by a runner and its paired driver.
// Policies run once, not again for the driver. A nil entry panics; retained
// drafts cannot mutate completed settings.
//
// Example:
//
//	policies := []RunnerBuilderFunc{func(runner *RunnerBuilder) {
//		runner.Prefix("orders")
//	}}
//	registry, err := workflow.Handlers()
//	if err != nil {
//		return err
//	}
//	runner, err := NewRunner(hatchet.New(), registry, policies...)
//	if err != nil {
//		return err
//	}
//	_ = runner.Driver()
type RunnerBuilderFunc = func(*RunnerBuilder)
