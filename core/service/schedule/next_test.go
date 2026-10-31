package schedule_test

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
)

// ExampleSpec_Next computes a deterministic calendar instant without timers.
// Next is strictly after the supplied time and does not start execution.
func ExampleSpec_Next() {
	spec := schedule.UTC().Hour(3)

	if err := spec.Err(); err != nil {
		fmt.Println(err)

		return
	}

	after := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	fmt.Println(spec.Next(after).Format(time.RFC3339))
	// Output: 2026-01-01T03:00:00Z
}

// TestSpecNextLargeStepStaysInsideRange verifies that a step larger than the
// calendar range selects only its first value, without integer wraparound.
func TestSpecNextLargeStepStaysInsideRange(t *testing.T) {
	t.Parallel()

	step := strconv.Itoa(int(^uint(0) >> 1))
	spec := schedule.UTC().Cron("0 0 31-31/" + step + " * *")

	if err := spec.Err(); err != nil {
		t.Fatal(err)
	}

	after := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC)

	if got := spec.Next(after); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
}

// utc builds fixed whole-second instants for calendar assertions. Using UTC
// keeps these cases independent of the machine's local time zone.
func utc(year int, month time.Month, day, hour, minute, second int) time.Time {
	return time.Date(year, month, day, hour, minute, second, 0, time.UTC)
}

// checkSpecNext fails the test unless spec.Next(after) equals want.
func checkSpecNext(t *testing.T, spec schedule.Spec, after, want time.Time) {
	t.Helper()

	if got := spec.Next(after); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestSpecNextStep checks that minute intervals advance to the next aligned
// boundary rather than adding a duration to the supplied instant.
func TestSpecNextStep(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Every(5*time.Minute), utc(2025, 6, 1, 10, 2, 0), utc(2025, 6, 1, 10, 5, 0))
}

// TestSpecNextStepStrictlyAfter protects the exclusive lower bound: an instant
// already on a matching boundary must advance to the following match.
func TestSpecNextStepStrictlyAfter(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Every(5*time.Minute), utc(2025, 6, 1, 10, 5, 0), utc(2025, 6, 1, 10, 10, 0))
}

// TestSpecNextStepCrossesHour checks that stepping past the final minute
// boundary carries into the next hour.
func TestSpecNextStepCrossesHour(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Every(15*time.Minute), utc(2025, 6, 1, 10, 50, 0), utc(2025, 6, 1, 11, 0, 0))
}

// TestSpecNextSecondsStep checks that a seconds cadence crosses minute
// boundaries without losing its alignment.
func TestSpecNextSecondsStep(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Every(10*time.Second), utc(2025, 6, 1, 10, 0, 55), utc(2025, 6, 1, 10, 1, 0))
}

// TestSpecNextHourlyStep checks that hourly intervals select the next aligned
// hour, with minutes and seconds reset to zero.
func TestSpecNextHourlyStep(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Every(6*time.Hour), utc(2025, 6, 1, 7, 0, 0), utc(2025, 6, 1, 12, 0, 0))
}

// TestSpecNextNightly checks that an exact daily match advances to the next
// calendar day rather than returning the starting instant.
func TestSpecNextNightly(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Hour(3), utc(2025, 6, 1, 3, 0, 0), utc(2025, 6, 2, 3, 0, 0))
}

// TestSpecNextNightlyBefore checks that an upcoming daily match can still run
// on the same calendar day.
func TestSpecNextNightlyBefore(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Hour(3), utc(2025, 6, 1, 2, 59, 59), utc(2025, 6, 1, 3, 0, 0))
}

// TestSpecNextNanoseconds checks that a fractional instant after a daily match
// cannot round backwards to that match.
func TestSpecNextNanoseconds(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Hour(3), time.Date(2025, 6, 1, 3, 0, 0, 500, time.UTC), utc(2025, 6, 2, 3, 0, 0))
}

// TestSpecNextFullTime checks matching when hour, minute, and second are all
// explicitly selected by the calendar declaration.
func TestSpecNextFullTime(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Hour(13).Minute(42).Second(11), utc(2025, 6, 1, 0, 0, 0), utc(2025, 6, 1, 13, 42, 11))
}

