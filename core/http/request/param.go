package request

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"

	"arkoren.dev/foundation/problem"
)

// Scalar is the set of types a path or query parameter can be parsed into.
// The tilde forms admit named types such as `type UserID int64`.
// Parsing uses the underlying Go scalar kind, not application schema rules.
//
// Example:
//
//	type UserID int64
//	id, err := request.Param[UserID](r, "id")
//	if err != nil {
//		return err
//	}
//	_ = id
type Scalar interface {
	~string | ~bool |
		~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

// ErrInvalidPathParameter is returned when a path parameter is empty or cannot
// be parsed as the requested type. The problem carries the "parameter" name and
// the "expected_type" Go type.
//
// Example:
//
//	failure := request.ErrInvalidPathParameter.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrInvalidPathParameter = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/request:invalid-path-parameter")
	details.Title("Invalid path parameter")
	details.Detail("The path parameter has an invalid value.")
	details.Status(http.StatusBadRequest)
})

// Param retrieves the named path parameter (see [http.Request.PathValue]) and
// parses it as T. Integers are range checked against T's size, so "300" is
// rejected for int8. An empty or missing value is an error for every T,
// including string; use [http.Request.PathValue] to read a raw string.
//
// It returns [ErrInvalidPathParameter] with the parameter name and type
// attached, and the parse error as its cause when there is one.
//
// Example:
//
//	id, err := request.Param[int64](r, "id")
//	if err != nil {
//		return err
//	}
//	_ = id
func Param[T Scalar](r *http.Request, name string) (T, error) {
	return parseScalar[T](r.PathValue(name), name, ErrInvalidPathParameter)
}

// ParamOr is like [Param] but returns fallback when the parameter is missing
// or cannot be parsed as T.
//
// Example:
//
//	page := request.ParamOr(r, "page", 1)
func ParamOr[T Scalar](r *http.Request, name string, fallback T) T {
	value, err := Param[T](r, name)

	if err != nil {
		return fallback
	}

	return value
}

// parseScalar parses raw as T, choosing the parser from T's underlying kind so
// named types work. It is shared by [Param] and [Query]. An empty raw value or
// a parse failure returns invalid annotated with name, T's type string, and
// the parse error when there is one. It never panics: [Scalar] restricts T to
// the kinds handled here.
func parseScalar[T Scalar](raw, name string, invalid problem.Details) (T, error) {
	var value T

	invalid = invalid.Derive(func(details *problem.Builder) {
		details.Extension("parameter", name)
		details.Extension("expected_type", reflect.TypeFor[T]().String())
	})

	if raw == "" {
		return value, invalid
	}

	field := reflect.ValueOf(&value).Elem()

	var err error

	switch kind := field.Kind(); {
	case kind == reflect.String:
		field.SetString(raw)
	case kind == reflect.Bool:
		var parsed bool

		parsed, err = strconv.ParseBool(raw)
		field.SetBool(parsed)
	case kind >= reflect.Int && kind <= reflect.Int64:
		var parsed int64

		parsed, err = strconv.ParseInt(raw, 10, field.Type().Bits())
		field.SetInt(parsed)
	case kind >= reflect.Uint && kind <= reflect.Uint64:
		var parsed uint64

		parsed, err = strconv.ParseUint(raw, 10, field.Type().Bits())
		field.SetUint(parsed)
	case kind == reflect.Float32 || kind == reflect.Float64:
		var parsed float64

		parsed, err = strconv.ParseFloat(raw, field.Type().Bits())
		field.SetFloat(parsed)
	default:
		err = fmt.Errorf("unsupported kind %s", kind)
	}

	if err != nil {
		var zero T

		return zero, invalid.Derive(func(details *problem.Builder) {
			details.Error(err)
		})
	}

	return value, nil
}
