package validate

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
)

// keyword is one JSON Schema keyword recorded by a builder call, such as
// {"minLength", 3} recorded by StringBuilder.Min during construction.
type keyword struct {
	// name is the JSON Schema keyword emitted by a recorded builder setting.
	name string
	// value is the JSON-compatible keyword value; later entries with the same
	// name win.
	value any
}

// annotate records the JSON Schema keyword that a rule maps to. When a
// keyword is recorded twice the later call wins, so a chain like
// Min(1).Min(2) renders minLength 2 even though both rules still run.
// Non-finite numeric annotations record a sticky definition diagnostic before
// replacement can hide them; compilation and export report the same problem.
// Floating values keep their concrete type for JSON encoding; reflection is
// used only for the finite check so named float32/float64 bounds behave alike.
func (rules base[T]) annotate(name string, value any) base[T] {
	number := reflect.ValueOf(value)
	kind := number.Kind()

	if kind == reflect.Float32 || kind == reflect.Float64 {
		bound := number.Float()
		finite := !math.IsNaN(bound) && !math.IsInf(bound, 0)

		if !finite {
			rules = rules.problemf("%s(%v): bound must be finite", name, bound)
		}
	}

	rules.keywords = append(slices.Clip(rules.keywords), keyword{
		name:  name,
		value: value,
	})

	return rules
}

// describe sets the human-readable JSON Schema annotation on a copied base.
// It changes documentation only; the validation rule sequence remains intact.
func (rules base[T]) describe(text string) base[T] {
	rules.description = text

	return rules
}

// isRequired reports whether unconditional Required was configured. Enclosing
// object rendering uses this flag to populate its required property list.
func (rules base[T]) isRequired() bool {
	return rules.required
}

// jsonDoc starts the JSON Schema document of a schema: the type, then the
// preset keywords, then the keywords recorded by rules, the description
// and the default. kind may be empty for schemas that take their type from
// an inner schema.
func (rules base[T]) jsonDoc(kind string, preset ...keyword) map[string]any {
	doc := make(map[string]any)

	if kind != "" {
		doc["type"] = kind
	}

	for _, entry := range slices.Concat(preset, rules.keywords) {
		if entry.name != "properties" {
			doc[entry.name] = entry.value
			continue
		}

		doc[entry.name] = mergeSchemaProperties(doc[entry.name], entry.value)
	}

	if rules.description != "" {
		doc["description"] = rules.description
	}

	if rules.def != nil {
		doc["default"] = rules.def()
	}

	return doc
}

// mergeSchemaProperties retains inferred fields while allowing explicit local
// child definitions to refine their annotations. Both source maps stay immutable.
func mergeSchemaProperties(current, incoming any) any {
	local, ok := current.(map[string]any)

	if !ok {
		return incoming
	}

	shared, ok := incoming.(map[string]any)

	if !ok {
		return incoming
	}

	merged := maps.Clone(shared)
	maps.Copy(merged, local)

	return merged
}

// marshalJSONSchema renders a schema document only after checking its complete
// executable definition. Metadata compilation disables default verification:
// user checks and normalizers never run merely to export a document. Rendering
// errors retain their original causes; definition errors wrap ErrInvalidSchema.
// Object keys are sorted so the output is stable.
func marshalJSONSchema(schema anySchema) ([]byte, error) {
	doc, err := schema.jsonSchema()

	if err != nil {
		return nil, err
	}

	rep := &report{}

	schema.compile(nil, rep, "")

	if len(rep.lines) > 0 {
		return nil, fmt.Errorf("%w: JSON Schema:\n  - %s", ErrInvalidSchema, strings.Join(rep.lines, "\n  - "))
	}

	return renderJSONSchema(doc)
}

