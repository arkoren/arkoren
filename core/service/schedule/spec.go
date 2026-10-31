package schedule

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"arkoren.dev/core/service/schedule/internal/calendar"
)

// Spec describes when an [Entry] runs. It is an immutable value: every method
// returns a modified copy, and the first error is kept and reported by
// [Spec.Err]. Start one with [UTC] or [In]; the zero Spec has no time zone
// and is invalid.
//
// Time fields (second, minute, hour) that are not set default to 0 when they
// are finer than the coarsest field that is set, and to every value when they
// are coarser. A spec with date fields but no time fields runs at midnight;
// a zone alone, such as UTC(), is invalid because it declares no schedule.
// Entry publication preserves these immutable Spec rules.
//
// Example:
//
//	spec := schedule.UTC().Day(1)
//	if err := spec.Err(); err != nil {
//		return err
//	}
//	fmt.Println(spec.Expression()) // 0 0 1 * *
type Spec struct {
	// location is the time zone the fields are read in.
	location *time.Location

	// second, minute, hour, day, month, and weekday hold the compiled text of
	// each field. An empty string means the field is not set.
	second, minute, hour, day, month, weekday string

	// every records that [Spec.Every] was used.
	every bool

	// cron holds a raw cron expression set with [Spec.Cron].
	cron string

	// err is the first error found while building the spec.
	err error
}

// UTC starts a spec that reads its fields in UTC.
// A zone alone is not a schedule; add a calendar field before registration.
//
//	spec := schedule.UTC().Hour(3)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func UTC() Spec {
	return Spec{
		location: time.UTC,
	}
}

// In starts a spec that reads its fields in location. A nil location is an
// error. Zones with daylight saving time skip a time inside the spring gap
// and run once inside the autumn overlap; see the package documentation.
// The location remains shared, and transformations return independent Specs.
//
//	location, err := time.LoadLocation("Europe/Amsterdam")
//	if err != nil {
//		return err
//	}
//	spec := schedule.In(location).Weekdays().Hour(9)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func In(location *time.Location) Spec {
	if location == nil {
		return Spec{
			err: fmt.Errorf("%w: nil location", ErrInvalidEntry),
		}
	}

	return Spec{
		location: location,
	}
}

// fail records the first error of spec.
func fail(spec *Spec, format string, args ...any) {
	if spec.err != nil {
		return
	}

	spec.err = fmt.Errorf("%w: %s", ErrInvalidEntry, fmt.Sprintf(format, args...))
}

// fieldsSet reports whether any field method has been used.
func (spec Spec) fieldsSet() bool {
	return spec.every || spec.second != "" || spec.minute != "" || spec.hour != "" ||
		spec.day != "" || spec.month != "" || spec.weekday != ""
}

// assign stores text in target after checking the spec accepts another field.
func assign(spec *Spec, target *string, name, text string) {
	switch {
	case spec.err != nil:
	case spec.cron != "":
		fail(spec, "%s cannot be combined with cron", name)
	case *target != "":
		fail(spec, "%s is set twice", name)
	default:
		*target = text
	}
}

// assignValues validates values against bounds and stores them sorted and
// without duplicates.
func assignValues(spec *Spec, target *string, bounds calendar.Bounds, values []int) {
	if spec.err != nil {
		return
	}

	if len(values) == 0 {
		fail(spec, "%s needs at least one value", bounds.Name)

		return
	}

	for _, value := range values {
		if value < bounds.Low || value > bounds.High {
			fail(spec, "%s value %d outside %d-%d", bounds.Name, value, bounds.Low, bounds.High)

			return
		}
	}

	sorted := slices.Compact(slices.Sorted(slices.Values(values)))
	texts := make([]string, len(sorted))

	for index, value := range sorted {
		texts[index] = strconv.Itoa(value)
	}

	assign(spec, target, bounds.Name, strings.Join(texts, ","))
}

// Second sets the seconds, 0-59, at which the spec matches.
// Values are sorted and deduplicated without retaining the input slice.
// Empty input, out-of-range values, or setting the field twice make Err non-nil.
//
//	spec := schedule.UTC().Second(0, 30)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) Second(values ...int) Spec {
	assignValues(&spec, &spec.second, calendar.Seconds, values)

	return spec
}

// Minute sets the minutes, 0-59, at which the spec matches.
// Empty input, out-of-range values, or a duplicate setting retain an error.
// Finer unset time fields default to zero.
//
//	spec := schedule.UTC().Minute(15, 45)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) Minute(values ...int) Spec {
	assignValues(&spec, &spec.minute, calendar.Minutes, values)

	return spec
}

