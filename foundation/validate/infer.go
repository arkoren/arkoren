package validate

import (
	"context"
	"encoding"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"arkoren.dev/foundation/validate/internal/fields"
)

var (
	// durationType is time.Duration, which encoding/json/v2 cannot encode.
	durationType = reflect.TypeFor[time.Duration]()

	// textMarshalerType is the interface that makes a type a JSON string.
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()

	// mapKeyKinds are the kinds, besides text marshalers, that
	// encoding/json accepts as map keys.
	mapKeyKinds = []reflect.Kind{
		reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
	}
)

// InferSchema describes the JSON shape of the Go type T. Create one with
// [Infer]. A valid definition has no runtime rules: Validate and Parse accept
// values of T unchanged unless the context is canceled. Inference errors are
// still returned. This describes
// metadata, not encoded-instance validation; an encoder may reject a particular
// value, such as a nonfinite float, even when its Go type can be inferred.
//
// Example:
//
//	schema := validate.Infer[struct{ Name string }]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(string(document))
type InferSchema[T any] struct {
	// doc retains the inferred JSON Schema document for rendering and
	// composition.
	doc map[string]any
	// err retains an inference error so every execution and rendering entry point
	// reports it.
	err error
}

// Infer builds a type-only schema for T by reflection, so output types
// can be published as JSON Schema without writing a shape. The mapping
// follows what encoding/json/v2 produces:
//
//   - string, integer, unsigned, float and bool kinds, and named types of
//     them, become string, integer, integer with minimum 0, number and
//     boolean.
//   - A type whose value or pointer implements [encoding.TextMarshaler]
//     becomes a string, with "format" from its JSONSchemaFormat() string
//     method if it has one, so [Duration] is
//     {"type":"string","format":"go-duration"}. This is checked before the
//     kind, because Duration's kind is int64. time.Time is the exception:
//     it is {"type":"string","format":"date-time"}, as [Time] renders it.
//   - []byte and [N]byte are strings (base64 in JSON). Sequences of a
//     defined uint8 element type are arrays, following JSON-v2 dispatch.
//   - A struct becomes an object over its JSON fields, named, promoted and
//     excluded exactly like [Struct] names them. A field is listed in
//     "required" unless its json tag has omitempty or omitzero, or its
//     owner is an embedded pointer that can be nil. Explicit embed, inline
//     and unknown options are not modeled and their fields are omitted.
//     Numeric fields tagged with string become strings. Fields with a format
//     option become {} because formatter-specific representations are not
//     modeled. This does not guarantee that the encoder accepts the option.
//   - Slices and arrays become arrays, maps become objects whose
//     "additionalProperties" is the value schema, and a pointer is its
//     pointee ([Ptr] does the same, nil is not modeled). Map keys may be
//     strings, integers, floats, custom JSON/text marshalers, interfaces or
//     pointers to supported key types. The encoder still checks actual key
//     values and can reject nil pointers or a nonstring custom representation.
//   - any and other interfaces become {}, like [Any]. So do types with a
//     MarshalJSON method, whose JSON shape cannot be known, unless they
//     explicitly implement [JSONTypeProvider] for a native slice or map shape.
//
// "required" only describes the output: the key is always present. Infer
// does not enforce it, because false, 0 and "" are absent in this package
// and would reject real outputs such as count: 0.
//
// A recursive type, such as a tree node with a Children []Node field, is
// described down to its second occurrence, where the schema is {}: JSON
// Schema output here has no $ref, so the repeated level is left open.
//
// A type with no JSON representation (a raw time.Duration, channels,
// functions, complex numbers, unsafe pointers, or map keys of other kinds)
// makes the schema invalid: Err returns an error wrapping
// [ErrInvalidSchema] naming the path, and JSONSchema, Validate and Parse
// return it. Infer itself never panics, so it is safe in package-level
// variables for types the caller does not control.
//
// Example:
//
//	type Result struct {
//		Name    string            `json:"name"`
//		Elapsed validate.Duration `json:"elapsed"`
//		Note    string            `json:"note,omitempty"`
//	}
//
//	doc, err := validate.Infer[Result]().JSONSchema()
//	// {"properties":{"elapsed":{"format":"go-duration","type":"string"},
//	//  "name":{"type":"string"},"note":{"type":"string"}},
//	//  "required":["name","elapsed"],"type":"object"}
func Infer[T any]() InferSchema[T] {
	typ := reflect.TypeFor[T]()

	doc, err := inferDoc(typ, make(map[reflect.Type]bool))

	if err != nil {
		return InferSchema[T]{
			err: fmt.Errorf("%w: Infer[%s]: %w", ErrInvalidSchema, typ, err),
		}
	}

	return InferSchema[T]{
		doc: doc,
	}
}

