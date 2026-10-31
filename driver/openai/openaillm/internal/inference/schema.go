package inference

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"slices"

	"arkoren.dev/core/service/llm"
)

// parseSchema decodes a portable JSON Schema into the map form the SDK sends.
// When strict is true it first rewrites the schema for OpenAI's strict mode
// with [strictSchema]. A schema that is not a JSON object fails with
// [llm.ErrInvalidRequest].
func parseSchema(raw []byte, strict bool) (map[string]any, error) {
	var schema map[string]any

	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("openaillm: schema is not a JSON object: %w: %w", llm.ErrInvalidRequest, err)
	}

	if schema == nil {
		return nil, fmt.Errorf("openaillm: schema is null, want a JSON object: %w", llm.ErrInvalidRequest)
	}

	if strict {
		strictSchema(schema)
	}

	return schema, nil
}

// strictSchema rewrites schema in place for OpenAI's strict mode, which
// demands that every object lists all of its properties as required and
// forbids additional ones:
//
//   - every property of an object becomes required;
//   - a property that was optional gets "null" added to its type (or to its
//     enum, or as an extra anyOf branch), so the model can still omit it by
//     sending null;
//   - additionalProperties is set to false.
//
// The rewrite recurses through properties, items, anyOf, oneOf, allOf,
// $defs and definitions. Objects without "properties" (open maps) are left
// alone, because strict mode cannot express them; the server then rejects the
// request. A null the model sends for an optional field decodes to the zero
// value, which the validate package treats as absent.
func strictSchema(schema map[string]any) {
	if properties, ok := schema["properties"].(map[string]any); ok {
		strictProperties(schema, properties)
	}

	if items, ok := schema["items"].(map[string]any); ok {
		strictSchema(items)
	}

	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		branches, _ := schema[keyword].([]any)
		strictBranches(branches)
	}

	for _, keyword := range []string{"$defs", "definitions"} {
		definitions, _ := schema[keyword].(map[string]any)
		strictDefinitions(definitions)
	}
}

// strictProperties closes one object and recursively normalizes its property
// schemas. Only originally optional properties gain nullability, and required
// names are sorted for deterministic output. Non-object property values retain
// their existing representation for the provider to validate.
func strictProperties(schema, properties map[string]any) {
	required := map[string]bool{}
	list, _ := schema["required"].([]any)

	for _, name := range list {
		if name, ok := name.(string); ok {
			required[name] = true
		}
	}

	names := slices.Sorted(maps.Keys(properties))

	for _, name := range names {
		property, ok := properties[name].(map[string]any)

		if !ok {
			continue
		}

		strictSchema(property)

		if !required[name] {
			makeNullable(property)
		}
	}

	all := make([]any, len(names))

	for i, name := range names {
		all[i] = name
	}

	schema["required"] = all
	schema["additionalProperties"] = false
}

// strictBranches visits schema objects in a composition array. Empty arrays
// and non-object branches are left alone, matching the schema walk's policy
// of normalization rather than provider-schema validation.
func strictBranches(branches []any) {
	for _, branch := range branches {
		if branch, ok := branch.(map[string]any); ok {
			strictSchema(branch)
		}
	}
}

// strictDefinitions normalizes object-valued entries in either definitions
// spelling. It mutates the decoded request-local schema, not caller-owned JSON.
func strictDefinitions(definitions map[string]any) {
	for _, definition := range definitions {
		if definition, ok := definition.(map[string]any); ok {
			strictSchema(definition)
		}
	}
}

// makeNullable lets property accept null in addition to what it accepts now.
// A property with no usable type information (such as a bare $ref) is wrapped
// in an anyOf with a null branch.
func makeNullable(property map[string]any) {
	switch kind := property["type"].(type) {
	case string:
		if kind != "null" {
			property["type"] = []any{kind, "null"}
		}
	case []any:
		if !slices.Contains(kind, any("null")) {
			property["type"] = append(kind, "null")
		}
	default:
		if branches, ok := property["anyOf"].([]any); ok {
			property["anyOf"] = append(branches, map[string]any{"type": "null"})

			return
		}

		wrapped := map[string]any{}

		for key, value := range property {
			wrapped[key] = value
			delete(property, key)
		}

		property["anyOf"] = []any{wrapped, map[string]any{"type": "null"}}

		return
	}

	if values, ok := property["enum"].([]any); ok && !slices.Contains(values, nil) {
		property["enum"] = append(values, nil)
	}
}
