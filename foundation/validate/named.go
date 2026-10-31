package validate

import (
	"context"
	"fmt"
	"reflect"
)

// NamedBuilder attaches an existing slice or map schema to its named container
// type T. It supplies only type adaptation; configure rules on the source
// schema. A fresh draft is selected by Field, Values, or Value like other drafts.
// Retaining a draft cannot change a published schema.
//
// Example:
//
//	type Names []string
//	type Request struct { Names Names }
//	schema := validate.Struct(func(object *validate.StructBuilder[Request]) {
//		object.Field("Names", func(names *validate.NamedBuilder[Names]) {
//			names.Use(validate.Slice[string]())
//		})
//	})
//	_ = schema
type NamedBuilder[T any] struct {
	// inner is captured as a published schema value rather than a mutable pointer.
	inner anySchema
}

// NamedBuilderFunc configures one NamedBuilder during construction. Its alias
// identity accepts ordinary function values and slices without conversion.
//
// Example:
//
//	type Names []string
//	var configure validate.NamedBuilderFunc[Names] = func(names *validate.NamedBuilder[Names]) {
//		names.Use(validate.Slice[string]())
//	}
//	_ = configure
type NamedBuilderFunc[T any] = func(*NamedBuilder[T])

// NamedSchema adapts a schema to an identical named slice or map type. It
// preserves source presence, defaults, normalization, errors, and JSON Schema
// metadata. It does not infer custom JSON encoding; use Infer for wire metadata.
// A zero NamedSchema is invalid. Published adapters are safe for concurrent use
// under the same callback-ownership rules as their source schemas.
//
// Example:
//
//	type Names []string
//	schema := validate.Named[Names](validate.Slice[string]())
//	values, err := schema.Parse(context.Background(), Names{"Ada"})
//	if err != nil {
//		panic(err)
//	}
//	_ = values
type NamedSchema[T any] struct {
	// inner retains the immutable source definition, including child execution.
	inner anySchema
}

// Named adapts inner to the named slice or map T without converting elements
// or keys. T and the source must have identical underlying container types;
// scalar conversions and element-wise conversions are rejected. The source
// need not be unnamed. To register a named field, use a NamedBuilder callback.
// Invalid definitions follow Schema's normal export/error and execution/panic
// behavior. Parsing preserves T and never mutates caller-owned storage.
//
// Example:
//
//	type Names []string
//	schema := validate.Named[Names](validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Values(func(name *validate.StringBuilder[string]) {
//			name.Trim().Min(1)
//		})
//	}))
//	values, err := schema.Parse(context.Background(), Names{" Ada "})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(values[0]) // Ada
func Named[T, U any](inner Schema[U]) NamedSchema[T] {
	return NamedSchema[T]{inner: schemaValue(inner)}
}

// Use attaches one immutable container schema. Repeated attachments are rejected
// rather than silently replacing rules. Source schema pointers are snapshotted
// as values; nil sources remain invalid definitions, not omitted validation.
//
// Example:
//
//	type Names []string
//	type Request struct { Names Names }
//	schema := validate.Struct(func(object *validate.StructBuilder[Request]) {
//		object.Field("Names", func(names *validate.NamedBuilder[Names]) {
//			names.Use(validate.Slice[string]())
//		})
//	})
//	_ = schema
func (named *NamedBuilder[T]) Use[U any](schema Schema[U]) *NamedBuilder[T] {
	if named.inner != nil {
		invalidf("Named.Use cannot replace an attached schema")
	}

	named.inner = schemaValue(schema)

	return named
}

// draftSelf makes NamedBuilder available to the statically selected child API.
// Its concrete return type prevents external wrappers from becoming drafts.
func (named *NamedBuilder[T]) draftSelf() *NamedBuilder[T] {
	return named
}

// publish detaches the draft's source reference into an immutable adapter value.
// Source schemas already own their rule containers; publication shares those
// immutable definitions without replaying their construction callbacks.
func (named *NamedBuilder[T]) publish() anySchema {
	return NamedSchema[T]{inner: named.inner}
}

// rtype preserves exact T identity for enclosing field and child checks.
// Only compile may adapt the execution view after checking compatibility.
func (schema NamedSchema[T]) rtype() reflect.Type {
	return reflect.TypeFor[T]()
}

