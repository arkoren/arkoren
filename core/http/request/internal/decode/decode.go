// Package decode converts native JSON semantics to safe validation feedback.
// It does not classify transport, storage, or application operational failures.
package decode

import (
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"time"

	"arkoren.dev/foundation/validate"
)

const (
	// codeDecode identifies a failure with no inferred wire expectation.
	codeDecode = "decode"
	// codeUnknown identifies an unexpected JSON member.
	codeUnknown = "unknown"
	// codeRequiredPrefix identifies presence rules suppressed on partial input.
	codeRequiredPrefix = "required"
)

// timeType identifies JSON v2's native timestamp representation.
var timeType = reflect.TypeFor[time.Time]()

// CallbackError identifies ordinary errors from custom decoding methods,
// whose cause alone cannot establish whether the client supplied bad input.
// Explicit JSON syntax errors remain input errors. time.Time uses JSON v2's
// built-in representation, so its format errors retain normal input handling.
// Text methods are invoked only for JSON strings; native non-string rejection
// remains an input error. No application method is invoked by this inspection.
func CallbackError(semantic *json.SemanticError) bool {
	if semantic.Err == nil || semantic.GoType == nil {
		return false
	}

	if _, ok := errors.AsType[*jsontext.SyntacticError](semantic.Err); ok {
		return false
	}

	goType := semantic.GoType

	for goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}

	if goType == timeType {
		return false
	}

	pointer := reflect.PointerTo(goType)

	switch {
	case pointer.Implements(reflect.TypeFor[json.Unmarshaler]()),
		pointer.Implements(reflect.TypeFor[json.UnmarshalerFrom]()):
		return true
	case pointer.Implements(reflect.TypeFor[encoding.TextUnmarshaler]()):
		return semantic.JSONKind == '"'
	default:
		return false
	}
}

// Merge adds schema violations to errs. JSON v2 leaves members after a failed
// decode untouched, so partial input suppresses misleading presence violations.
func Merge(errs, schemaErrs validate.Errors, partial bool) validate.Errors {
	if errs == nil {
		errs = validate.Errors{}
	}

	for pointer, violations := range schemaErrs {
		for _, violation := range violations {
			if partial && strings.HasPrefix(violation.Code, codeRequiredPrefix) {
				continue
			}

			errs.Add(pointer, violation)
		}
	}

	return errs
}

// Violation preserves the native JSON Pointer without treating GoType as a
// field-format description. Unknown names retain "unknown"; other semantic
// failures use "decode" with an optional qualified default representation.
// Native error text, input values, and Go type names never reach the message.
func Violation(semantic *json.SemanticError) validate.Errors {
	pointer := string(semantic.JSONPointer)
	errs := validate.Errors{}

	if errors.Is(semantic.Err, json.ErrUnknownName) {
		errs.Add(pointer, validate.Violation{Code: codeUnknown})

		return errs
	}

	representation := defaultJSONRepresentation(semantic.GoType)
	violation := validate.Violation{
		Code: codeDecode, Message: "cannot decode value using the configured JSON representation",
	}

	if representation != "" {
		violation.Params = map[string]any{"default_representation": representation}
	}

	errs.Add(pointer, violation)

	return errs
}

// defaultJSONRepresentation returns a nominal default representation, not the
// expectation of the failed field. GoType does not carry field annotations.
// Custom JSON methods and unsupported Go kinds have no inferred hint.
func defaultJSONRepresentation(goType reflect.Type) string {
	if goType == nil {
		return ""
	}

	for goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}

	if goType == timeType {
		return "string"
	}

	if goType == reflect.TypeFor[time.Duration]() {
		return ""
	}

	pointer := reflect.PointerTo(goType)

	switch {
	case pointer.Implements(reflect.TypeFor[json.Unmarshaler]()),
		pointer.Implements(reflect.TypeFor[json.UnmarshalerFrom]()):
		return ""
	case pointer.Implements(reflect.TypeFor[encoding.TextUnmarshaler]()):
		return "string"
	}

	if goType.Kind() == reflect.Slice || goType.Kind() == reflect.Array {
		if goType.Elem().Kind() == reflect.Uint8 {
			return "string"
		}
	}

	// names describes nominal Go-kind defaults; native string-backed types above
	// take precedence. An absent kind deliberately produces no hint.
	names := map[reflect.Kind]string{
		reflect.Bool: "boolean",
		reflect.Int:  "integer", reflect.Int8: "integer", reflect.Int16: "integer",
		reflect.Int32: "integer", reflect.Int64: "integer",
		reflect.Uint: "integer", reflect.Uint8: "integer", reflect.Uint16: "integer",
		reflect.Uint32: "integer", reflect.Uint64: "integer", reflect.Uintptr: "integer",
		reflect.Float32: "number", reflect.Float64: "number", reflect.String: "string",
		reflect.Slice: "array", reflect.Array: "array", reflect.Struct: "object", reflect.Map: "object",
	}

	return names[goType.Kind()]
}