// renderJSONSchema encodes a document with sorted keys.
func renderJSONSchema(doc map[string]any) ([]byte, error) {
	out, err := json.Marshal(doc, json.Deterministic(true))

	if err != nil {
		return nil, fmt.Errorf("validate: render JSON Schema: %w", err)
	}

	return out, nil
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// Definition errors wrap ErrInvalidSchema and return no document, including
// recorded Use errors and rules that need an enclosing struct. Export checks
// definitions but never executes Check or Normalize callbacks, including those
// attached to defaults. Default annotations retain the supplied value rather
// than a normalized runtime result.
//
// Example:
//
//	schema := validate.String[string]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema StringSchema[S]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// As with [StringSchema.JSONSchema], definition checks never execute validation
// callbacks; recorded construction errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Int[int]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema IntSchema[N]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// As with [StringSchema.JSONSchema], definition checks never execute validation
// callbacks; recorded construction errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Uint[uint]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema UintSchema[N]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// As with [StringSchema.JSONSchema], definition checks never execute validation
// callbacks; non-finite bounds and other definition errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Float[float64]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema FloatSchema[N]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// As with [StringSchema.JSONSchema], definition checks never execute validation
// callbacks; recorded construction errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Bool[bool]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema BoolSchema[B]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// As with [StringSchema.JSONSchema], definition checks never execute validation
// callbacks; recorded construction errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Time()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema TimeSchema) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// As with [StringSchema.JSONSchema], definition checks never execute validation
// callbacks; recorded construction errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Any()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema AnySchema) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// As with [StringSchema.JSONSchema], definition checks never execute validation
// callbacks; recorded construction errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Text[validate.Duration]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema TextSchema[T]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// As with [StringSchema.JSONSchema], definition checks never execute validation
// callbacks; recorded construction errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.File()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema FileSchema) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// Definition checks include the element schema without executing validation
// callbacks; recorded construction errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Slice[string]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema SliceSchema[E]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// Definition checks include keys even though key rules are not exported. They
// never execute validation callbacks; definition errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Map[string, int]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema MapSchema[K, V]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// Definition checks include the pointee without executing validation callbacks;
// recorded construction errors wrap ErrInvalidSchema.
//
// Example:
//
//	schema := validate.Ptr(validate.String[string]())
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema PtrSchema[T]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// JSONSchema renders this schema's supported JSON Schema constraints.
// Runtime-only rules remain enforced by Parse and Validate; invalid schema
// definitions or values that JSON cannot encode return an error.
// A zero StructSchema returns an error wrapping ErrInvalidSchema. Export never
// executes validation callbacks or rechecks field defaults verified by Struct.
//
// Example:
//
//	schema := validate.Struct[struct{}]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema StructSchema[T]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// jsonSchema renders string constraints and descriptive annotations. Runtime
// callbacks and cross-field observations have no JSON Schema representation.
func (schema StringSchema[S]) jsonSchema() (map[string]any, error) {
	return schema.jsonDoc("string"), nil
}

// jsonSchema renders signed-integer constraints and annotations. The numeric
// keywords retain the original limits without converting the validated type.
func (schema IntSchema[N]) jsonSchema() (map[string]any, error) {
	return schema.jsonDoc("integer"), nil
}

// jsonSchema renders unsigned-integer constraints with a minimum of zero.
// Explicit numeric annotations are merged with that inherent type boundary.
func (schema UintSchema[N]) jsonSchema() (map[string]any, error) {
	return schema.jsonDoc("integer", keyword{
		name:  "minimum",
		value: 0,
	}), nil
}

// jsonSchema renders floating-point constraints and annotations. Non-finite
// limits remain recorded construction diagnostics, even if a later annotation
// replaces them; marshalJSONSchema checks those before encoding the document.
func (schema FloatSchema[N]) jsonSchema() (map[string]any, error) {
	return schema.jsonDoc("number"), nil
}

// jsonSchema renders boolean constraints and annotations. Accepted and Declined
// contribute constant-value keywords rather than ordinary presence checks.
func (schema BoolSchema[B]) jsonSchema() (map[string]any, error) {
	return schema.jsonDoc("boolean"), nil
}

// jsonSchema describes time values as RFC 3339 date-time strings. Runtime
// comparisons against sibling fields or a clock are deliberately omitted.
func (schema TimeSchema) jsonSchema() (map[string]any, error) {
	return schema.jsonDoc("string", keyword{
		name:  "format",
		value: "date-time",
	}), nil
}

// jsonSchema emits annotations without a type keyword because Any accepts
// every JSON kind. Custom runtime checks do not add document constraints.
func (schema AnySchema) jsonSchema() (map[string]any, error) {
	return schema.jsonDoc(""), nil
}

// jsonSchema implements anySchema. Text types are strings, with the format
// the type declares, if any.
func (schema TextSchema[T]) jsonSchema() (map[string]any, error) {
	if schema.format == "" {
		return schema.jsonDoc("string"), nil
	}

	return schema.jsonDoc("string", keyword{
		name:  "format",
		value: schema.format,
	}), nil
}

// jsonSchema implements anySchema. An upload is not JSON; it is described
// as a binary string.
func (schema FileSchema) jsonSchema() (map[string]any, error) {
	return schema.jsonDoc("string", keyword{
		name:  "format",
		value: "binary",
	}), nil
}

// jsonSchema renders collection constraints and the optional element document.
// A zero, unconstructed schema returns ErrInvalidSchema instead of a document.
func (schema SliceSchema[E]) jsonSchema() (map[string]any, error) {
	if !schema.constructed {
		return nil, fmt.Errorf("%w: Slice schema is uninitialized", ErrInvalidSchema)
	}

	if schema.elem == nil {
		return schema.jsonDoc("array"), nil
	}

	items, err := schema.elem.jsonSchema()

	if err != nil {
		return nil, err
	}

	return schema.jsonDoc("array", keyword{
		name:  "items",
		value: items,
	}), nil
}

// jsonSchema implements anySchema. Keys are strings in JSON, so only the
// value schema is rendered.
func (schema MapSchema[K, V]) jsonSchema() (map[string]any, error) {
	if !schema.constructed {
		return nil, fmt.Errorf("%w: Map schema is uninitialized", ErrInvalidSchema)
	}

	if schema.val == nil {
		return schema.jsonDoc("object"), nil
	}

	values, err := schema.val.jsonSchema()

	if err != nil {
		return nil, err
	}

	return schema.jsonDoc("object", keyword{
		name:  "additionalProperties",
		value: values,
	}), nil
}

// jsonSchema implements anySchema. Nil is not modeled: the pointer renders
// as its pointee, with the pointer's own keywords layered on top.
func (schema PtrSchema[T]) jsonSchema() (map[string]any, error) {
	if schema.inner == nil {
		return nil, fmt.Errorf("%w: Ptr inner schema is nil", ErrInvalidSchema)
	}

	doc, err := schema.inner.jsonSchema()

	if err != nil {
		return nil, err
	}

	for name, value := range schema.jsonDoc("") {
		doc[name] = value
	}

	return doc, nil
}

// jsonSchema implements anySchema. Properties use the JSON names of the
// shape's fields; fields whose schema called Required are listed in
// "required" in struct order. A zero schema has no resolved shape and returns
// ErrInvalidSchema rather than pretending to describe an empty object.
func (schema StructSchema[T]) jsonSchema() (map[string]any, error) {
	if schema.env == nil {
		return nil, fmt.Errorf("%w: Struct schema is uninitialized; use Struct", ErrInvalidSchema)
	}

	properties := make(map[string]any, len(schema.fields))

	var required []string

	for _, field := range schema.fields {
		doc, err := field.schema.jsonSchema()

		if err != nil {
			return nil, fmt.Errorf("field %q: %w", field.info.GoName, err)
		}

		properties[field.info.JSONName] = doc

		if field.schema.isRequired() {
			required = append(required, field.info.JSONName)
		}
	}

	doc := schema.jsonDoc("object", keyword{
		name:  "properties",
		value: properties,
	})

	if required != nil {
		doc["required"] = required
	}

	return doc, nil
}