// Hour sets the hours, 0-23, at which the spec matches.
// Values are copied into immutable text; empty or invalid input retains an
// error. Unset minutes and seconds default to zero.
//
//	spec := schedule.UTC().Hour(3, 15)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) Hour(values ...int) Spec {
	assignValues(&spec, &spec.hour, calendar.Hours, values)

	return spec
}

// Day sets the days of the month, 1-31, on which the spec matches. It cannot
// be combined with [Spec.On], [Spec.Weekdays], or [Spec.Weekends].
// Impossible dates are skipped rather than normalized into another month.
//
//	spec := schedule.UTC().Day(31)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) Day(values ...int) Spec {
	if spec.err == nil && spec.weekday != "" {
		fail(&spec, "day cannot be combined with weekdays")

		return spec
	}

	assignValues(&spec, &spec.day, calendar.Days, values)

	return spec
}

// Month sets the months in which the spec matches.
// Only January through December are valid; an empty list retains an error.
// With no time fields, the schedule matches at midnight.
//
//	spec := schedule.UTC().Month(time.January, time.July).Day(1)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) Month(values ...time.Month) Spec {
	numbers := make([]int, len(values))

	for index, value := range values {
		numbers[index] = int(value)
	}

	assignValues(&spec, &spec.month, calendar.Months, numbers)

	return spec
}

// On sets the days of the week on which the spec matches. It cannot be
// combined with [Spec.Day].
// An empty list or values outside Sunday through Saturday retain an error.
//
//	spec := schedule.UTC().On(time.Monday, time.Thursday).Hour(9)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) On(days ...time.Weekday) Spec {
	numbers := make([]int, len(days))

	for index, day := range days {
		numbers[index] = int(day)
	}

	if spec.err == nil && spec.day != "" {
		fail(&spec, "weekdays cannot be combined with day")

		return spec
	}

	assignValues(&spec, &spec.weekday, calendar.Weekdays, numbers)

	return spec
}

// Weekdays matches Monday to Friday. It cannot be combined with [Spec.Day].
// Like On, setting a weekday selector twice retains the first error.
//
//	spec := schedule.UTC().Weekdays().Hour(9)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) Weekdays() Spec {
	return spec.weekdayText("1-5")
}

// Weekends matches Saturday and Sunday. It cannot be combined with
// [Spec.Day].
// Without time fields the selected days match at midnight.
//
//	spec := schedule.UTC().Weekends().Hour(10)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) Weekends() Spec {
	return spec.weekdayText("0,6")
}

// weekdayText stores a fixed day-of-week text, rejecting a day-of-month rule.
func (spec Spec) weekdayText(text string) Spec {
	if spec.err == nil && spec.day != "" {
		fail(&spec, "weekdays cannot be combined with day")

		return spec
	}

	assign(&spec, &spec.weekday, "weekdays", text)

	return spec
}

// Every matches at a fixed step. The unit of the step is seconds when
// duration is under a minute, minutes when it is under an hour, and hours
// otherwise. Fields finer than the unit are 0; coarser fields match every
// value unless set. The duration must be a whole number of its unit that
// divides 60 (seconds and minutes) or 24 (hours) evenly, and under 24 hours.
// This is an aligned calendar cadence, not a delay measured from construction.
//
//	spec := schedule.UTC().Every(5 * time.Minute)
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) Every(duration time.Duration) Spec {
	if spec.err != nil {
		return spec
	}

	if spec.every {
		fail(&spec, "every is set twice")

		return spec
	}

	if duration <= 0 || duration >= 24*time.Hour {
		fail(&spec, "every %s must be positive and under 24h", duration)

		return spec
	}

	unit, cycle, target := time.Second, 60, &spec.second

	switch {
	case duration >= time.Hour:
		unit, cycle, target = time.Hour, 24, &spec.hour
	case duration >= time.Minute:
		unit, cycle, target = time.Minute, 60, &spec.minute
	}

	steps := int(duration / unit)

	if duration%unit != 0 || cycle%steps != 0 {
		fail(&spec, "every %s must be a whole number of %ss dividing %d evenly", duration, unit, cycle)

		return spec
	}

	text := "*"

	if steps > 1 {
		text = "*/" + strconv.Itoa(steps)
	}

	assign(&spec, target, "every", text)

	spec.every = spec.err == nil

	return spec
}

