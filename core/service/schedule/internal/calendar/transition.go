package calendar

import (
	"time"
)

// wallOf returns the wall-clock reading of moment as a naive UTC value.
func wallOf(moment time.Time) time.Time {
	year, month, day := moment.Date()
	hour, minute, second := moment.Clock()

	return time.Date(year, month, day, hour, minute, second, 0, time.UTC)
}

// resolve converts a naive wall time to an instant in location. It reports
// false when the wall time does not exist because a transition skips it. A
// wall time that occurs twice resolves to its first occurrence.
func resolve(wall time.Time, location *time.Location) (time.Time, bool) {
	year, month, day := wall.Date()
	hour, minute, second := wall.Clock()

	moment := time.Date(year, month, day, hour, minute, second, 0, location)

	if !wallOf(moment).Equal(wall) {
		return time.Time{}, false
	}

	// An overlap resolves to the first occurrence: test the offset in force a
	// day earlier.
	_, earlier := moment.Add(-24 * time.Hour).Zone()
	candidate := wall.Add(-time.Duration(earlier) * time.Second)

	if candidate.Before(moment) && wallOf(candidate.In(location)).Equal(wall) {
		return candidate.In(location), true
	}

	return moment, true
}

// FindTransition returns the first second in (low, high] at which the zone
// offset differs from the offset at low.
func FindTransition(low, high time.Time) time.Time {
	_, base := low.Zone()

	for high.Sub(low) > time.Second {
		middle := low.Add(high.Sub(low) / 2)

		if _, offset := middle.Zone(); offset == base {
			low = middle
		} else {
			high = middle
		}
	}

	return high
}