// Err returns the error that made the schema invalid, or nil. It wraps
// [ErrInvalidSchema].
//
// Example:
//
//	schema := validate.Infer[string]()
//	if err := schema.Err(); err != nil {
//		panic(err)
//	}
func (schema InferSchema[T]) Err() error {
	return schema.err
}

// Validate returns [InferSchema.Err] for an invalid definition, otherwise the
// context error or nil. It applies no value rules, defaults or normalization
// and does not fields.Inspect or modify the supplied value.
//
// Example:
//
//	schema := validate.Infer[string]()
//	if err := schema.Validate(context.Background(), "Ada"); err != nil {
//		panic(err)
//	}
func (schema InferSchema[T]) Validate(ctx context.Context, _ T) error {
	if schema.err != nil {
		return schema.err
	}

	return ctx.Err()
}

// Parse returns v unchanged when the definition and context are valid. It does
// not copy or normalize arbitrary values. An inference or context error returns
// the zero value and the same error as [InferSchema.Validate].
//
// Example:
//
//	schema := validate.Infer[string]()
//	value, err := schema.Parse(context.Background(), "Ada")
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema InferSchema[T]) Parse(ctx context.Context, v T) (T, error) {
	if err := schema.Validate(ctx, v); err != nil {
		var zero T

		return zero, err
	}

	return v, nil
}

// JSONSchema renders the inferred type document, without inspecting a value
// or executing application code. It returns the inference error for an invalid
// definition. Inferred metadata does not add runtime value constraints.
//
// Example:
//
//	schema := validate.Infer[string]()
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema InferSchema[T]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema InferSchema[T]) rtype() reflect.Type {
	return reflect.TypeFor[T]()
}

// jsonSchema implements anySchema. It returns a copy because callers such
// as [Ptr] add keywords to the document they receive.
func (schema InferSchema[T]) jsonSchema() (map[string]any, error) {
	if schema.err != nil {
		return nil, schema.err
	}

	return maps.Clone(schema.doc), nil
}

// isRequired always returns false because type-only inference adds no runtime
// presence constraint. Required JSON properties are inferred from field tags.
func (schema InferSchema[T]) isRequired() bool {
	return false
}

// ruleBase supplies the inferred document to Use without inventing runtime
// constraints. Inference deliberately describes shape, not value validation.
func (schema InferSchema[T]) ruleBase() base[T] {
	if schema.err != nil {
		invalidf("Use: %v", schema.err)
	}

	rules := base[T]{}

	for name, value := range schema.doc {
		rules = rules.annotate(name, value)
	}

	return rules
}

// compile binds this schema's recorded rules and children to an execution
// node. There are no runtime value rules, so the node is empty; an invalid
// inference is reported like any other construction problem.
func (schema InferSchema[T]) compile(_ *env, rep *report, where string) *node {
	if schema.err != nil {
		rep.addf(where, "%v", schema.err)
	}

	return &node{}
}

// inferDoc builds the JSON Schema document of typ. path holds the
// composite types being described above this point, which is how a
// recursive type is detected.
func inferDoc(typ reflect.Type, path map[reflect.Type]bool) (map[string]any, error) {
	switch typ {
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}, nil
	case durationType:
		return nil, errors.New("time.Duration has no JSON representation; use validate.Duration")
	}

	switch typ.Kind() {
	case reflect.Pointer, reflect.Struct, reflect.Slice, reflect.Array, reflect.Map:
		if path[typ] {
			return map[string]any{}, nil
		}

		path[typ] = true
		defer delete(path, typ)
	}

	if typ.Kind() == reflect.Pointer {
		return inferDoc(typ.Elem(), path)
	}

	if reflect.PointerTo(typ).Implements(reflect.TypeFor[JSONTypeProvider]()) {
		return inferProvidedShape(typ, path)
	}

	if fields.Marshals(typ) {
		return map[string]any{}, nil
	}

	if reflect.PointerTo(typ).Implements(textMarshalerType) {
		doc := map[string]any{"type": "string"}

		if format := textFormat(typ); format != "" {
			doc["format"] = format
		}

		return doc, nil
	}

	return inferKindDoc(typ, path)
}

