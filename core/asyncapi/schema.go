package asyncapi

import "arkoren.dev/core/asyncapi/internal/schema"

// Schema supplies a self-contained Draft 7 JSON Schema for documentation.
// Definitions capture the returned bytes immediately; implementations need not
// retain their output. Providers must not perform network resolution. Provider
// errors propagate to the definition that captures the schema.
//
// Example:
//
//	var payload asyncapi.Schema = asyncapi.RawSchema(`{"type":"string"}`)
//	data, err := payload.JSONSchema()
//	if err != nil {
//		return err
//	}
//	_ = data
type Schema interface {
	// JSONSchema returns one object or boolean schema, or an error. Capture
	// invokes this method once and takes an independent copy of its result.
	//
	// Example:
	//
	//	data, err := asyncapi.RawSchema(`true`).JSONSchema()
	//	if err != nil {
	//		return err
	//	}
	//	_ = data
	JSONSchema() ([]byte, error)
}

// RawSchema contains a JSON Schema in the supported, self-contained Draft 7
// subset. It accepts boolean schemas, tuple items and composition, but rejects
// references, identifiers, definitions, unknown keywords and other dialects.
// Only schema-bearing positions are checked: arbitrary defaults, enum values
// and x- extension contents remain opaque. Formats and patterns are annotations
// here; no instance validation, regexp conversion or reference resolution runs.
// Callers must not mutate the slice concurrently with capture or JSONSchema.
//
// Example:
//
//	data, err := asyncapi.RawSchema(`{"type":["string","null"]}`).JSONSchema()
//	if err != nil {
//		return err
//	}
//	_ = data
type RawSchema []byte

// JSONSchema validates the supported Draft 7 profile and returns an independent
// copy without re-encoding numbers or changing whitespace. Empty, malformed and
// unsupported schemas return errors with a JSON Pointer when available. This
// checks declaration shapes, not satisfiability or instance validity.
//
// Example:
//
//	data, err := asyncapi.RawSchema(`{"minimum":9007199254740993}`).JSONSchema()
//	if err != nil {
//		return err
//	}
//	_ = data
func (payload RawSchema) JSONSchema() ([]byte, error) {
	return schema.Capture(payload)
}
