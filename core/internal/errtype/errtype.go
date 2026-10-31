// Package errtype classifies errors into low-cardinality names for the
// error.type telemetry attribute. The name never contains error text, which
// can carry keys, values, or query arguments.
// Context cancellation classes win over owner-supplied sentinels; otherwise
// callers receive a matched name or the innermost Go type, never Error().
//
// Example: class := errtype.Of(context.DeadlineExceeded); fmt.Println(class)
package errtype

import (
	"context"
	"errors"
	"fmt"
)

// Sentinel names an error that callers match with [errors.Is].
// Names must be static, bounded classifications, not interpolated error text.
// Of checks supplied entries in order after the common context errors.
//
//	missing := errors.New("object missing")
//	class := errtype.Of(missing, errtype.Sentinel{Err: missing, Name: "storage.ErrNotFound"})
//	fmt.Println(class)
type Sentinel struct {
	// Err is the error to match.
	// Example: known := errtype.Sentinel{Err: context.Canceled, Name: "canceled"}; _ = known
	Err error

	// Name is the value reported when Err matches.
	// Example: known := errtype.Sentinel{Err: context.Canceled, Name: "canceled"}; fmt.Println(known.Name)
	Name string
}

// common are the sentinels every component reports by name.
var common = []Sentinel{
	{Err: context.Canceled, Name: "context.Canceled"},
	{Err: context.DeadlineExceeded, Name: "context.DeadlineExceeded"},
}

// Of returns the name of err's class: the Name of the first matching
// sentinel in known (after the context errors), otherwise the Go type of the
// innermost error in its Unwrap chain, such as "*pgconn.PgError". It returns
// "" for a nil err.
// It reads error chains synchronously; custom Is and Unwrap implementations
// must obey the ordinary errors package's contracts.
//
// Example: fmt.Println(errtype.Of(fmt.Errorf("receive: %w", context.Canceled)))
func Of(err error, known ...Sentinel) string {
	if err == nil {
		return ""
	}

	for _, sentinels := range [][]Sentinel{common, known} {
		for _, sentinel := range sentinels {
			if errors.Is(err, sentinel.Err) {
				return sentinel.Name
			}
		}
	}

	return fmt.Sprintf("%T", innermost(err))
}

// innermost follows single-error Unwrap links to the root cause, including a
// joined error that holds exactly one error. A joined error with several
// errors has no single cause, so it is returned as is.
func innermost(err error) error {
	for {
		next := unwrapOne(err)

		if next == nil {
			return err
		}

		err = next
	}
}

// unwrapOne returns the single error wrapped by err, or nil when err wraps
// none or several.
func unwrapOne(err error) error {
	joined, ok := err.(interface{ Unwrap() []error })

	if !ok {
		return errors.Unwrap(err)
	}

	if wrapped := joined.Unwrap(); len(wrapped) == 1 {
		return wrapped[0]
	}

	return nil
}
