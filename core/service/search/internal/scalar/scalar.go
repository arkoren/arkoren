// Package scalar normalizes supported search operands and field lists.
package scalar

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"time"
)

// Value converts a caller value to what portable conditions carries: string,
// bool, and float64 for every integer and float type (so integers above 2^53
// lose precision), and an RFC 3339 UTC string for [time.Time]. Named types of
// those kinds are accepted. Anything else, nil included, is an error.
func Value(value any) (any, error) {
	if moment, ok := value.(time.Time); ok {
		return moment.UTC().Format(time.RFC3339Nano), nil
	}

	reflected := reflect.ValueOf(value)

	switch reflected.Kind() {
	case reflect.String:
		return reflected.String(), nil
	case reflect.Bool:
		return reflected.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(reflected.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(reflected.Uint()), nil
	case reflect.Float32, reflect.Float64:
		if number := reflected.Float(); !math.IsNaN(number) && !math.IsInf(number, 0) {
			return number, nil
		}

		return nil, errors.New("value is not a finite number")
	default:
		return nil, fmt.Errorf("unsupported value of type %T", value)
	}
}

// Fields returns a sorted copy of fields without duplicates.
func Fields(fields []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(fields)))
}
