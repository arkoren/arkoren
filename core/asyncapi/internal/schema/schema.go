// Package schema checks the self-contained Draft 7 documentation profile.
package schema

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"
)

// Capture validates the supported Draft 7 profile and returns an independent
// copy without re-encoding numbers or changing whitespace. Empty, malformed and
// unsupported schemas return errors with a JSON Pointer when available. This
// checks declaration shapes, not satisfiability or instance validity.
func Capture(data []byte) ([]byte, error) {
	value := jsontext.Value(data).Clone()

	if !value.IsValid() {
		return nil, fmt.Errorf("schema at /: expected valid JSON")
	}

	if err := checkSchema(value, ""); err != nil {
		return nil, err
	}

	return value, nil
}

// schemaError identifies the unsupported declaration, using escaped JSON
// Pointers. The root is displayed as / to keep root diagnostics visible.
func schemaError(path, reason string) error {
	if path == "" {
		path = "/"
	}

	return fmt.Errorf("schema at %s: %s", path, reason)
}

// schemaPath appends one member or index to a JSON Pointer. Property names are
// escaped rather than interpreted as keywords or pointer segments.
func schemaPath(path, member string) string {
	member = strings.ReplaceAll(member, "~", "~0")

	return path + "/" + strings.ReplaceAll(member, "/", "~1")
}

// checkSchema walks only schema positions. Sorting member names makes the
// first reported failure deterministic independently of Go map iteration.
func checkSchema(value jsontext.Value, path string) error {
	if value.Kind() == 't' || value.Kind() == 'f' {
		return nil
	}

	if value.Kind() != '{' {
		return schemaError(path, "expected a schema object or boolean")
	}

	var members map[string]jsontext.Value

	if err := json.Unmarshal(value, &members); err != nil {
		return schemaError(path, err.Error())
	}

	for _, key := range slices.Sorted(maps.Keys(members)) {
		if err := checkKeyword(key, members[key], schemaPath(path, key)); err != nil {
			return err
		}
	}

	return nil
}

// checkKeyword dispatches recursive schema positions separately from scalar
// constraints. Extension payloads are deliberately not interpreted as schemas.
func checkKeyword(key string, value jsontext.Value, path string) error {
	if strings.HasPrefix(key, "x-") {
		return nil
	}

	switch key {
	case "properties", "patternProperties":
		return checkSchemaMap(value, path)
	case "additionalProperties", "additionalItems", "contains", "propertyNames", "not", "if", "then", "else":
		return checkSchema(value, path)
	case "allOf", "anyOf", "oneOf":
		return checkSchemaList(value, path)
	case "items":
		if value.Kind() == '[' {
			return checkSchemaList(value, path)
		}

		return checkSchema(value, path)
	case "$schema":
		return checkSchemaDialect(value, path)
	case "type":
		return checkSchemaTypes(value, path)
	default:
		return checkConstraint(key, value, path)
	}
}

// checkSchemaDialect permits only the canonical Draft 7 declaration. Omission
// is handled by the caller; explicit incompatible dialects never fall back.
func checkSchemaDialect(value jsontext.Value, path string) error {
	var dialect string

	if err := json.Unmarshal(value, &dialect); err != nil {
		return schemaError(path, "expected the Draft 7 dialect URL")
	}

	if dialect != "http://json-schema.org/draft-07/schema#" {
		return schemaError(path, "only http://json-schema.org/draft-07/schema# is supported")
	}

	return nil
}

// checkSchemaMap checks a property-name-to-schema object. Names themselves
// are unrestricted, including names that resemble unsupported keywords.
func checkSchemaMap(value jsontext.Value, path string) error {
	if value.Kind() != '{' {
		return schemaError(path, "expected an object of schemas")
	}

	var members map[string]jsontext.Value

	if err := json.Unmarshal(value, &members); err != nil {
		return schemaError(path, err.Error())
	}

	for _, key := range slices.Sorted(maps.Keys(members)) {
		if err := checkSchema(members[key], schemaPath(path, key)); err != nil {
			return err
		}
	}

	return nil
}

// checkSchemaList checks nonempty composition or tuple-items arrays, retaining
// index-specific diagnostics and accepting boolean children.
func checkSchemaList(value jsontext.Value, path string) error {
	if value.Kind() != '[' {
		return schemaError(path, "expected a nonempty array of schemas")
	}

	var children []jsontext.Value

	if err := json.Unmarshal(value, &children); err != nil {
		return schemaError(path, err.Error())
	}

	if len(children) == 0 {
		return schemaError(path, "expected a nonempty array of schemas")
	}

	for index, child := range children {
		if err := checkSchema(child, schemaPath(path, fmt.Sprint(index))); err != nil {
			return err
		}
	}

	return nil
}

