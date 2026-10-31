package schedule_test

import (
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
)

// checkSpecExpression fails the test unless spec is valid and renders as want.
func checkSpecExpression(t *testing.T, spec schedule.Spec, want string) {
	t.Helper()

	if err := spec.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := spec.Expression(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSpecExpressionHour checks that an hour-only declaration defaults the
// minute to zero and leaves the calendar fields unrestricted.
func TestSpecExpressionHour(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Hour(3), "0 3 * * *")
}

// TestSpecExpressionEveryMinutes checks that a whole-minute interval becomes
// a stepped minute field without adding a seconds field.
func TestSpecExpressionEveryMinutes(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Every(5*time.Minute), "*/5 * * * *")
}

// TestSpecDateOnlyDefaultsToMidnight characterizes date-only defaulting through
// the published expression and next instant, without inspecting compiled fields.
func TestSpecDateOnlyDefaultsToMidnight(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Day(1)

	if err := spec.Err(); err != nil {
		t.Fatal(err)
	}

	if got := spec.Expression(); got != "0 0 1 * *" {
		t.Fatalf("expression = %q, want midnight on the first", got)
	}

	after := time.Date(2026, time.January, 1, 12, 34, 30, 0, time.UTC)
	want := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)

	if got := spec.Next(after); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
}

// TestSpecExplicitZeroSecondKeepsCoarserFieldsWildcard distinguishes an
// explicit zero second from omitted time fields on a date-only declaration.
func TestSpecExplicitZeroSecondKeepsCoarserFieldsWildcard(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Day(1).Second(0)

	if err := spec.Err(); err != nil {
		t.Fatal(err)
	}

	if got := spec.Expression(); got != "* * 1 * *" {
		t.Fatalf("expression = %q, want every minute on the first", got)
	}

	if spec.HasSeconds() {
		t.Fatal("explicit zero seconds should not require a sixth field")
	}

	after := time.Date(2026, time.January, 1, 12, 34, 30, 0, time.UTC)
	want := time.Date(2026, time.January, 1, 12, 35, 0, 0, time.UTC)

	if got := spec.Next(after); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
}

// TestSpecExpressionEveryOneMinute checks that a one-minute interval renders
// as a wildcard rather than a redundant step of one.
func TestSpecExpressionEveryOneMinute(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Every(time.Minute), "* * * * *")
}

// TestSpecExpressionEverySeconds checks that sub-minute schedules include the
// seconds field required to represent their cadence.
func TestSpecExpressionEverySeconds(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Every(10*time.Second), "*/10 * * * * *")
}

// TestSpecExpressionEveryHours checks that a whole-hour interval fixes minutes
// at zero and steps the hour field.
func TestSpecExpressionEveryHours(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Every(6*time.Hour), "0 */6 * * *")
}

// TestSpecExpressionEveryOneHour checks the canonical hourly expression,
// with a zero minute and an unrestricted hour.
func TestSpecExpressionEveryOneHour(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Every(time.Hour), "0 * * * *")
}

// TestSpecExpressionFullTime checks that explicitly selected seconds, minutes,
// and hours retain their positions in a six-field expression.
func TestSpecExpressionFullTime(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Hour(13).Minute(42).Second(11), "11 42 13 * * *")
}

// TestSpecExpressionWeekdayMonday checks the cron weekday numbering and its
// composition with a specified hour and minute.
func TestSpecExpressionWeekdayMonday(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().On(time.Monday).Hour(8).Minute(30), "30 8 * * 1")
}

// TestSpecExpressionDayOfMonth checks that a monthly day restriction composes
// with an hour without constraining the weekday.
func TestSpecExpressionDayOfMonth(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Day(1).Hour(6), "0 6 1 * *")
}

// TestSpecExpressionMonthAndDay checks that an annual calendar declaration
// defaults to midnight on the selected month and day.
func TestSpecExpressionMonthAndDay(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Month(time.January).Day(1), "0 0 1 1 *")
}

// TestSpecExpressionWeekdays checks that the convenience selector expands to
// Monday through Friday at midnight.
func TestSpecExpressionWeekdays(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Weekdays(), "0 0 * * 1-5")
}

// TestSpecExpressionWeekends checks that Saturday and Sunday remain a set of
// cron weekdays when combined with a selected hour.
func TestSpecExpressionWeekends(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Weekends().Hour(10), "0 10 * * 0,6")
}

// TestSpecExpressionMinuteOnly checks that selecting a minute does not
// accidentally restrict which hours may run.
func TestSpecExpressionMinuteOnly(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Minute(30), "30 * * * *")
}

