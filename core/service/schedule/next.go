package schedule

import (
	"time"

	"arkoren.dev/core/service/schedule/internal/calendar"
)

// Next returns the first matching time strictly after the given time. It
// returns the zero time when the spec is invalid or nothing matches within
// five years.
// It skips nonexistent wall times and chooses the first occurrence of an
// overlap. This computes timing only; it does not start work or mutate Spec.
//
//	after := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
//	spec := schedule.UTC().Hour(3)
//	fmt.Println(spec.Next(after).IsZero()) // false
func (spec Spec) Next(after time.Time) time.Time {
	schedule, err := spec.schedule()

	if err != nil {
		return time.Time{}
	}

	return schedule.Next(after, spec.location)
}

// schedule parses the compiled expression.
func (spec Spec) schedule() (calendar.Calendar, error) {
	expression, err := spec.compile()

	if err != nil {
		return calendar.Calendar{}, err
	}

	return calendar.Parse(expression)
}