// checkConstraint validates supported nonrecursive keywords. Values of data
// annotations are opaque; no schema rules are applied inside their contents.
func checkConstraint(key string, value jsontext.Value, path string) error {
	switch key {
	case "const", "default":
		return nil
	case "title", "description", "format", "pattern", "contentEncoding", "contentMediaType", "$comment":
		return checkKind(value, '"', path, "a string")
	case "readOnly", "writeOnly", "uniqueItems":
		if value.Kind() == 't' || value.Kind() == 'f' {
			return nil
		}

		return schemaError(path, "expected a boolean")
	case "examples":
		return checkKind(value, '[', path, "an array")
	case "enum":
		return checkSchemaEnum(value, path)
	case "required":
		_, err := schemaStrings(value, path)

		return err
	default:
		return checkNumericKeyword(key, value, path)
	}
}

// checkSchemaEnum requires a nonempty data array without interpreting its
// values as schemas or imposing instance-validation semantics on them.
func checkSchemaEnum(value jsontext.Value, path string) error {
	if err := checkKind(value, '[', path, "a nonempty array"); err != nil {
		return err
	}

	var entries []jsontext.Value

	if err := json.Unmarshal(value, &entries); err != nil {
		return schemaError(path, err.Error())
	}

	if len(entries) == 0 {
		return schemaError(path, "expected a nonempty array")
	}

	return nil
}

// checkKind supplies consistent diagnostics for scalar keyword shapes.
func checkKind(value jsontext.Value, kind jsontext.Kind, path, expected string) error {
	if value.Kind() != kind {
		return schemaError(path, "expected "+expected)
	}

	return nil
}

// checkNumericKeyword gates numeric keywords before interpreting their values.
// Unknown keywords fail closed, including reference and newer-dialect keywords.
func checkNumericKeyword(key string, value jsontext.Value, path string) error {
	switch key {
	case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
		return checkSchemaNumber(key, value, path)
	case "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties":
		return checkSchemaNumber(key, value, path)
	default:
		return schemaError(path, "unsupported keyword "+key+"; use the self-contained Draft 7 subset")
	}
}

// checkSchemaNumber checks counts and bounds without converting to float64.
// Counts must be nonnegative integers and multipleOf must be strictly positive.
func checkSchemaNumber(key string, value jsontext.Value, path string) error {
	if value.Kind() != '0' {
		return schemaError(path, "expected a number")
	}

	number, ok := new(big.Rat).SetString(string(value))

	if !ok {
		return schemaError(path, "invalid number")
	}

	switch key {
	case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
		return nil
	case "multipleOf":
		if number.Sign() <= 0 {
			return schemaError(path, "expected a positive number")
		}

		return nil
	default:
		if number.Sign() < 0 || !number.IsInt() {
			return schemaError(path, "expected a nonnegative integer")
		}

		return nil
	}
}

// schemaStrings decodes distinct string lists for required and type. Empty
// required lists are legal Draft 7 declarations; callers impose other limits.
func schemaStrings(value jsontext.Value, path string) ([]string, error) {
	if value.Kind() != '[' {
		return nil, schemaError(path, "expected an array of distinct strings")
	}

	var values []jsontext.Value

	if err := json.Unmarshal(value, &values); err != nil {
		return nil, schemaError(path, "expected an array of distinct strings")
	}

	entries := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))

	for _, value := range values {
		if err := checkKind(value, '"', path, "an array of distinct strings"); err != nil {
			return nil, err
		}

		var entry string

		if err := json.Unmarshal(value, &entry); err != nil {
			return nil, schemaError(path, err.Error())
		}

		if seen[entry] {
			return nil, schemaError(path, "expected distinct strings")
		}

		seen[entry] = true
		entries = append(entries, entry)
	}

	return entries, nil
}

// checkSchemaTypes accepts Draft 7 type names, either singly or in a nonempty
// distinct array. Nullable declarations use the ordinary null type name.
func checkSchemaTypes(value jsontext.Value, path string) error {
	if value.Kind() == '"' {
		value = append(append(jsontext.Value{'['}, value...), ']')
	}

	entries, err := schemaStrings(value, path)

	if err != nil {
		return err
	}

	if len(entries) == 0 {
		return schemaError(path, "expected at least one type")
	}

	for _, entry := range entries {
		switch entry {
		case "null", "boolean", "object", "array", "number", "integer", "string":
		default:
			return schemaError(path, "unknown Draft 7 type "+entry)
		}
	}

	return nil
}
