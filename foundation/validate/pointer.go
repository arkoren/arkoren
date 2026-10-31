package validate

import (
	"context"
	"reflect"
)

// PtrSchema validates a pointer and the value it points to. Create one
// with [Ptr]. A nil pointer is absent; a pointer to a zero value is
// present, which makes Ptr the way to accept false, 0 or "" as real values.
// Settings belong to PtrBuilder; published schemas expose no configuration.
//
// Example:
//
//	schema := validate.Ptr(validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Min(0)
//	}))
//	value := 0
//	if err := schema.Validate(context.Background(), &value); err != nil {
//		panic(err)
//	}
type PtrSchema[T any] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[*T]

	// inner retains the pointee schema; violations stay at the pointer path.
	inner anySchema
	// constructed distinguishes a published definition from a zero schema.
	// Private conditional branches may intentionally omit pointee validation.
	constructed bool
}

// Ptr returns a schema for *T that validates the pointee with inner.
// Violations are reported at the pointer's own path.
// Callbacks run once in order after the explicit inner schema is attached.
// Value can replace the pointee definition; Use imports an exact *T schema.
// Nil inner schemas without a replacement are construction errors.
// Non-nil schema pointers are captured as values after the callbacks finish;
// reassigning the caller's schema variable cannot change the published rules.
//
// Example:
//
//	schema := validate.Ptr(validate.String[string](), func(pointer *validate.PtrBuilder[string]) {
//		pointer.Required()
//	})
//
//	value := ""
//	if err := schema.Validate(context.Background(), &value); err != nil {
//		panic(err)
//	}
func Ptr[T any](inner Schema[T], configure ...PtrBuilderFunc[T]) PtrSchema[T] {
	draft := &PtrBuilder[T]{
		inner: inner,
	}

	for _, callback := range configure {
		callback(draft)
	}

	if draft.inner != nil {
		draft.inner = schemaValue(draft.inner.(Schema[T]))
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema PtrSchema[T]) rtype() reflect.Type {
	return reflect.TypeFor[*T]()
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema PtrSchema[T]) compile(e *env, rep *report, where string) *node {
	typ := reflect.TypeFor[*T]()
	n := schema.build(e, rep, where, leafHooks[*T]{
		typ: typ,
		get: func(v reflect.Value) *T {
			pointer, _ := v.Interface().(*T)

			return pointer
		},
	})

	if schema.inner == nil {
		if !schema.constructed {
			rep.addf(where, "Ptr inner schema is nil")
		}

		return n
	}

	inner := schema.inner.compile(nil, rep, where+"*")

	n.mut = n.mut || inner.mut
	n.pre = func(st *state, v reflect.Value) {
		if v.IsNil() {
			return
		}

		cloned := reflect.New(typ.Elem())
		cloned.Elem().Set(v.Elem())
		v.Set(cloned)

		inner.prepare(st, v.Elem())
	}

	n.child = func(st *state, v reflect.Value) {
		if v.IsNil() {
			return
		}

		inner.runPrepared(st, v.Elem(), reflect.Value{}, true)
	}

	schema.verifyDefault(n, rep, where)

	return n
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.Ptr(validate.String[string]())
//	if err := schema.Validate(context.Background(), nil); err != nil {
//		panic(err)
//	}
func (schema PtrSchema[T]) Validate(ctx context.Context, v *T) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Ptr(validate.String[string]())
//	value, err := schema.Parse(context.Background(), nil)
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema PtrSchema[T]) Parse(ctx context.Context, v *T) (*T, error) {
	return execute(ctx, compileStandalone[*T](schema), v)
}

// Default sets the value used by Parse when the pointer is nil. Each
// result gets its own pointer to a copy of value.
//
// Example:
//
//	schema := validate.Ptr(validate.String[string](), func(pointer *validate.PtrBuilder[string]) {
//		pointer.Default("Ada")
//	})
//	value, err := schema.Parse(context.Background(), nil)
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(*value)
func (pointer *PtrBuilder[T]) Default(value T) *PtrBuilder[T] {
	pointer.base = pointer.setDefault(func() *T {
		copied := value

		return &copied
	})

	return pointer
}
