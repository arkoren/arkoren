package feature

import (
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// BuilderFunc configures a Builder.
// Example:
//
//	var policy feature.BuilderFunc = func(flags *feature.Builder) {
//		flags.Name("memory")
//	}
//	_ = feature.New(driver, policy)
type BuilderFunc = func(*Builder)

// DefaultName is the provider name reported in telemetry when [Builder.Name]
// is empty.
//
// Example:
//
//	name := feature.DefaultName
//	feature.New(driver, func(flags *feature.Builder) {
//		flags.Name(name)
//	})
const DefaultName = "default"

// settings configures [Flags].
type settings struct {
	// Telemetry receives the spans and metrics of every evaluation. Nil uses
	// [telemetry.Default], resolved each time an evaluation runs, so calling
	// [telemetry.SetDefault] after [New] still takes effect.
	Telemetry *telemetry.Telemetry

	// Name identifies the provider in the feature_flag.provider.name
	// telemetry attribute, which tells apart several [Flags] in one process.
	// Choose a short, fixed value such as "launchdarkly". Empty reports
	// [DefaultName].
	Name string

	// Events receives [EvaluationFailed] when the driver fails an
	// evaluation. The default, nil, publishes nothing.
	Events *event.Bus
}

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	feature.New(driver, func(flags *feature.Builder) {
//		flags.Name("primary")
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...BuilderFunc) settings {
	flags := &Builder{}

	for _, configure := range configure {
		configure(flags)
	}

	return flags.settings
}

// Telemetry sets the telemetry that records evaluations. Without it the
// facade follows [telemetry.Default].
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	feature.New(driver, func(flags *feature.Builder) {
//		flags.Telemetry(nil)
//	})
func (flags *Builder) Telemetry(t *telemetry.Telemetry) *Builder {
	flags.settings.Telemetry = t

	return flags
}

// Name sets the provider name reported as feature_flag.provider.name.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	feature.New(driver, func(flags *feature.Builder) {
//		flags.Name("primary")
//	})
func (flags *Builder) Name(name string) *Builder {
	flags.settings.Name = name

	return flags
}

// Events selects the shared bus receiving EvaluationFailed after a driver
// failure. Nil publishes nothing; listeners cannot change the evaluated result.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	feature.New(driver, func(flags *feature.Builder) {
//		flags.Events(nil)
//	})
func (flags *Builder) Events(bus *event.Bus) *Builder {
	flags.settings.Events = bus

	return flags
}
