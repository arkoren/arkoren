package hatchetschedule

import (
	"log/slog"
	"time"
)

// Default configuration values.
const (
	// DefaultPrefix is the workflow name prefix used when [Builder.Prefix] is empty.
	//
	// Example:
	//
	//	component, err := New(hatchet.New(), func(scheduler *Builder) {
	//		scheduler.Prefix(DefaultPrefix)
	//	})
	//	if err != nil {
	//		panic(err)
	//	}
	//	_ = component
	DefaultPrefix = "arkoren"

	// DefaultMaxRunTime is the execution timeout used when [Builder.MaxRunTime] is zero.
	//
	// Example:
	//
	//	component, err := New(hatchet.New(), func(scheduler *Builder) {
	//		scheduler.MaxRunTime(DefaultMaxRunTime)
	//	})
	//	if err != nil {
	//		panic(err)
	//	}
	//	_ = component
	DefaultMaxRunTime = time.Hour
)

// settings retains the optional construction policy for a [Driver].
// Construction publishes this private value after applying its role callbacks;
// live dependencies and SDK internals retain their existing ownership.
type settings struct {
	// Prefix names the Hatchet workflows and marks the crons this driver owns.
	// Empty uses [DefaultPrefix].
	Prefix string

	// MaxRunTime is the Hatchet execution timeout of every run. Zero uses
	// [DefaultMaxRunTime].
	MaxRunTime time.Duration

	// AllowCronTZ lets entries in a non-UTC location use a "CRON_TZ=<zone> "
	// expression prefix. The Hatchet engine supports it only according to a
	// reading of its code, not its documentation, so it is off by default.
	AllowCronTZ bool

	// Logger receives driver log records. Nil uses [slog.Default].
	Logger *slog.Logger
}

// Builder edits the construction settings of [New]. Callbacks share one
// fresh draft and run once in order; a nil callback panics. Retaining a draft
// does not permit changes to the published component. Dependencies and native
// SDK internals retain their existing ownership.
//
// Example:
//
//	component, err := New(hatchet.New(), func(scheduler *Builder) {
//		scheduler.Logger(slog.Default())
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
type Builder struct {
	// config holds this draft's optional settings until publication.
	config settings
}

// buildSettings applies construction callbacks directly and publishes a detached
// settings root. An empty list preserves defaults; nil entries panic.
func buildSettings(configure ...BuilderFunc) settings {
	config := settings{
		Prefix:     DefaultPrefix,
		MaxRunTime: DefaultMaxRunTime,
	}

	scheduler := &Builder{
		config: config,
	}

	for _, configure := range configure {
		configure(scheduler)
	}

	return scheduler.config
}

// Prefix names the Hatchet workflows and marks the crons this driver owns.
// Empty uses [DefaultPrefix].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(hatchet.New(), func(scheduler *Builder) {
//		scheduler.Prefix("example")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (scheduler *Builder) Prefix(prefix string) *Builder {
	scheduler.config.Prefix = prefix

	return scheduler
}

// MaxRunTime is the Hatchet execution timeout of every run. Zero uses
// [DefaultMaxRunTime]; New rejects negative values with hatchet.ErrInvalidConfig.
// This native backstop cannot force a Go callback that ignores cancellation to
// terminate. Entry timeouts remain owned by the schedule facade.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(hatchet.New(), func(scheduler *Builder) {
//		scheduler.MaxRunTime(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (scheduler *Builder) MaxRunTime(duration time.Duration) *Builder {
	scheduler.config.MaxRunTime = duration

	return scheduler
}

// AllowCronTZ lets entries in a non-UTC location use a "CRON_TZ=<zone> "
// expression prefix. The Hatchet engine supports it only according to a
// reading of its code, not its documentation, so it is off by default.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(hatchet.New(), func(scheduler *Builder) {
//		scheduler.AllowCronTZ(true)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (scheduler *Builder) AllowCronTZ(allow bool) *Builder {
	scheduler.config.AllowCronTZ = allow

	return scheduler
}

// Logger receives driver log records. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(hatchet.New(), func(scheduler *Builder) {
//		scheduler.Logger(slog.Default())
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (scheduler *Builder) Logger(logger *slog.Logger) *Builder {
	scheduler.config.Logger = logger

	return scheduler
}

// BuilderFunc edits schedule-driver settings before New registers native tasks.
// Policies run synchronously in order; a nil entry panics. Retaining a draft
// does not permit later mutation of the published driver.
//
// Example:
//
//	policies := []BuilderFunc{func(scheduler *Builder) {
//		scheduler.Prefix("orders")
//	}}
//	driver, err := New(hatchethost.New(), policies...)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)