// compile reuses the source execution plan through writable pointer views.
// Pointer conversion checks identical underlying types and makes the adapted
// container slot writable without converting or copying individual entries.
func (schema NamedSchema[T]) compile(e *env, rep *report, where string) *node {
	typ := reflect.TypeFor[T]()

	if schema.inner == nil {
		rep.addf(where, "Named requires a source schema")

		return &node{}
	}

	if typ.Name() == "" || (typ.Kind() != reflect.Slice && typ.Kind() != reflect.Map) {
		rep.addf(where, "Named target %s must be a named slice or map", typ)

		return &node{}
	}

	source := reflect.PointerTo(schema.inner.rtype())

	if !reflect.PointerTo(typ).ConvertibleTo(source) {
		rep.addf(where, "Named target %s and source %s must have identical underlying types", typ, source.Elem())

		return &node{}
	}

	return namedNode(schema.inner.compile(e, rep, where), source)
}

// namedNode changes only reflected container views, not execution sequencing.
// Preparation still runs once and all source defaults and checks see a writable
// source-typed value backed by the same local slot as the named container.
func namedNode(inner *node, pointer reflect.Type) *node {
	result := *inner
	result.pre = namedAction(inner.pre, pointer)
	result.norm = namedAction(inner.norm, pointer)
	result.child = namedAction(inner.child, pointer)

	if inner.def != nil {
		result.def = func(v reflect.Value) {
			inner.def(namedView(v, pointer))
		}
	}

	result.steps = make([]step, len(inner.steps))

	for index, run := range inner.steps {
		result.steps[index] = func(st *state, current *cursor) bool {
			original := current.v
			current.v = namedView(original, pointer)
			defer func() {
				current.v = original
			}()

			return run(st, current)
		}
	}

	return &result
}

// namedAction adapts a nullable preparation or child hook to a writable source
// view. Keeping nil hooks nil preserves the engine's default/preparation rules.
func namedAction(action func(*state, reflect.Value), pointer reflect.Type) func(*state, reflect.Value) {
	if action == nil {
		return nil
	}

	return func(st *state, value reflect.Value) {
		action(st, namedView(value, pointer))
	}
}

// namedView preserves whether a container slot can be changed. Unreachable
// promoted fields are deliberately non-addressable zero values; their rules
// must still observe absence without allocating an embedded pointer or storage.
func namedView(value reflect.Value, pointer reflect.Type) reflect.Value {
	if !value.CanAddr() {
		return value.Convert(pointer.Elem())
	}

	return value.Addr().Convert(pointer).Elem()
}

// isRequired delegates presence metadata to the source without executing it.
// An uninitialized adapter has no presence metadata and is rejected on export
// or compilation rather than being interpreted as a valid optional schema.
func (schema NamedSchema[T]) isRequired() bool {
	return schema.inner != nil && schema.inner.isRequired()
}

// jsonSchema renders source metadata; the shared exporter checks adaptation
// compatibility through compile without executing runtime callbacks.
func (schema NamedSchema[T]) jsonSchema() (map[string]any, error) {
	if schema.inner == nil {
		return nil, fmt.Errorf("%w: Named requires a source schema", ErrInvalidSchema)
	}

	return schema.inner.jsonSchema()
}

// JSONSchema renders the source's supported constraints, not inferred custom
// encoding. Invalid adapters return an error wrapping ErrInvalidSchema.
//
// Example:
//
//	type Names []string
//	document, err := validate.Named[Names](validate.Slice[string]()).JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema NamedSchema[T]) JSONSchema() ([]byte, error) {
	return marshalJSONSchema(schema)
}

// Parse validates a copied T through the source execution plan and returns its
// normalized named result. Failure returns zero T and the source error.
// Invalid adapters panic with ErrInvalidSchema during compilation.
//
// Example:
//
//	type Names []string
//	values, err := validate.Named[Names](validate.Slice[string]()).Parse(context.Background(), Names{"Ada"})
//	if err != nil {
//		panic(err)
//	}
//	_ = values
func (schema NamedSchema[T]) Parse(ctx context.Context, value T) (T, error) {
	return execute(ctx, compileStandalone[T](schema), value)
}

// Validate performs the same preparation and checks as Parse without returning
// a normalized value. Caller-owned containers are not modified.
//
// Example:
//
//	type Names []string
//	err := validate.Named[Names](validate.Slice[string]()).Validate(context.Background(), Names{"Ada"})
//	if err != nil {
//		panic(err)
//	}
func (schema NamedSchema[T]) Validate(ctx context.Context, value T) error {
	_, err := schema.Parse(ctx, value)

	return err
}
