package request

import (
	"errors"
	"net/http"

	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"
)

// ValidationProblem converts a [validate.Errors] found anywhere in err's
// chain to [ErrValidationFailed] (422) carrying the violations as the
// "errors" member, with err kept as the cause for logging and tracing. A nil
// err returns nil, and any other error is returned unchanged.
//
// The Validate* and Parse* helpers already apply it. Call it for errors from
// schema.Validate or schema.Parse used directly. Its signature also fits the
// errormap package, so one registration covers every handler:
//
//	r.Use(errormap.Middleware(errormap.Func(request.ValidationProblem)))
//
// Example:
//
//	if err := schema.Validate(ctx, input); err != nil {
//		return request.ValidationProblem(err)
//	}
func ValidationProblem(err error) error {
	if err == nil {
		return nil
	}

	errs, ok := errors.AsType[validate.Errors](err)

	if !ok {
		return err
	}

	return ErrValidationFailed.Derive(func(details *problem.Builder) {
		details.Extension("errors", errs)
		details.Error(err)
	})
}

// ValidateJSON decodes the request body as JSON like [JSON] and checks the
// value against schema. Defaults are not applied; use [ParseJSON] for that.
// [Strict] turns unknown members into "unknown" violations; [MaxSize]
// and [Unlimited] change the size limit (see [DefaultMaxBodySize]).
//
// Malformed syntax, including duplicate names, returns [ErrInvalidJSON] (400).
// Native semantic failures become "decode" violations at the decoder's JSON
// Pointer, merged with schema violations under [ErrValidationFailed] (422).
// The native semantic cause remains available through errors.As/errors.Is in
// that 422 result. Schema infrastructure failures take precedence instead.
// Strict unknown members retain "unknown"; schema violation codes are unchanged.
// Ordinary application unmarshaler failures pass through without conversion.
//
// A "decode" violation never supplies params.expected: the native Go type does
// not establish the field's wire format. Optional params.default_representation
// describes that type's default representation only. For example, an integer
// field tagged with ,string still has an "integer" default hint, but requires
// a quoted number. Opaque custom JSON methods and unsupported Go kinds have no
// hint; time.Duration has no default representation. The safe generic message
// excludes native error text, input values, and Go type names. Add a "decode"
// entry to the validation catalog to translate it; the built-in fallback is
// English. See the package documentation for partial-decoding limits.
//
// Example:
//
//	input, err := request.ValidateJSON(r, userSchema, request.Strict(), request.MaxSize(1<<20))
//	if err != nil {
//		return err
//	}
//	_ = input
func ValidateJSON[T any](request *http.Request, schema validate.Schema[T], opts ...BodyOption) (T, error) {
	return run(request, schema, false, func() (T, error) {
		return JSON[T](request, opts...)
	})
}

// ValidateXML decodes the request body as XML like [XML] and checks the
// value against schema. Defaults are not applied. Native XML input errors,
// including invalid numeric fields, stay [ErrInvalidXML] (400), because XML
// errors carry no JSON Pointer. Operational errors pass through unchanged.
// [Strict] returns [ErrStrictUnsupported] before reading. Limits work as for [XML].
//
// Example:
//
//	input, err := request.ValidateXML(r, feedSchema)
//	if err != nil {
//		return err
//	}
//	_ = input
func ValidateXML[T any](request *http.Request, schema validate.Schema[T], opts ...BodyOption) (T, error) {
	return run(request, schema, false, func() (T, error) {
		return XML[T](request, opts...)
	})
}

// ParseJSON decodes like [ValidateJSON] and returns the result of
// schema.Parse: a copy of the decoded value with defaults applied.
// It preserves the same input-error and operational-error classifications.
//
// Example:
//
//	input, err := request.ParseJSON(r, userSchema)
//	if err != nil {
//		return err
//	}
//	_ = input
func ParseJSON[T any](request *http.Request, schema validate.Schema[T], opts ...BodyOption) (T, error) {
	return run(request, schema, true, func() (T, error) {
		return JSON[T](request, opts...)
	})
}

// ParseXML decodes like [ValidateXML] and returns the result of
// schema.Parse. Native numeric conversion failures remain 400 input problems;
// operational failures are not converted to schema feedback.
//
// Example:
//
//	input, err := request.ParseXML(r, feedSchema)
//	if err != nil {
//		return err
//	}
//	_ = input
func ParseXML[T any](request *http.Request, schema validate.Schema[T], opts ...BodyOption) (T, error) {
	return run(request, schema, true, func() (T, error) {
		return XML[T](request, opts...)
	})
}
