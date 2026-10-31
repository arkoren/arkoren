package asyncapi

import (
	"encoding/json/jsontext"
	"fmt"
	"reflect"
)

// captureObject detaches an opaque object at the definition boundary, without
// re-encoding its contents or traversing protocol-specific members.
func captureObject(value RawObject) (jsontext.Value, error) {
	result := jsontext.Value(value).Clone()

	if !result.IsValid() || result.Kind() != '{' {
		return nil, fmt.Errorf("object: expected a valid JSON object")
	}

	return result, nil
}

// captureSchema calls a non-nil provider exactly once and detaches its bytes
// before checking the profile. Typed nil providers are rejected without calls;
// application panics are not recovered. Provider error identity is preserved.
func captureSchema(schema Schema) (jsontext.Value, error) {
	if schema == nil {
		return nil, fmt.Errorf("schema: nil provider")
	}

	value := reflect.ValueOf(schema)

	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, fmt.Errorf("schema: nil provider")
		}
	}

	data, err := schema.JSONSchema()

	if err != nil {
		return nil, fmt.Errorf("schema provider: %w", err)
	}

	result, err := RawSchema(data).JSONSchema()

	return jsontext.Value(result), err
}
