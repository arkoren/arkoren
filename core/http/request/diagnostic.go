package request

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"

	"arkoren.dev/core/http/request/internal/decode"
	"arkoren.dev/foundation/validate"
)

// run decodes a body with read, then checks the value against schema,
// applying defaults when apply is set. Native JSON semantic failures become
// violations merged with the schema's violations. Syntax errors and ordinary
// operational/application decoder failures bypass that conversion. Violations
// return through [ValidationProblem].
func run[T any](request *http.Request, schema validate.Schema[T], apply bool, read func() (T, error)) (T, error) {
	value, err := validateBody(request, schema, apply, read)

	return value, ValidationProblem(err)
}

// validateBody decodes and checks the body for [run]. Schema-only failures keep
// their original [validate.Errors] shape. Converted native JSON failures join
// the merged violations with the native semantic cause, not the intermediate
// [ErrInvalidJSON] problem, so [ValidationProblem] can expose one 422 identity
// without losing errors.As/errors.Is access to decoder details. Other failures
// bypass conversion; a schema infrastructure error takes precedence.
func validateBody[T any](request *http.Request, schema validate.Schema[T], apply bool, read func() (T, error)) (T, error) {
	ctx := request.Context()
	value, decodeErr := read()

	var errs validate.Errors
	// decodeCause retains native diagnostics for server-side handling without
	// copying their text or values into the client-facing violations.
	var decodeCause error

	if decodeErr != nil {
		semantic, ok := errors.AsType[*json.SemanticError](decodeErr)

		if !ok || decode.CallbackError(semantic) {
			return value, decodeErr
		}

		if _, ok := errors.AsType[*jsontext.SyntacticError](decodeErr); ok {
			return value, decodeErr
		}

		decodeCause = semantic
		errs = decode.Violation(semantic).Localize(ctx)
	}

	parsed, schemaErr := check(request, schema, apply, value)

	if schemaErr != nil {
		schemaErrs, ok := errors.AsType[validate.Errors](schemaErr)

		if !ok {
			return value, schemaErr
		}

		errs = decode.Merge(errs, schemaErrs, decodeErr != nil)
	}

	if decodeCause != nil {
		return value, errors.Join(errs, decodeCause)
	}

	if len(errs) > 0 {
		return value, errs
	}

	if apply {
		return parsed, nil
	}

	return value, nil
}

// check runs Parse or Validate on value.
func check[T any](request *http.Request, schema validate.Schema[T], apply bool, value T) (T, error) {
	if apply {
		return schema.Parse(request.Context(), value)
	}

	return value, schema.Validate(request.Context(), value)
}