// TestSpecExpressionSecondOnly checks that selecting a second preserves all
// minutes and hours in the six-field expression.
func TestSpecExpressionSecondOnly(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Second(10), "10 * * * * *")
}

// TestSpecExpressionSecondZero checks that an explicit zero second does not
// require a redundant sixth field for an otherwise minute-level schedule.
func TestSpecExpressionSecondZero(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Hour(1).Second(0), "0 1 * * *")
}

// TestSpecExpressionWeekdaysEvery checks that an interval can restrict its
// executions to weekdays without changing its minute cadence.
func TestSpecExpressionWeekdaysEvery(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Weekdays().Every(15*time.Minute), "*/15 * * * 1-5")
}

// TestSpecExpressionHourWithEvery checks that a minute interval can be limited
// to one selected hour.
func TestSpecExpressionHourWithEvery(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Hour(9).Every(15*time.Minute), "*/15 9 * * *")
}

// TestSpecExpressionHourAndSecond checks that the omitted minute defaults to
// zero between an explicit hour and second.
func TestSpecExpressionHourAndSecond(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Hour(9).Second(5), "5 0 9 * * *")
}

// TestSpecExpressionSortedValues protects canonical output: selected values
// are sorted and duplicates appear only once.
func TestSpecExpressionSortedValues(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Hour(18, 6, 6, 12), "0 6,12,18 * * *")
}

// TestSpecExpressionMonths checks that typed month values render in ascending
// numeric order without changing their calendar meaning.
func TestSpecExpressionMonths(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Month(time.June, time.March), "0 0 * 3,6 *")
}

// TestSpecExpressionInZone checks that choosing a location does not insert
// zone syntax into the cron expression exposed to drivers.
func TestSpecExpressionInZone(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.In(time.UTC).Hour(1), "0 1 * * *")
}

// TestSpecExpressionCron checks that raw cron whitespace is normalized while
// preserving the caller's field expressions.
func TestSpecExpressionCron(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Cron("0  */6 * * *"), "0 */6 * * *")
}

// TestSpecExpressionCronSeconds checks that a supplied six-field expression
// retains its explicit seconds cadence.
func TestSpecExpressionCronSeconds(t *testing.T) {
	t.Parallel()

	checkSpecExpression(t, schedule.UTC().Cron("*/5 0 * * * *"), "*/5 0 * * * *")
}

// TestSpecHasSeconds distinguishes minute-level declarations from intervals
// and raw cron expressions that require seconds precision.
func TestSpecHasSeconds(t *testing.T) {
	t.Parallel()

	if schedule.UTC().Hour(3).HasSeconds() {
		t.Fatal("hour spec has no seconds")
	}

	if !schedule.UTC().Every(time.Second).HasSeconds() {
		t.Fatal("every second spec has seconds")
	}

	if !schedule.UTC().Cron("1 2 3 4 5 *").HasSeconds() {
		t.Fatal("six field cron has seconds")
	}
}

// checkSpecInvalid fails the test unless spec reports ErrInvalidEntry, renders
// an empty expression, and has no next run.
func checkSpecInvalid(t *testing.T, spec schedule.Spec) {
	t.Helper()

	err := spec.Err()

	if !errors.Is(err, schedule.ErrInvalidEntry) {
		t.Errorf("got %v, want ErrInvalidEntry", err)
	}

	if spec.Expression() != "" {
		t.Error("expression of invalid spec should be empty")
	}

	if !spec.Next(time.Now()).IsZero() {
		t.Error("next of invalid spec should be zero")
	}
}

// TestSpecInvalidZeroSpec checks that the zero value is not a usable schedule
// and exposes neither an expression nor a next execution.
func TestSpecInvalidZeroSpec(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.Spec{})
}

// TestSpecInvalidNilLocation requires a location error rather than an implicit
// fallback to UTC or the process-local zone.
func TestSpecInvalidNilLocation(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.In(nil))
}

// TestSpecInvalidEmptySpec checks that a location alone does not supply a
// calendar or interval declaration.
func TestSpecInvalidEmptySpec(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC())
}

// TestSpecInvalidSecondOutOfRange rejects a second outside the cron field's
// inclusive range instead of carrying it into the next minute.
func TestSpecInvalidSecondOutOfRange(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Second(60))
}

// TestSpecInvalidMinuteOutOfRange rejects negative minutes and checks that the
// resulting spec remains unusable.
func TestSpecInvalidMinuteOutOfRange(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Minute(-1))
}

