package validate

import (
	"context"
	"encoding"
	"reflect"
)

// TextSchema validates values of a type that encodes as a JSON string
// through [encoding.TextMarshaler] and [encoding.TextUnmarshaler], such as
// [Duration], net/netip.Addr or a UUID type. Create one with [Text]. A zero
// value of the type is absent, like for the other leaf schemas.
//
// The schema checks no format by itself, because the type has already
// parsed its text; add rules with Check. It exists so that such fields can
// be registered with [StructBuilder.Field] and render as a string in JSON Schema.
//
// Example:
//
//	schema := validate.Text[validate.Duration]()
//	value, err := schema.Parse(context.Background(), validate.Duration(time.Second))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value.Std())
type TextSchema[T any] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[T]

	// format is the JSON Schema "format" reported by the type's
	// JSONSchemaFormat method, or empty.
	format string
}

// jsonSchemaFormatter is implemented by types that name the JSON Schema
// "format" of their text form, such as [Duration].
type jsonSchemaFormatter interface {
	// JSONSchemaFormat names the JSON Schema format of the text form.
	JSONSchemaFormat() string
}

// Text returns a schema for T, a type whose pointer implements
// [encoding.TextMarshaler] and [encoding.TextUnmarshaler]. The pointer
// constraint is how Go expresses that UnmarshalText normally has a pointer
// receiver while the field holds a T: write Text[Duration](), the second
// type argument is inferred.
//
// The JSON Schema is {"type":"string"}, plus "format" when T or *T has a
// JSONSchemaFormat() string method. That method is called once, on a zero
// value, so it must return a constant.
//
// Example:
//
//	type Job struct {
//		Timeout validate.Duration
//	}
//	var job = validate.Struct[Job](func(object *validate.StructBuilder[Job]) {
//		object.Field("Timeout", func(timeout *validate.TextBuilder[validate.Duration]) {
//			timeout.Describe("Per attempt")
//		})
//	})
//	err := job.Validate(context.Background(), Job{
//		Timeout: validate.Duration(time.Second),
//	})
//	if err != nil {
//		panic(err)
//	}
func Text[T any, PT interface {
	*T
	encoding.TextMarshaler
	encoding.TextUnmarshaler
}](configure ...TextBuilderFunc[T]) TextSchema[T] {
	draft := &TextBuilder[T]{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// textFormat returns the JSON Schema format that typ declares through a
// JSONSchemaFormat method on typ or *typ, or the empty string.
func textFormat(typ reflect.Type) string {
	if formatter, ok := reflect.New(typ).Interface().(jsonSchemaFormatter); ok {
		return formatter.JSONSchemaFormat()
	}

	return ""
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema TextSchema[T]) rtype() reflect.Type {
	return reflect.TypeFor[T]()
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema TextSchema[T]) compile(e *env, rep *report, where string) *node {
	typ := reflect.TypeFor[T]()
	n := schema.build(e, rep, where, leafHooks[T]{
		typ: typ,
		get: func(v reflect.Value) T {
			value, _ := v.Interface().(T)

			return value
		},
	})

	schema.verifyDefault(n, rep, where)

	return n
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.Text[validate.Duration]()
//	if err := schema.Validate(context.Background(), validate.Duration(time.Second)); err != nil {
//		panic(err)
//	}
func (schema TextSchema[T]) Validate(ctx context.Context, v T) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Text[validate.Duration]()
//	value, err := schema.Parse(context.Background(), validate.Duration(time.Second))
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema TextSchema[T]) Parse(ctx context.Context, v T) (T, error) {
	return execute(ctx, compileStandalone[T](schema), v)
}
