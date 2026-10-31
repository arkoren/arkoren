package errormap

import (
	"errors"
	"net/http"
	"reflect"
	"slices"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// Mapping translates an error into another error. It returns nil, or its
// input unchanged, when it does not handle the error, so [Middleware] tries
// the next mapping. Build one with [Map] or [Func]. Matching an input with
// [errors.Is] does not make a replacement unchanged.
//
// Comparable errors use equality. Non-comparable values compare their fields
// and elements recursively, with maps and slices compared by storage identity
// rather than contents. Separately constructed value errors with equal fields
// are indistinguishable, and non-nil functions and NaNs cannot compare equal.
// Return nil to decline reliably for those cases, or use pointer errors when
// replacement identity matters.
// Mappings must not mutate their input; detection does not snapshot errors.
//
// Example:
//
//	mapping := errormap.Map(database.ErrNoRows, problem.NotFound())
//	r.Use(errormap.Middleware(mapping))
type Mapping func(err error) error

// Map returns a [Mapping] that replaces any error matching target, according
// to [errors.Is], with replacement. The replacement wraps the original error
// ([problem.Builder.Error] in [problem.Details.Derive]), so the cause stays available to logging and
// tracing and still matches target with [errors.Is].
//
// replacement is a [problem.Details] rather than an error because attaching
// the cause uses a derivation callback; any other error type could not carry it.
//
// Example:
//
//	errormap.Map(database.ErrNoRows, problem.NotFound().Derive(func(details *problem.Builder) {
//		details.Detail("No such record.")
//	}))
func Map(target error, replacement problem.Details) Mapping {
	return func(err error) error {
		if !errors.Is(err, target) {
			return nil
		}

		return replacement.Derive(func(details *problem.Builder) {
			details.Error(err)
		})
	}
}

// Func returns a [Mapping] backed by fn, for conversions that need to inspect
// the error, such as extracting a typed error with [errors.As]. fn returns the
// replacement error, or nil or the unchanged input to leave the error
// unhandled and try the next mapping. See [Mapping] for unchanged-value
// comparison limits. fn owns cause preservation for its replacement; unlike
// [Map], Func does not attach the original error automatically.
//
// The signature matches request.ValidationProblem, so
// errormap.Func(request.ValidationProblem) converts validate.Errors to a 422
// problem and passes every other error on, in any position.
//
// Example:
//
//	errormap.Func(func(err error) error {
//		if conflict, ok := errors.AsType[*ConflictError](err); ok {
//			return problem.Conflict().Derive(func(details *problem.Builder) {
//				details.Detail(conflict.Reason)
//				details.Error(err)
//			})
//		}
//
//		return nil
//	})
func Func(fn func(err error) error) Mapping {
	return Mapping(fn)
}

// Middleware returns middleware that maps errors returned by the next handler.
// Mappings are tried in the order given and the first one that returns an
// error other than nil or its unchanged input wins. Each mapping receives the
// original handler error, not a previous mapping's result. An unhandled error
// and a nil error pass through unchanged. Errors are only mapped when the
// handler returns them; a response already written is left alone.
//
// The ordered mapping slice is copied at construction; functions and their
// captured values remain shared. See [Mapping] for unchanged-value semantics.
//
// Register it first with Use so it sees the errors of every later middleware,
// including router-generated 404 and 405 problems at router-wide scope.
// Calling Middleware with no mappings is a no-op.
//
// Example:
//
//	r.Use(errormap.Middleware(
//		errormap.Map(database.ErrNoRows, problem.NotFound()),
//	))
func Middleware(mappings ...Mapping) router.Middleware {
	mappings = slices.Clone(mappings)

	return func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, request *http.Request) error {
			err := next(writer, request)

			if err == nil {
				return nil
			}

			original := reflect.ValueOf(err)

			for _, mapping := range mappings {
				if mapped := mapping(err); mapped != nil && !unchanged(reflect.ValueOf(mapped), original) {
					return mapped
				}
			}

			return err
		}
	}
}

// unchanged compares valid reflected error values and their members without
// invoking matching hooks or traversing pointers. Value.Comparable checks
// dynamic interface contents, unlike Type.Comparable, so Equal cannot panic.
// Nil interfaces return through equality before the recursive Elem call.
// Other value errors require matching fields and retained references, not
// merely matching categories; derived problem details, headers, and causes
// remain distinguishable. Reference cycles terminate at identity comparisons.
func unchanged(mapped, original reflect.Value) bool {
	if mapped.Comparable() {
		return mapped.Equal(original)
	}

	if original.Comparable() {
		return original.Equal(mapped)
	}

	if mapped.Type() != original.Type() {
		return false
	}

	switch original.Kind() {
	case reflect.Interface:
		return unchanged(mapped.Elem(), original.Elem())
	case reflect.Struct:
		return sameFields(mapped, original)
	case reflect.Array:
		return sameElements(mapped, original)
	default:
		return sameReference(mapped, original)
	}
}

// sameReference compares map, slice, or function values of the same type;
// unchanged handles all other kinds before calling it.
// Slice identity includes bounds as well as backing storage. Function identity
// is unavailable through reflection, so only two nil functions are unchanged.
func sameReference(mapped, original reflect.Value) bool {
	switch original.Kind() {
	case reflect.Map:
		return mapped.UnsafePointer() == original.UnsafePointer()
	case reflect.Slice:
		return mapped.UnsafePointer() == original.UnsafePointer() &&
			mapped.Len() == original.Len() && mapped.Cap() == original.Cap()
	default:
		return mapped.IsNil() && original.IsNil()
	}
}

// sameFields compares all fields, including private fields, of same-type
// non-comparable structs. Reading reflected values avoids exposing private
// fields through Interface, which would panic for types in other packages.
func sameFields(mapped, original reflect.Value) bool {
	for index := range original.NumField() {
		if !unchanged(mapped.Field(index), original.Field(index)) {
			return false
		}
	}

	return true
}

// sameElements compares each element of same-type non-comparable arrays.
// Their lengths are part of the type, so indexing requires no separate bounds
// check; reference-valued elements keep identity semantics through unchanged.
func sameElements(mapped, original reflect.Value) bool {
	for index := range original.Len() {
		if !unchanged(mapped.Index(index), original.Index(index)) {
			return false
		}
	}

	return true
}
