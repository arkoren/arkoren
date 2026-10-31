package calendar

import (
	"fmt"
	"strconv"
	"strings"
)

// Parse parses a five-field expression, or a six-field expression with
// seconds first.
func Parse(expression string) (Calendar, error) {
	fields := strings.Fields(expression)

	if len(fields) != 5 && len(fields) != 6 {
		return Calendar{}, fmt.Errorf("expected 5 or 6 fields, got %d", len(fields))
	}

	schedule := Calendar{
		second: 1,
	}

	if len(fields) == 6 {
		var err error

		schedule.second, _, err = parseField(fields[0], Seconds)

		if err != nil {
			return Calendar{}, err
		}

		fields = fields[1:]
	}

	var err error

	if schedule.minute, _, err = parseField(fields[0], Minutes); err != nil {
		return Calendar{}, err
	}

	if schedule.hour, _, err = parseField(fields[1], Hours); err != nil {
		return Calendar{}, err
	}

	if schedule.dom, schedule.domStar, err = parseField(fields[2], Days); err != nil {
		return Calendar{}, err
	}

	if schedule.month, _, err = parseField(fields[3], Months); err != nil {
		return Calendar{}, err
	}

	if schedule.dow, schedule.dowStar, err = parseField(fields[4], Weekdays); err != nil {
		return Calendar{}, err
	}

	return schedule, nil
}

// parseField parses one cron field into a bitset. It also reports whether the
// field text starts with "*".
func parseField(text string, bounds Bounds) (uint64, bool, error) {
	var bits uint64

	for part := range strings.SplitSeq(text, ",") {
		partBits, err := parsePart(part, bounds)

		if err != nil {
			return 0, false, err
		}

		bits |= partBits
	}

	return bits, strings.HasPrefix(text, "*"), nil
}

// parsePart parses one comma-separated element: "*", "n", "a-b", "*/n", or
// "a-b/n".
func parsePart(part string, bounds Bounds) (uint64, error) {
	rangeText, stepText, stepped := strings.Cut(part, "/")

	step := 1

	if stepped {
		parsed, err := strconv.Atoi(stepText)

		if err != nil || parsed < 1 {
			return 0, fmt.Errorf("%s: invalid step %q", bounds.Name, stepText)
		}

		step = parsed
	}

	if stepped && rangeText != "*" && !strings.Contains(rangeText, "-") {
		return 0, fmt.Errorf("%s: step needs * or a range, got %q", bounds.Name, rangeText)
	}

	low, high, err := parseRange(rangeText, bounds)

	if err != nil {
		return 0, err
	}

	var bits uint64
	// A larger step still selects only low. Capping it before addition keeps
	// accepted large integers from wrapping back into the calendar range.
	step = min(step, high-low+1)

	for value := low; value <= high; value += step {
		bits |= 1 << uint(value)
	}

	return bits, nil
}

// parseRange resolves a wildcard, scalar, or inclusive a-b range within bounds.
// A scalar returns equal endpoints, so bitset construction needs no special
// case. Invalid endpoints and reversed ranges return plain contextual errors;
// parsePart separately handles whether a step is permitted by the grammar.
func parseRange(text string, bounds Bounds) (int, int, error) {
	if text == "*" {
		return bounds.Low, bounds.High, nil
	}

	lowText, highText, ranged := strings.Cut(text, "-")

	low, err := parseValue(lowText, bounds)

	if err != nil {
		return 0, 0, err
	}

	if !ranged {
		return low, low, nil
	}

	high, err := parseValue(highText, bounds)

	if err != nil {
		return 0, 0, err
	}

	if low > high {
		return 0, 0, fmt.Errorf("%s: range %q is reversed", bounds.Name, text)
	}

	return low, high, nil
}

// parseValue parses one number and checks it against bounds.
func parseValue(text string, bounds Bounds) (int, error) {
	value, err := strconv.Atoi(text)

	if err != nil {
		return 0, fmt.Errorf("%s: invalid value %q", bounds.Name, text)
	}

	if value < bounds.Low || value > bounds.High {
		return 0, fmt.Errorf("%s: value %d outside %d-%d", bounds.Name, value, bounds.Low, bounds.High)
	}

	return value, nil
}