// inferKindDoc handles native JSON shapes after custom representation methods
// have been resolved. Collection recursion keeps the same active type path;
// unsupported kinds return a contextual inference error rather than a panic.
func inferKindDoc(typ reflect.Type, path map[reflect.Type]bool) (map[string]any, error) {
	switch typ.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return map[string]any{"type": "integer", "minimum": 0}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Interface:
		return map[string]any{}, nil
	case reflect.Struct:
		return inferStruct(typ, path)
	default:
		return inferCollectionDoc(typ, path)
	}
}

// inferCollectionDoc describes native element/value shapes and validates map
// key encodability. Builtin byte storage has a string representation, while a
// sequence of defined uint8 elements retains ordinary array dispatch.
func inferCollectionDoc(typ reflect.Type, path map[reflect.Type]bool) (map[string]any, error) {
	switch typ.Kind() {
	case reflect.Slice, reflect.Array:
		if typ.Elem() == reflect.TypeFor[byte]() {
			return map[string]any{"type": "string"}, nil
		}

		items, err := inferDoc(typ.Elem(), path)

		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}

		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		key := typ.Key()

		if !supportedMapKey(key) {
			return nil, fmt.Errorf("map key type %s has no JSON representation", key)
		}

		values, err := inferDoc(typ.Elem(), path)

		if err != nil {
			return nil, fmt.Errorf("values: %w", err)
		}

		return map[string]any{"type": "object", "additionalProperties": values}, nil
	default:
		return nil, fmt.Errorf("%s (kind %s) has no JSON representation", typ, typ.Kind())
	}
}

// supportedMapKey follows JSON-v2's pointer and dynamic-interface key dispatch
// before checking native kinds and custom representation methods. Recursive
// pointer-only types have no terminal wire value and are rejected without
// looping. This is type-only inspection: no marshaler is invoked, so actual
// values can still fail encoding or produce duplicate object names.
func supportedMapKey(typ reflect.Type) bool {
	seen := make(map[reflect.Type]bool)

	for typ.Kind() == reflect.Pointer {
		if seen[typ] {
			return false
		}

		seen[typ] = true
		typ = typ.Elem()
	}

	if fields.Marshals(typ) || reflect.PointerTo(typ).Implements(textMarshalerType) {
		return true
	}

	return typ.Kind() == reflect.Interface || slices.Contains(mapKeyKinds, typ.Kind())
}

// inferStruct builds the object document of a struct type from the same
// field table [Struct] uses, so both name fields identically.
func inferStruct(typ reflect.Type, path map[reflect.Type]bool) (map[string]any, error) {
	info := fields.Inspect(typ)
	properties := make(map[string]any, len(info.Fields))

	var required []string

	for _, field := range info.Fields {
		declaration := typ.FieldByIndex(field.Index)
		_, tag, _ := strings.Cut(declaration.Tag.Get("json"), ",")
		options := fields.Options(tag)
		doc, err := inferFieldDoc(field.Type, options, path)

		if err != nil {
			return nil, fmt.Errorf("field %q: %w", field.GoName, err)
		}

		properties[field.JSONName] = doc

		omitted := slices.ContainsFunc(options, func(option string) bool {
			return option == "omitempty" || option == "omitzero"
		})
		omitted = omitted || optionalEmbeddedOwner(typ, field.Index)

		if !omitted {
			required = append(required, field.JSONName)
		}
	}

	doc := map[string]any{"type": "object", "properties": properties}

	if required != nil {
		doc["required"] = required
	}

	return doc, nil
}

// inferFieldDoc accounts for field-local encoding options before inferring the
// underlying type. A format option is deliberately opaque: its interpretation
// belongs to the encoder (including custom marshalers), not to this schema
// package. Numeric stringification applies only to the inferred top-level
// number; custom JSON methods remain opaque and are never invoked here.
func inferFieldDoc(typ reflect.Type, options []string, path map[reflect.Type]bool) (map[string]any, error) {
	for _, option := range options {
		if strings.HasPrefix(option, "format:") {
			return map[string]any{}, nil
		}
	}

	doc, err := inferDoc(typ, path)

	if err != nil {
		return nil, err
	}

	numeric := doc["type"] == "number" || doc["type"] == "integer"

	if numeric && slices.Contains(options, "string") {
		return map[string]any{"type": "string"}, nil
	}

	return doc, nil
}

// optionalEmbeddedOwner reports whether a promoted field can disappear when
// an intermediate embedded pointer is nil. The terminal field's own pointer
// type does not make its JSON key optional; its tag handles that separately.
func optionalEmbeddedOwner(typ reflect.Type, index []int) bool {
	for _, position := range index[:len(index)-1] {
		typ = typ.Field(position).Type

		if typ.Kind() == reflect.Pointer {
			return true
		}
	}

	return false
}