// TestSpecNextWeekdaysSkipWeekend checks that the next weekday after Friday's
// matching time is Monday, not Saturday.
func TestSpecNextWeekdaysSkipWeekend(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Weekdays().Hour(9), utc(2025, 6, 6, 9, 0, 0), utc(2025, 6, 9, 9, 0, 0))
}

// TestSpecNextWeekends checks that the weekend selector advances from Friday
// to Saturday's default midnight execution.
func TestSpecNextWeekends(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Weekends(), utc(2025, 6, 6, 9, 0, 0), utc(2025, 6, 7, 0, 0, 0))
}

// TestSpecNextMonday checks that a weekday restriction preserves the selected
// hour and minute when advancing from Sunday.
func TestSpecNextMonday(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().On(time.Monday).Hour(8).Minute(30), utc(2025, 6, 1, 0, 0, 0), utc(2025, 6, 2, 8, 30, 0))
}

// TestSpecNextDayOrWeekday checks raw cron's OR rule for restricted month-day
// and weekday fields: the earlier matching weekday wins.
func TestSpecNextDayOrWeekday(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Cron("0 0 13 * 5"), utc(2025, 6, 1, 0, 0, 0), utc(2025, 6, 6, 0, 0, 0))
}

// TestSpecNextDayOrWeekdayOnDay checks that the day-of-month branch can select
// the next match under raw cron's OR rule.
func TestSpecNextDayOrWeekdayOnDay(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Cron("0 0 13 * 5"), utc(2025, 6, 6, 0, 0, 0), utc(2025, 6, 13, 0, 0, 0))
}

// TestSpecNextDayAndWeekday checks the AND rule when a stepped wildcard
// day-of-month field is combined with a weekday restriction.
func TestSpecNextDayAndWeekday(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Cron("0 0 */2 * 1"), utc(2025, 6, 1, 0, 0, 0), utc(2025, 6, 9, 0, 0, 0))
}

// TestSpecNextLeapDay checks that an annual leap-day declaration skips years
// in which February has only 28 days.
func TestSpecNextLeapDay(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Month(time.February).Day(29), utc(2025, 1, 1, 0, 0, 0), utc(2028, 2, 29, 0, 0, 0))
}

// TestSpecNextLeapDayFromLeapYear checks exclusive matching across a full
// four-year leap cycle when the starting instant is itself February 29.
func TestSpecNextLeapDayFromLeapYear(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Month(time.February).Day(29), utc(2024, 2, 29, 0, 0, 0), utc(2028, 2, 29, 0, 0, 0))
}

// TestSpecNextMonthEnd checks that day 31 skips a month in which that date
// does not exist rather than normalizing it into another month.
func TestSpecNextMonthEnd(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Day(31), utc(2025, 4, 15, 0, 0, 0), utc(2025, 5, 31, 0, 0, 0))
}

// TestSpecNextMonthEndSkipsShortMonth checks that advancing from a matching
// day 31 bypasses the next short month.
func TestSpecNextMonthEndSkipsShortMonth(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Day(31), utc(2025, 5, 31, 0, 0, 0), utc(2025, 7, 31, 0, 0, 0))
}

// TestSpecNextYearEnd checks that a daily midnight declaration carries the
// year correctly after December 31.
func TestSpecNextYearEnd(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Hour(0), utc(2025, 12, 31, 0, 0, 0), utc(2026, 1, 1, 0, 0, 0))
}

// TestSpecNextJanuaryFirst checks that an annual match advances one year when
// the lower bound is already the selected midnight.
func TestSpecNextJanuaryFirst(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Month(time.January).Day(1), utc(2025, 1, 1, 0, 0, 0), utc(2026, 1, 1, 0, 0, 0))
}

// TestSpecNextSixFieldCron checks that raw seconds precision participates in
// next-run matching rather than being ignored.
func TestSpecNextSixFieldCron(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Cron("30 0 12 * * *"), utc(2025, 6, 1, 0, 0, 0), utc(2025, 6, 1, 12, 0, 30))
}

// TestSpecNextInputInOtherZone checks that the lower bound is interpreted as
// an instant even when its displayed location differs from the spec's.
func TestSpecNextInputInOtherZone(t *testing.T) {
	t.Parallel()

	checkSpecNext(t, schedule.UTC().Hour(3), time.Date(2025, 6, 1, 5, 0, 0, 0, time.FixedZone("x", 3600*5)), utc(2025, 6, 1, 3, 0, 0))
}

