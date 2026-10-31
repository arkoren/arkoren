package temporalschedule

import (
	"log/slog"
	"time"
)

// settings retains the optional construction policy for a [Driver].
// Construction publishes this private value after applying its role callbacks;
// live dependencies and SDK internals retain their existing ownership.
type settings struct {
	// Prefix starts the name of every workflow, activity, schedule ID, and
	// deferred workflow ID. The default is "arkoren". Schedules whose ID starts
	// with Prefix and a hyphen are managed by the driver and may be deleted.
	Prefix string

	// MaxRunTime bounds one run when the entry has no timeout. The default is
	// one hour; zero and negative settings restore that default.
	MaxRunTime time.Duration

	// CatchupWindow is how far back Temporal fires missed runs after an
	// outage. The default is one minute; zero and negative settings restore it.
	CatchupWindow time.Duration

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
//	component, err := New(temporal.New(), func(scheduler *Builder) {
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
		Prefix:        "arkoren",
		MaxRunTime:    time.Hour,
		CatchupWindow: time.Minute,
	}
	scheduler := &Builder{
		config: config,
	}

	for _, configure := range configure {
		configure(scheduler)
	}

	return scheduler.config
}

// Prefix starts the name of every workflow, activity, schedule ID, and
// deferred workflow ID. The default is "arkoren". Schedules whose ID starts
// with Prefix and a hyphen are managed by the driver and may be deleted.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(temporal.New(), func(scheduler *Builder) {
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

// MaxRunTime bounds one run when the entry has no timeout. The default is
// one hour. Zero and negative values restore that default; they do not disable
// the activity bound. A positive entry timeout overrides this fallback.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(temporal.New(), func(scheduler *Builder) {
//		scheduler.MaxRunTime(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (scheduler *Builder) MaxRunTime(maxRunTime time.Duration) *Builder {
	scheduler.config.MaxRunTime = maxRunTime

	return scheduler
}

// CatchupWindow is how far back Temporal fires missed runs after an
// outage. The default is one minute. Zero and negative values restore that
// default; use an explicit positive duration to change missed-tick recovery.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(temporal.New(), func(scheduler *Builder) {
//		scheduler.CatchupWindow(time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = component
func (scheduler *Builder) CatchupWindow(catchupWindow time.Duration) *Builder {
	scheduler.config.CatchupWindow = catchupWindow

	return scheduler
}

// Logger receives driver log lines. Nil uses [slog.Default].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component, err := New(temporal.New(), func(scheduler *Builder) {
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

// BuilderFunc configures a fresh schedule-driver draft during New. Callbacks run
// directly once in order; nil entries panic. Published settings do not share the
// retained draft, while the supplied host and logger remain shared dependencies.
//
// Example:
//
//	policies := []BuilderFunc{
//		func(scheduler *Builder) {
//			scheduler.Prefix("orders")
//		},
//	}
//	driver, err := New(temporal.New(), policies...)
//	if err != nil {
//		return err
//	}
//	_ = schedule.New(driver)
type BuilderFunc = func(*Builder)
