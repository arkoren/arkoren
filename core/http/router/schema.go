package router

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
)

// Schema supplies a JSON Schema object or boolean for descriptive metadata.
// Implementations are rendered at declaration time; no validation is executed.
//
// Example:
//
//	var schema router.Schema = router.RawSchema(`{"type":"string"}`)
//	data, err := schema.JSONSchema()
//	if err != nil {
//		return err
//	}
//	_ = data
type Schema interface {
	// JSONSchema returns one JSON object or boolean, or a construction error.
	// Returned bytes are copied by the router, never retained directly.
	//
	// Example:
	//	data, err := router.RawSchema(`true`).JSONSchema()
	//	if err != nil {
	//		return err
	//	}
	//	_ = data
	JSONSchema() ([]byte, error)
}

// RawSchema is an unevaluated JSON Schema document. Arbitrary keywords and
// references are preserved; references are never fetched or rewritten.
//
// Example:
//
//	schema := router.RawSchema(`{"type":"integer","minimum":1}`)
//	data, err := schema.JSONSchema()
//	if err != nil {
//		return err
//	}
//	_ = data
type RawSchema []byte

// JSONSchema returns a detached copy after checking JSON syntax and the root
// kind. It accepts objects and booleans, not arrays, strings or null.
//
// Example:
//
//	data, err := router.RawSchema(`false`).JSONSchema()
//	if err != nil {
//		return err
//	}
//	_ = data
func (schema RawSchema) JSONSchema() ([]byte, error) {
	value := bytes.TrimSpace(schema)

	if !jsontext.Value(value).IsValid() {
		return nil, fmt.Errorf("invalid JSON schema syntax")
	}

	if value[0] != '{' && !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
		return nil, fmt.Errorf("schema must be an object or boolean")
	}

	return bytes.Clone(value), nil
}

// renderSchema captures external schema bytes immediately, so later changes
// to a schema implementation cannot change published route metadata.
func renderSchema(schema Schema) (jsontext.Value, error) {
	if schema == nil {
		return nil, fmt.Errorf("schema is nil")
	}

	data, err := schema.JSONSchema()

	if err != nil {
		return nil, err
	}

	data, err = RawSchema(data).JSONSchema()

	return jsontext.Value(data), err
}