// TestSpecNextBeyondFiveYears checks that a calendar date with no possible
// match returns zero instead of searching indefinitely.
func TestSpecNextBeyondFiveYears(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Cron("0 0 30 2 *")

	if got := spec.Next(utc(2025, 1, 1, 0, 0, 0)); !got.IsZero() {
		t.Fatalf("got %v, want zero", got)
	}
}

// TestSpecNextResultInSpecLocation checks that the returned instant carries
// the spec's location and the selected local wall-clock hour.
func TestSpecNextResultInSpecLocation(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	got := schedule.In(amsterdam).Hour(9).Next(utc(2025, 6, 1, 0, 0, 0))

	if got.Location() != amsterdam || got.Hour() != 9 {
		t.Fatalf("got %v", got)
	}
}

// dateIn returns the given 2025 date and time in location.
func dateIn(location *time.Location, month time.Month, day, hour, minute, second int) time.Time {
	return time.Date(2025, month, day, hour, minute, second, 0, location)
}

// checkDaylightSaving fails the test unless spec.Next(after) equals want and
// is strictly after after.
func checkDaylightSaving(t *testing.T, spec schedule.Spec, after, want time.Time) {
	t.Helper()

	got := spec.Next(after)

	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want.UTC())
	}

	if !got.After(after) {
		t.Errorf("%v is not after %v", got, after)
	}
}

// TestSpecNextDaylightSavingAmsterdamGapIsSkipped checks that a nonexistent
// local time is omitted, not moved across the spring-forward gap.
func TestSpecNextDaylightSavingAmsterdamGapIsSkipped(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSaving(t,
		schedule.In(amsterdam).Hour(2).Minute(30),
		dateIn(amsterdam, time.March, 29, 12, 0, 0),
		dateIn(amsterdam, time.March, 31, 2, 30, 0))
}

// TestSpecNextDaylightSavingAmsterdamGapBoundaryExists checks that the first
// valid wall time after the spring transition remains eligible that day.
func TestSpecNextDaylightSavingAmsterdamGapBoundaryExists(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSaving(t,
		schedule.In(amsterdam).Hour(3),
		dateIn(amsterdam, time.March, 29, 12, 0, 0),
		dateIn(amsterdam, time.March, 30, 3, 0, 0))
}

// TestSpecNextDaylightSavingAmsterdamGapWithMinuteStep checks that a stepped
// schedule resumes at the first valid aligned time after the spring gap.
func TestSpecNextDaylightSavingAmsterdamGapWithMinuteStep(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSaving(t,
		schedule.In(amsterdam).Every(30*time.Minute),
		dateIn(amsterdam, time.March, 30, 1, 30, 0),
		dateIn(amsterdam, time.March, 30, 3, 0, 0))
}

// TestSpecNextDaylightSavingAmsterdamOverlapRunsAtFirstOccurrence checks that
// a repeated autumn wall time resolves to its earlier real instant.
func TestSpecNextDaylightSavingAmsterdamOverlapRunsAtFirstOccurrence(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSaving(t,
		schedule.In(amsterdam).Hour(2).Minute(30),
		dateIn(amsterdam, time.October, 25, 12, 0, 0),
		time.Date(2025, time.October, 26, 0, 30, 0, 0, time.UTC))
}

// TestSpecNextDaylightSavingAmsterdamOverlapRunsOnceAfterFirst checks that an
// exact match at the first occurrence advances to the next calendar day.
func TestSpecNextDaylightSavingAmsterdamOverlapRunsOnceAfterFirst(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSaving(t,
		schedule.In(amsterdam).Hour(2).Minute(30),
		time.Date(2025, time.October, 26, 0, 30, 0, 0, time.UTC),
		dateIn(amsterdam, time.October, 27, 2, 30, 0))
}

// TestSpecNextDaylightSavingAmsterdamOverlapNotAgainAtSecondOccurrence checks
// that searching inside the repeated hour does not run a daily entry twice.
func TestSpecNextDaylightSavingAmsterdamOverlapNotAgainAtSecondOccurrence(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSaving(t,
		schedule.In(amsterdam).Hour(2).Minute(30),
		time.Date(2025, time.October, 26, 1, 0, 0, 0, time.UTC),
		dateIn(amsterdam, time.October, 27, 2, 30, 0))
}

