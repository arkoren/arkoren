package schedule

import (
	"errors"

	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
)

// telemetryScope is the instrumentation scope of schedule telemetry.
const telemetryScope = "arkoren.dev/core/service/schedule"

// errPanic marks the error that [protect] builds from a recovered panic, so
// telemetry can report error.type "schedule.panic" instead of a generic Go
// type. Its text is the "panic" prefix of the error message.
var errPanic = errors.New("panic")

// runNames describes the instruments of scheduled runs. Runs last from
// milliseconds to many minutes, so the buckets span one millisecond to an
// hour rather than cache's sub-millisecond range.
var runNames = clientop.Names{
	Scope:    telemetryScope,
	Noun:     "scheduled runs",
	Count:    semconv.ScheduleRunCount,
	Duration: semconv.ScheduleRunDuration,
	Keys:     []string{semconv.ScheduleEntry, semconv.ScheduleKind, semconv.ScheduleOutcome},
	Buckets:  []float64{0.001, 0.01, 0.1, 1, 10, 60, 300, 900, 3600},
}

// errorSentinels name the package errors reported as error.type.
var errorSentinels = []errtype.Sentinel{
	{
		Err:  errPanic,
		Name: "schedule.panic",
	},
}
