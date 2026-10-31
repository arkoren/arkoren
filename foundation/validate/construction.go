package validate

import (
	"encoding"
	"reflect"
	"slices"
)

// draftPointer admits only a concrete draft pointer. The self-typed method
// prevents an external wrapper from satisfying the constraint by promotion.
type draftPointer[D any] interface {
	*D
	// draftSelf returns the exact concrete draft pointer.
	draftSelf() *D
	// publish returns the completed schema with its owned settings detached.
	publish() anySchema
}

// ChildBuilderFunc edits one exact concrete child draft. Its alias identity
// permits ordinary typed functions and slices without adapters.
//
// Example:
//
//	var name validate.ChildBuilderFunc[*validate.StringBuilder[string]] = func(rules *validate.StringBuilder[string]) {
//		rules.Required()
//	}
//
//	_ = name
type ChildBuilderFunc[P any] = func(P)

// String publishes a schema for S, including named string types. Callbacks
// run once in order on one fresh draft; nil entries and callback panics
// propagate. Retained drafts cannot change the published rule containers.
//
// Example:
//
//	schema := validate.String(func(rules *validate.StringBuilder[string]) {
//		rules.Trim()
//		rules.Required()
//	})
//
//	value, err := schema.Parse(context.Background(), " Ada ")
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value)
func String[S ~string](configure ...StringBuilderFunc[S]) StringSchema[S] {
	draft := &StringBuilder[S]{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// buildChild allocates a statically selected concrete draft without reflect
// allocation. Publication snapshots it after each callback has run once.
func buildChild[D any, P draftPointer[D]](configure []ChildBuilderFunc[P]) anySchema {
	draft := P(new(D))

	for _, callback := range configure {
		callback(draft)
	}

	return draft.publish()
}

// requireChild rejects missing composite definitions and exact-type mismatches
// at construction, before validation can inspect application data.
func requireChild(schema anySchema, typ reflect.Type, operation string) {
	if schema == nil {
		invalidf("%s requires a child schema", operation)
	}

	value := reflect.ValueOf(schema)

	if value.Kind() == reflect.Pointer && value.IsNil() {
		invalidf("%s child schema pointer is nil", operation)
	}

	if schema.rtype() != typ {
		invalidf("%s child validates %s, want %s", operation, schema.rtype(), typ)
	}
}

// Values defines the element schema of a slice using its concrete draft.
// The element's Go type must be E; callbacks and publication run immediately.
// Repeated local Values calls replace the local child until Use imports one.
// Replacing an imported child is an ErrInvalidSchema construction error.
//
// Example:
//
//	schema := validate.Slice[string](func(items *validate.SliceBuilder[string]) {
//		items.Values(func(value *validate.StringBuilder[string]) {
//			value.Required()
//		})
//	})
//
//	_ = schema
func (items *SliceBuilder[E]) Values[D any, P draftPointer[D]](configure ...ChildBuilderFunc[P]) *SliceBuilder[E] {
	schema := buildChild[D](configure)

	requireChild(schema, reflect.TypeFor[E](), "Slice.Values")

	if items.sharedElement {
		invalidf("Slice.Values cannot replace a child imported by Use")
	}

	items.elem = schema

	return items
}

// Keys defines the exact key schema of a map. Key normalization is rejected
// when the resulting schema is compiled, because it would change identity.
// Local Keys calls replace earlier local keys, but cannot replace Use imports.
//
// Example:
//
//	schema := validate.Map[string, int](func(entries *validate.MapBuilder[string, int]) {
//		entries.Keys(func(key *validate.StringBuilder[string]) {
//			key.Min(1)
//		})
//		entries.Values(func(value *validate.IntBuilder[int]) {
//			value.Positive()
//		})
//	})
//
//	_ = schema
func (entries *MapBuilder[K, V]) Keys[D any, P draftPointer[D]](configure ...ChildBuilderFunc[P]) *MapBuilder[K, V] {
	schema := buildChild[D](configure)

	requireChild(schema, reflect.TypeFor[K](), "Map.Keys")

	if entries.sharedKey {
		invalidf("Map.Keys cannot replace a child imported by Use")
	}

	entries.key = schema

	return entries
}

// Values defines the exact value schema of a map. The fresh child draft is
// published immediately, so later changes to a retained child do not leak.
// Local Values calls replace earlier local values, but cannot replace Use imports.
//
// Example:
//
//	schema := validate.Map[string, int](func(entries *validate.MapBuilder[string, int]) {
//		entries.Keys(func(key *validate.StringBuilder[string]) {
//		})
//		entries.Values(func(value *validate.IntBuilder[int]) {
//			value.Min(1)
//		})
//	})
//
//	_ = schema
func (entries *MapBuilder[K, V]) Values[D any, P draftPointer[D]](configure ...ChildBuilderFunc[P]) *MapBuilder[K, V] {
	schema := buildChild[D](configure)

	requireChild(schema, reflect.TypeFor[V](), "Map.Values")

	if entries.sharedValue {
		invalidf("Map.Values cannot replace a child imported by Use")
	}

	entries.val = schema

	return entries
}

// Value defines a pointer's exact pointee schema. A nonnil pointer forces
// its pointee present, so zero values still receive the child rules.
// Value replaces a local positional adapter or earlier local Value definition.
// It cannot replace a pointee imported by Use, which would discard shared rules.
//
// Example:
//
//	schema := validate.Ptr(validate.Int[int](), func(pointer *validate.PtrBuilder[int]) {
//		pointer.Value(func(value *validate.IntBuilder[int]) {
//			value.Min(0)
//		})
//	})
//
//	_ = schema
func (pointer *PtrBuilder[T]) Value[D any, P draftPointer[D]](configure ...ChildBuilderFunc[P]) *PtrBuilder[T] {
	schema := buildChild[D](configure)

	requireChild(schema, reflect.TypeFor[T](), "Ptr.Value")

	if pointer.sharedInner {
		invalidf("Ptr.Value cannot replace a child imported by Use")
	}

	pointer.inner = schema

	return pointer
}

// checkedTextFormat enforces Text's marshaling contract even when the draft
// is selected directly by Field rather than through the Text constructor.
func checkedTextFormat(typ reflect.Type) string {
	pointer := reflect.PointerTo(typ)

	if !pointer.Implements(reflect.TypeFor[encoding.TextMarshaler]()) || !pointer.Implements(reflect.TypeFor[encoding.TextUnmarshaler]()) {
		invalidf("Text[%s] requires text marshaling and unmarshaling methods", typ)
	}

	return textFormat(typ)
}

// detached separates all owned containers from a retained draft. Functions,
// defaults and their captures retain their documented application ownership.
func (rules base[T]) detached() base[T] {
	rules.ops = slices.Clone(rules.ops)
	rules.norms = slices.Clone(rules.norms)
	rules.keywords = slices.Clone(rules.keywords)
	rules.problems = slices.Clone(rules.problems)

	return rules
}

// schemaRules exposes immutable base settings without exposing configuration
// methods on the public schema. All executable built-in schemas provide it.
type schemaRules[T any] interface {
	// ruleBase exposes the schema's base rules to construction helpers.
	ruleBase() base[T]
}

// schemaValue normalizes the supported pointer/value forms at the Use boundary.
// A typed nil pointer is a domain error, not a promoted-method runtime panic.
// Plain nil remains available for the enclosing aggregated construction report.
func schemaValue[T any](schema Schema[T]) Schema[T] {
	if schema == nil {
		return nil
	}

	value := reflect.ValueOf(schema)

	if value.Kind() != reflect.Pointer {
		return schema
	}

	if value.IsNil() {
		invalidf("Use schema pointer is nil")
	}

	snapshot, ok := value.Elem().Interface().(Schema[T])

	if !ok {
		invalidf("Use requires a published schema value or its pointer")
	}

	return snapshot
}

// reusedDefinition accepts the destination role or type-only Infer metadata.
// Different roles with the same value type can hide child execution plans;
// rejecting them prevents a base-only import from silently weakening validation.
func reusedDefinition[T any, S Schema[T]](schema Schema[T]) S {
	if shared, ok := schema.(S); ok {
		return shared
	}

	var empty S

	if schema == nil {
		return empty
	}

	if _, ok := schema.(InferSchema[T]); ok {
		return empty
	}

	invalidf("Use requires %s or InferSchema[%s], got %T", reflect.TypeFor[S](), reflect.TypeFor[T](), schema)

	return empty
}

// importChild checks that a shared child will not replace another definition.
// Disjoint map key/value imports and schemas without children remain additive.
func importChild(current, incoming anySchema, operation string) bool {
	if incoming == nil {
		return false
	}

	if current != nil {
		invalidf("%s cannot replace an existing child definition", operation)
	}

	return true
}

// emptyPointee recognizes an unconstrained scalar adapter supplied to Ptr.
// Such an adapter has no child rules to lose when a complete pointer schema is
// imported. Composite adapters and scalar normalizers or validators never yield.
func emptyPointee[T any](schema anySchema) bool {
	typ := reflect.TypeFor[T]()

	if family(typ.Kind()) == 0 && typ != timeType {
		return false
	}

	value := reflect.ValueOf(schema)

	if value.Kind() == reflect.Pointer && value.IsNil() {
		return false
	}

	source, ok := schema.(schemaRules[T])

	if !ok {
		return false
	}

	return source.ruleBase().unconfigured()
}

// unconfigured identifies a genuinely empty scalar adapter, not merely a schema
// whose current input happens to pass. Metadata and bail policy count as edits
// too, so replacing an adapter cannot silently discard any deliberate setting.
func (rules base[T]) unconfigured() bool {
	if len(rules.ops) > 0 || len(rules.norms) > 0 {
		return false
	}

	if rules.def != nil || rules.bail {
		return false
	}

	if len(rules.keywords) > 0 || rules.description != "" {
		return false
	}

	return len(rules.problems) == 0
}

// inherit appends published rules and normalizers while preserving local
// construction order. The source remains immutable through copy-on-write.
func (rules base[T]) inherit(schema Schema[T]) base[T] {
	if schema == nil {
		rules.problems = append(slices.Clip(rules.problems), "the schema is nil")

		return rules
	}

	source, ok := any(schema).(schemaRules[T])

	if !ok {
		invalidf("Use requires an executable validation schema")
	}

	inherited := source.ruleBase()

	rules.problems = append(slices.Clip(rules.problems), inherited.problems...)
	rules.ops = append(slices.Clip(rules.ops), inherited.ops...)
	rules.norms = append(slices.Clip(rules.norms), inherited.norms...)
	rules.keywords = append(slices.Clip(rules.keywords), inherited.keywords...)
	rules = rules.inheritPresence(inherited)
	rules.bail = rules.bail || inherited.bail
	rules.custom = rules.custom || inherited.custom

	if inherited.description != "" {
		rules.description = inherited.description
	}

	return rules
}

// inheritPresence preserves the Required/Default exclusion invariant when
// shared schema settings are combined with local draft settings.
func (rules base[T]) inheritPresence(inherited base[T]) base[T] {
	if inherited.def != nil {
		rules = rules.setDefault(inherited.def)
	}

	if inherited.required && rules.def != nil {
		invalidf("Required cannot be combined with Default")
	}

	rules.required = rules.required || inherited.required

	return rules
}