// TestSpecNextDaylightSavingAmsterdamOverlapStepSkipsRepeatedHour checks that
// stepped minutes resume after the repeated local hour instead of replaying it.
func TestSpecNextDaylightSavingAmsterdamOverlapStepSkipsRepeatedHour(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSaving(t,
		schedule.In(amsterdam).Every(15*time.Minute),
		time.Date(2025, time.October, 26, 0, 45, 0, 0, time.UTC),
		time.Date(2025, time.October, 26, 2, 0, 0, 0, time.UTC))
}

// TestSpecNextDaylightSavingAmsterdamOverlapSecondsStepSkipsRepeatedHour checks
// that seconds-level schedules also avoid the second autumn occurrence.
func TestSpecNextDaylightSavingAmsterdamOverlapSecondsStepSkipsRepeatedHour(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	checkDaylightSaving(t,
		schedule.In(amsterdam).Every(10*time.Second),
		time.Date(2025, time.October, 26, 1, 30, 0, 0, time.UTC),
		time.Date(2025, time.October, 26, 2, 0, 0, 0, time.UTC))
}

// TestSpecNextDaylightSavingNewYorkGapIsSkipped exercises nonexistent local
// time handling in a zone whose spring transition differs from Amsterdam's.
func TestSpecNextDaylightSavingNewYorkGapIsSkipped(t *testing.T) {
	t.Parallel()

	newYork := loadZone(t, "America/New_York")

	checkDaylightSaving(t,
		schedule.In(newYork).Hour(2).Minute(30),
		dateIn(newYork, time.March, 8, 12, 0, 0),
		dateIn(newYork, time.March, 10, 2, 30, 0))
}

// TestSpecNextDaylightSavingNewYorkOverlapRunsAtFirstOccurrence checks that
// the earlier instant wins for New York's repeated autumn wall time.
func TestSpecNextDaylightSavingNewYorkOverlapRunsAtFirstOccurrence(t *testing.T) {
	t.Parallel()

	newYork := loadZone(t, "America/New_York")

	checkDaylightSaving(t,
		schedule.In(newYork).Hour(1).Minute(30),
		dateIn(newYork, time.November, 1, 12, 0, 0),
		time.Date(2025, time.November, 2, 5, 30, 0, 0, time.UTC))
}

// TestSpecNextDaylightSavingNewYorkOverlapRunsOnce checks that searching after
// the first match skips the repeated occurrence in New York too.
func TestSpecNextDaylightSavingNewYorkOverlapRunsOnce(t *testing.T) {
	t.Parallel()

	newYork := loadZone(t, "America/New_York")

	checkDaylightSaving(t,
		schedule.In(newYork).Hour(1).Minute(30),
		time.Date(2025, time.November, 2, 5, 30, 0, 0, time.UTC),
		dateIn(newYork, time.November, 3, 1, 30, 0))
}

// TestSpecNextDaylightSavingNewYorkOverlapSecondOccurrence checks that a
// lower bound inside the repeated hour skips to the next day's daily match.
func TestSpecNextDaylightSavingNewYorkOverlapSecondOccurrence(t *testing.T) {
	t.Parallel()

	newYork := loadZone(t, "America/New_York")

	checkDaylightSaving(t,
		schedule.In(newYork).Hour(1).Minute(30),
		time.Date(2025, time.November, 2, 6, 15, 0, 0, time.UTC),
		dateIn(newYork, time.November, 3, 1, 30, 0))
}

// TestSpecNextAlwaysAdvances checks a sequence across the autumn transition;
// every returned instant must be strictly later than its predecessor.
func TestSpecNextAlwaysAdvances(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")
	spec := schedule.In(amsterdam).Every(15 * time.Minute)

	moment := time.Date(2025, time.October, 25, 22, 0, 0, 0, amsterdam)

	for range 40 {
		next := spec.Next(moment)

		if !next.After(moment) {
			t.Fatalf("%v is not after %v", next, moment)
		}

		moment = next
	}
}