// TestSpecInvalidHourOutOfRange rejects the first hour beyond a calendar day's
// valid range instead of wrapping it to midnight.
func TestSpecInvalidHourOutOfRange(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Hour(24))
}

// TestSpecInvalidDayZero checks the one-based day-of-month boundary rather
// than accepting zero as an alias for a neighboring date.
func TestSpecInvalidDayZero(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Day(0))
}

// TestSpecInvalidDayTooLarge rejects a day that cannot occur in any month,
// independently of the spec's other calendar restrictions.
func TestSpecInvalidDayTooLarge(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Day(32))
}

// TestSpecInvalidMonthOutOfRange checks that typed time.Month inputs still
// receive calendar-range validation.
func TestSpecInvalidMonthOutOfRange(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Month(time.Month(13)))
}

// TestSpecInvalidWeekdayOutOfRange checks that typed weekday inputs outside
// Sunday through Saturday are rejected.
func TestSpecInvalidWeekdayOutOfRange(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().On(time.Weekday(7)))
}

// TestSpecInvalidNoValues rejects an explicitly empty field selection rather
// than interpreting it as an unrestricted field.
func TestSpecInvalidNoValues(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Hour())
}

// TestSpecInvalidHourTwice checks that immutable chaining does not permit a
// second assignment to an already selected field.
func TestSpecInvalidHourTwice(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Hour(1).Hour(2))
}

// TestSpecInvalidEveryOnSetField rejects an interval that would overwrite the
// minute field already assigned by the caller.
func TestSpecInvalidEveryOnSetField(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Minute(5).Every(5*time.Minute))
}

// TestSpecInvalidFieldAfterEvery checks the reverse assignment order: a field
// selected by an interval cannot be set again explicitly.
func TestSpecInvalidFieldAfterEvery(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(5*time.Minute).Minute(5))
}

// TestSpecInvalidEveryTwice rejects competing interval declarations instead
// of silently replacing the earlier cadence.
func TestSpecInvalidEveryTwice(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(5*time.Minute).Every(time.Hour))
}

// TestSpecInvalidDayThenOn rejects combining the fluent day-of-month and
// weekday selectors, whose cron interaction would otherwise be ambiguous.
func TestSpecInvalidDayThenOn(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Day(1).On(time.Monday))
}

// TestSpecInvalidOnThenDay checks that conflicting calendar selectors are
// rejected regardless of their order in the fluent chain.
func TestSpecInvalidOnThenDay(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().On(time.Monday).Day(1))
}

// TestSpecInvalidDayThenWeekdays checks that the weekday convenience method
// obeys the same conflict rule as explicit weekday selection.
func TestSpecInvalidDayThenWeekdays(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Day(1).Weekdays())
}

// TestSpecInvalidWeekdaysTwice rejects repeated assignments through the
// convenience selector rather than treating them as harmless duplicates.
func TestSpecInvalidWeekdaysTwice(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Weekdays().Weekends())
}

// TestSpecInvalidEveryZero checks that zero does not define a cadence or
// implicitly select a default interval.
func TestSpecInvalidEveryZero(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(0))
}

// TestSpecInvalidEveryNegative rejects a backwards interval before the spec
// can be published as a usable schedule.
func TestSpecInvalidEveryNegative(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(-time.Minute))
}

// TestSpecInvalidEveryADay checks the interval API's upper boundary; daily
// schedules must be represented by calendar selections instead.
func TestSpecInvalidEveryADay(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(24*time.Hour))
}

// TestSpecInvalidEveryFractionalSecond rejects intervals that cannot be
// represented exactly by cron's whole-second precision.
func TestSpecInvalidEveryFractionalSecond(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(1500*time.Millisecond))
}

// TestSpecInvalidEveryUnevenSeconds rejects second steps that do not divide a
// minute evenly and would create an irregular boundary interval.
func TestSpecInvalidEveryUnevenSeconds(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(7*time.Second))
}

// TestSpecInvalidEveryUnevenMinutes rejects minute steps that do not divide an
// hour evenly and would create an irregular boundary interval.
func TestSpecInvalidEveryUnevenMinutes(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(8*time.Minute))
}

// TestSpecInvalidEveryUnevenHours rejects hour steps that do not divide a day
// evenly and would create an irregular midnight interval.
func TestSpecInvalidEveryUnevenHours(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(5*time.Hour))
}

// TestSpecInvalidEveryNotWholeMinutes rejects a multi-minute interval with a
// seconds remainder that a stepped minute field cannot encode.
func TestSpecInvalidEveryNotWholeMinutes(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(90*time.Second))
}