// Cron sets a raw cron expression: five fields, or six with seconds first.
// Each field accepts *, n, a,b, a-b, */n, and a-b/n, with numbers only. Day of
// week is 0-6 from Sunday. When both day of month and day of week are
// restricted, either may match. It cannot be combined with field methods.
// A step larger than a range selects only its first value. Invalid text is
// retained as the first Err; a later valid expression cannot clear it.
//
//	spec := schedule.UTC().Cron("0 */6 * * *")
//	if err := spec.Err(); err != nil {
//		return err
//	}
func (spec Spec) Cron(expression string) Spec {
	if spec.err != nil {
		return spec
	}

	if spec.cron != "" || spec.fieldsSet() {
		fail(&spec, "cron cannot be combined with other fields")

		return spec
	}

	if _, err := calendar.Parse(expression); err != nil {
		fail(&spec, "cron %q: %v", expression, err)

		return spec
	}

	spec.cron = strings.Join(strings.Fields(expression), " ")

	return spec
}

// compile validates spec and returns its cron expression text.
func (spec Spec) compile() (string, error) {
	switch {
	case spec.err != nil:
		return "", spec.err
	case spec.location == nil:
		return "", fmt.Errorf("%w: %w", ErrInvalidEntry, errNoZone)
	case spec.cron != "":
		return spec.cron, nil
	case !spec.fieldsSet():
		return "", fmt.Errorf("%w: no field set", ErrInvalidEntry)
	}

	fields := spec.timeFields()

	for _, text := range []string{spec.day, spec.month, spec.weekday} {
		fields = append(fields, cmpStar(text))
	}

	return strings.Join(fields, " "), nil
}

// timeFields compiles second, minute, and hour text into an owned slice with
// space for the calendar fields compile appends. Unset finer fields default to
// zero; unset coarser fields match every value. With no explicit time field,
// midnight is selected. A constant zero second is omitted, retaining five-field
// cron output without changing an explicitly selected zero-second cadence.
func (spec Spec) timeFields() []string {
	// Find the coarsest time field that is set.
	coarsest := -1

	for level, text := range []string{spec.second, spec.minute, spec.hour} {
		if text != "" {
			coarsest = level
		}
	}

	fields := make([]string, 0, 6)

	for level, text := range []string{spec.second, spec.minute, spec.hour} {
		switch {
		case text != "":
		case level < coarsest || coarsest == -1:
			text = "0"
		default:
			text = "*"
		}

		fields = append(fields, text)
	}

	// Seconds are only emitted when they are not always 0.
	if fields[0] == "0" {
		fields = fields[1:]
	}

	return fields
}

// cmpStar returns text, or "*" when text is empty.
func cmpStar(text string) string {
	if text == "" {
		return "*"
	}

	return text
}

// Err returns the first error found while building the spec, or nil. Errors
// wrap [ErrInvalidEntry].
// An unset zone or a zone with no fields is invalid even without a stored error.
//
//	spec := schedule.UTC().Hour(25)
//	if err := spec.Err(); err != nil {
//		fmt.Println(errors.Is(err, schedule.ErrInvalidEntry))
//	}
func (spec Spec) Err() error {
	_, err := spec.compile()

	return err
}

// Location returns the time zone the spec is read in. It is nil for a zero
// Spec or a spec built with a nil location.
// The returned location is shared rather than copied.
//
//	spec := schedule.UTC().Hour(3)
//	fmt.Println(spec.Location() == time.UTC)
func (spec Spec) Location() *time.Location {
	return spec.location
}

// Expression returns the compiled cron expression: five fields, or six with
// seconds first when seconds other than 0 are needed. It is empty when the
// spec is invalid.
// Call Err when an empty expression must be distinguished from a valid one.
//
//	spec := schedule.UTC().Day(1)
//	if err := spec.Err(); err != nil {
//		return err
//	}
//	fmt.Println(spec.Expression()) // 0 0 1 * *
func (spec Spec) Expression() string {
	expression, err := spec.compile()

	if err != nil {
		return ""
	}

	return expression
}

// HasSeconds reports whether [Spec.Expression] has a seconds field.
// Invalid specs return false; inspect Err before relying on the result.
//
//	spec := schedule.UTC().Second(30)
//	if err := spec.Err(); err != nil {
//		return err
//	}
//	fmt.Println(spec.HasSeconds()) // true
func (spec Spec) HasSeconds() bool {
	return len(strings.Fields(spec.Expression())) == 6
}
