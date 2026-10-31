package validate

import (
	"context"
	"reflect"
)

// AnySchema validates values of the empty interface type. Create one with
// [Any]. A nil value is absent and every other value is present, whatever
// it holds, so Required means "not nil".
//
// Example:
//
//	schema := validate.Any(func(value *validate.AnyBuilder) {
//		value.Required()
//	})
//
// if err := schema.Validate(context.Background(), "created"); err != nil { panic(err) }
type AnySchema struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[any]
}

// anyType is the type [AnySchema] validates.
var anyType = reflect.TypeFor[any]()

// Any returns a schema that accepts any value, for fields declared as any
// or as an interface that the schema should not constrain. Its JSON Schema
// is {} (every JSON value is valid), plus the description and default when
// set. Struct matches it only against fields of exactly type any; use
// [AnyBuilder.Check] to inspect the dynamic value. Callbacks run once in order
// and retained drafts cannot change the published schema.
//
// Example:
//
//	type Event struct {
//		Payload any
//	}
//	var event = validate.Struct[Event](func(object *validate.StructBuilder[Event]) {
//		object.Field("Payload", func(payload *validate.AnyBuilder) {
//			payload.Required().Describe("Event-specific data")
//		})
//	})
//	err := event.Validate(context.Background(), Event{
//		Payload: "created",
//	})
//	if err != nil {
//		panic(err)
//	}
func Any(configure ...AnyBuilderFunc) AnySchema {
	draft := &AnyBuilder{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema AnySchema) rtype() reflect.Type {
	return anyType
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema AnySchema) compile(e *env, rep *report, where string) *node {
	n := schema.build(e, rep, where, leafHooks[any]{
		typ: anyType,
		get: func(v reflect.Value) any {
			return v.Interface()
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
//	schema := validate.Any()
//	if err := schema.Validate(context.Background(), "Ada"); err != nil {
//		panic(err)
//	}
func (schema AnySchema) Validate(ctx context.Context, v any) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Any()
//	value, err := schema.Parse(context.Background(), "Ada")
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema AnySchema) Parse(ctx context.Context, v any) (any, error) {
	return execute(ctx, compileStandalone[any](schema), v)
}
