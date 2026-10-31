// Package calendar compiles bounded cron calendars and resolves wall times.
package calendar

import (
	"time"
)

// searchYears bounds how far ahead a schedule looks for its next time.
const searchYears = 5

// Calendar is a parsed cron expression. Each field is a bitset with bit n
// set when value n matches.
type Calendar struct {
	// second, minute, hour, dom, month, and dow hold the matching values.
	second, minute, hour, dom, month, dow uint64

	// domStar and dowStar report that the field text started with "*".
	domStar, dowStar bool
}

// matchesDay reports whether the date of wall satisfies the day rules. When
// both day of month and day of week are restricted, either may match.
func (calendar Calendar) matchesDay(wall time.Time) bool {
	domMatch := calendar.dom&(1<<uint(wall.Day())) != 0
	dowMatch := calendar.dow&(1<<uint(wall.Weekday())) != 0

	if !calendar.domStar && !calendar.dowStar {
		return domMatch || dowMatch
	}

	return domMatch && dowMatch
}

// FirstWall returns the first matching wall-clock time at or after from, up to
// and including limit. Wall times are naive: they are carried in UTC values
// and carry no zone. It reports false when nothing matches by limit.
func (calendar Calendar) FirstWall(from, limit time.Time) (time.Time, bool) {
	wall := from.UTC()

	for !wall.After(limit) {
		year, month, day := wall.Date()
		hour, minute, second := wall.Clock()

		switch {
		case calendar.month&(1<<uint(month)) == 0:
			wall = time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
		case !calendar.matchesDay(wall):
			wall = time.Date(year, month, day+1, 0, 0, 0, 0, time.UTC)
		case calendar.hour&(1<<uint(hour)) == 0:
			wall = time.Date(year, month, day, hour+1, 0, 0, 0, time.UTC)
		case calendar.minute&(1<<uint(minute)) == 0:
			wall = time.Date(year, month, day, hour, minute+1, 0, 0, time.UTC)
		case calendar.second&(1<<uint(second)) == 0:
			wall = wall.Add(time.Second)
		default:
			return wall, true
		}
	}

	return time.Time{}, false
}

// Next returns the first matching instant strictly after the given moment in
// location, or the zero time when none falls within the search window.
func (calendar Calendar) Next(after time.Time, location *time.Location) time.Time {
	after = after.In(location)
	cursor := wallOf(after).Add(time.Second)
	limit := wallOf(after).AddDate(searchYears, 0, 0)

	for {
		wall, found := calendar.FirstWall(cursor, limit)

		if !found {
			return time.Time{}
		}

		cursor = wall.Add(time.Second)

		// Skip wall times inside a gap, and times already passed, which are
		// the repeated pass over an overlap.
		moment, exists := resolve(wall, location)

		if !exists || !moment.After(after) {
			continue
		}

		return moment
	}
}