// TestSpecInvalidEveryNotWholeHours rejects a multi-hour interval with a
// minutes remainder that a stepped hour field cannot encode.
func TestSpecInvalidEveryNotWholeHours(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Every(90*time.Minute))
}

// TestSpecInvalidCronThenField prevents fluent field edits from changing a
// schedule already supplied as a complete raw cron expression.
func TestSpecInvalidCronThenField(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Cron("0 0 * * *").Hour(1))
}

// TestSpecInvalidFieldThenCron prevents a raw expression from replacing
// fields already selected through the fluent API.
func TestSpecInvalidFieldThenCron(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Hour(1).Cron("0 0 * * *"))
}

// TestSpecInvalidCronThenEvery checks that interval selection cannot be mixed
// with a previously supplied complete raw cron expression.
func TestSpecInvalidCronThenEvery(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Cron("0 0 * * *").Every(time.Hour))
}

// TestSpecInvalidCronTwice rejects replacement of a complete raw declaration
// so the first assignment remains the only accepted expression.
func TestSpecInvalidCronTwice(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Cron("0 0 * * *").Cron("0 1 * * *"))
}

// TestSpecInvalidInvalidCron checks that malformed raw text produces the same
// unusable-spec behavior as invalid fluent declarations.
func TestSpecInvalidInvalidCron(t *testing.T) {
	t.Parallel()

	checkSpecInvalid(t, schedule.UTC().Cron("nope"))
}

// TestSpecKeepsFirstError protects stable diagnostics when more than one
// chained field contains an invalid value.
func TestSpecKeepsFirstError(t *testing.T) {
	t.Parallel()

	spec := schedule.UTC().Hour(99).Minute(99)

	if got := spec.Err().Error(); got != "invalid schedule entry: hour value 99 outside 0-23" {
		t.Fatalf("got %q", got)
	}
}

// TestSpecIsImmutable checks that discarding a transformed copy cannot mutate
// the original reusable calendar declaration.
func TestSpecIsImmutable(t *testing.T) {
	t.Parallel()

	base := schedule.UTC().Hour(3)
	_ = base.Minute(30)

	if got := base.Expression(); got != "0 3 * * *" {
		t.Fatalf("base changed to %q", got)
	}
}

// TestSpecLocation checks explicit, UTC, and zero-value locations without
// assuming a process-local default.
func TestSpecLocation(t *testing.T) {
	t.Parallel()

	amsterdam := loadZone(t, "Europe/Amsterdam")

	if schedule.In(amsterdam).Location() != amsterdam {
		t.Fatal("location not kept")
	}

	if schedule.UTC().Location() != time.UTC {
		t.Fatal("UTC location not kept")
	}

	if (schedule.Spec{}).Location() != nil {
		t.Fatal("zero spec has a location")
	}
}

// TestSpecCronParseErrors checks raw-expression acceptance boundaries; every
// invalid input must match ErrInvalidEntry and every valid input must compile.
func TestSpecCronParseErrors(t *testing.T) {
	t.Parallel()

	invalid := []string{
		"",
		"* * * *",
		"* * * * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * 32 * *",
		"* * * 0 *",
		"* * * 13 *",
		"* * * * 7",
		"a * * * *",
		"*/0 * * * *",
		"*/x * * * *",
		"*/ * * * *",
		"5/2 * * * *",
		"5-1 * * * *",
		"1-2-3 * * * *",
		"1,,2 * * * *",
		"JAN * * * *",
		"@daily",
	}

	for _, expression := range invalid {
		if err := schedule.UTC().Cron(expression).Err(); !errors.Is(err, schedule.ErrInvalidEntry) {
			t.Errorf("%q: got %v, want ErrInvalidEntry", expression, err)
		}
	}

	valid := []string{
		"* * * * *",
		"0 */6 * * *",
		"1-5/2,10 * * * *",
		"0-30/10 * * * * *",
		"0 0 1,15 * 1-5",
		"59 23 31 12 6",
	}

	for _, expression := range valid {
		if err := schedule.UTC().Cron(expression).Err(); err != nil {
			t.Errorf("%q: %v", expression, err)
		}
	}
}

// loadZone loads a named zone for a test.
func loadZone(t *testing.T, name string) *time.Location {
	t.Helper()

	location, err := time.LoadLocation(name)

	if err != nil {
		t.Skipf("zone %s unavailable: %v", name, err)
	}

	return location
}
