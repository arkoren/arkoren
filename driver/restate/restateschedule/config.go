package restateschedule

import (
	"log/slog"
	"time"
)

// settings retains the optional construction policy for a [Driver].
// Construction publishes this private value after applying its role callbacks;
// live dependencies and SDK internals retain their existing ownership.
type settings struct {
	// Prefix names the Restate services: "arkoren" gives ArkorenSchedule,
	// ArkorenScheduleIndex, and ArkorenRunner. Dots, dashes, and other
	// separators are dropped and the next letter is upper-cased. The default
	// is "arkoren".
	Prefix string

	// MaxRunTime bounds one run, as the abort and inactivity timeout of the
	// Tick and Run handlers. Zero uses one hour; a negative value keeps the
	// Restate server defaults.
	MaxRunTime time.Duration

	// Logger receives driver log lines. Nil uses [slog.Default].
	Logger *slog.Logger
}

// Builder edits the construction settings of [New]. Callbacks share one
// fresh draft and run once in order; a nil callback panics. Retaining a draft
// does not permit changes to the published component. Dependencies and native
// SDK internals retain their existing ownership.
//
// Example:
//
//	component, err := New(restate.New(), func(scheduler *Builder) {
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
		Prefix:     "arkoren",
		MaxRunTime: time.Hour,
	}
	scheduler := &Builder{
		config: config,
	}

	for _, configure := range configure {
		configure(scheduler)
	}

	return scheduler.config
}

// Prefix names the Restate services: "arkoren" gives ArkorenSchedule,
// ArkorenScheduleIndex, and ArkorenRunner. Dots, dashes, and other
// separators are dropped and the next letter is upper-cased. The default
// is "arkoren".
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(restate.New(), func(scheduler *Builder) {
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

// MaxRunTime bounds one run, as the abort and inactivity timeout of the
// Tick and Run handlers. Zero uses one hour; a negative value keeps the
// Restate server defaults.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(restate.New(), func(scheduler *Builder) {
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

// Logger receives driver log lines. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(restate.New(), func(scheduler *Builder) {
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

// BuilderFunc is an ordinary callback alias for scheduler construction. New
// invokes callbacks once in order after validating its required host. Nil
// entries panic; retained drafts cannot mutate the published settings.
//
// Example:
//
//	policies := []BuilderFunc{func(scheduler *Builder) {
//		scheduler.Prefix("orders")
//	}}
//	_, err := New(restate.New(), policies...)
//	if err != nil {
//		panic(err)
//	}
type BuilderFunc = func(*Builder)
