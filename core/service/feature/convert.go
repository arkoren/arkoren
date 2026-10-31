package feature

import (
	"fmt"
	"math"
	"reflect"
	"time"
)

// kindOf returns the [Kind] a request for T sends to the driver. The
// [Primitive] constraint guarantees T has one of the listed underlying
// types, so no other case is possible.
func kindOf[T Primitive]() Kind {
	switch reflect.TypeFor[T]().Kind() {
	case reflect.Bool:
		return KindBool
	case reflect.String:
		return KindString
	case reflect.Float32, reflect.Float64:
		return KindFloat
	default:
		return KindInt
	}
}

// validateSubject reports an error wrapping [ErrInvalidSubject] for the first
// attribute whose dynamic type is not one of string, bool, int64, float64,
// []string, or time.Time. The error names the attribute and the type but
// never the value, so it is safe to log.
func validateSubject(subject Subject) error {
	for name, value := range subject.Attributes {
		switch value.(type) {
		case string, bool, int64, float64, []string, time.Time:
		default:
			return fmt.Errorf("feature: attribute %q: unsupported type %T: %w", name, value, ErrInvalidSubject)
		}
	}

	return nil
}

// convert turns a driver's non-nil value, requested as kind, into T. A bool
// or string must match its kind exactly. An int64 converts to any integer
// type that holds it, or to a float type. A float64 converts to a float type
// (float32 must not overflow) or, for KindInt, to an integer type when it is
// a whole number within range. Anything else, including a Go type a driver
// should never return, fails with [ErrTypeMismatch]. The error never
// contains the value.
func convert[T Primitive](kind Kind, value any) (T, error) {
	var out T
	target := reflect.ValueOf(&out).Elem()
	// mismatch is a function so that the error is only built on failure; a
	// successful conversion allocates nothing.
	mismatch := func() error {
		return fmt.Errorf("value of type %T cannot be read as %s %T: %w", value, kind, out, ErrTypeMismatch)
	}

	switch v := value.(type) {
	case bool:
		if kind != KindBool {
			return out, mismatch()
		}

		target.SetBool(v)
	case string:
		if kind != KindString {
			return out, mismatch()
		}

		target.SetString(v)
	case int64:
		switch kind {
		case KindInt:
			if target.OverflowInt(v) {
				return out, mismatch()
			}

			target.SetInt(v)
		case KindFloat:
			target.SetFloat(float64(v))
		default:
			return out, mismatch()
		}
	case float64:
		switch kind {
		case KindFloat:
			if target.OverflowFloat(v) {
				return out, mismatch()
			}

			target.SetFloat(v)
		case KindInt:
			// The upper bound is exclusive because float64(math.MaxInt64)
			// rounds up to 2^63, which int64 cannot hold.
			whole := v == math.Trunc(v) && v >= math.MinInt64 && v < math.MaxInt64

			if !whole || target.OverflowInt(int64(v)) {
				return out, mismatch()
			}

			target.SetInt(int64(v))
		default:
			return out, mismatch()
		}
	default:
		return out, mismatch()
	}

	return out, nil
}
