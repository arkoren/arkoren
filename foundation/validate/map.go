package validate

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
)

// MapSchema validates maps, their keys and their values. Create one with
// [Map]. A nil map is absent; an empty non-nil map is present. Keys and
// values are always present.
// A zero MapSchema is uninitialized. Use Map even when only collection-level
// rules are needed, and configure settings on its temporary MapBuilder.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.Has("age")
//	})
//	if err := schema.Validate(context.Background(), map[string]int{"age": 0}); err != nil {
//		panic(err)
//	}
type MapSchema[K comparable, V any] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[map[K]V]

	// key retains the optional exact-key-type schema applied to each map key.
	key anySchema
	// val retains the optional exact-value-type schema applied to each map value.
	val anySchema
	// constructed distinguishes an explicit collection definition from zero.
	constructed bool
}

// Map publishes a schema for map[K]V. Callbacks run once in order; Keys and
// Values optionally add exact-type child validation. Retained drafts cannot
// change the result. Entry violations extend the collection pointer by key,
// with entries visited in sorted diagnostic-name order. JSON-compatible keys
// use their JSON-v2 object name, including custom JSON and text marshaling.
// Other Go keys retain fmt.Sprint names; validation does not require JSON
// encodability. Different keys with the same name share a diagnostic path and
// retain every violation, but their relative order is unspecified. Key
// marshalers must be deterministic, concurrency-safe and must not mutate keys.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.Keys(func(key *validate.StringBuilder[string]) {
//			key.Min(1)
//		})
//		entries.Values(func(number *validate.IntBuilder[int]) {
//			number.Min(0)
//		})
//	})
//
//	if err := schema.Validate(context.Background(), map[string]int{"age": 0}); err != nil {
//		panic(err)
//	}
func Map[K comparable, V any](configure ...MapBuilderFunc[K, V]) MapSchema[K, V] {
	draft := &MapBuilder[K, V]{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema MapSchema[K, V]) rtype() reflect.Type {
	return reflect.TypeFor[map[K]V]()
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema MapSchema[K, V]) compile(e *env, rep *report, where string) *node {
	if !schema.constructed {
		rep.addf(where, "Map schema is uninitialized; use Map")
	}

	typ := reflect.TypeFor[map[K]V]()
	n := schema.build(e, rep, where, leafHooks[map[K]V]{
		typ: typ,
		get: func(v reflect.Value) map[K]V {
			entries, _ := v.Interface().(map[K]V)

			return entries
		},
	})

	key, val := &node{}, &node{}

	if schema.key != nil {
		key = schema.key.compile(nil, rep, where+"{key}")
	}

	if schema.val != nil {
		val = schema.val.compile(nil, rep, where+"{value}")
	}

	if key.mut {
		rep.addf(where, "Map key schemas cannot use Default, ExcludeIf or any normalizer (Trim, ToLower, Normalize and so on)")
	}

	n.mut = n.mut || val.mut
	n.pre = func(st *state, v reflect.Value) {
		if v.IsNil() {
			return
		}

		copied := reflect.MakeMapWithSize(typ, v.Len())
		iter := v.MapRange()

		for iter.Next() {
			if st.stopped() {
				return
			}

			writable := reflect.New(typ.Elem()).Elem()
			writable.Set(iter.Value())
			val.prepare(st, writable)
			copied.SetMapIndex(iter.Key(), writable)
		}

		v.Set(copied)
	}

	n.child = func(st *state, v reflect.Value) {
		runMapEntries(st, v, key, val)
	}

	schema.verifyDefault(n, rep, where)

	return n
}

// runMapEntries validates ranged key/value pairs in diagnostic-name order.
// Values are retained before sorting, so non-reflexive keys cannot turn into
// missing reflect.Values or silently substitute zero-valued predicate inputs.
func runMapEntries(st *state, v reflect.Value, key, val *node) {
	entries := make([]mapEntry[reflect.Value, reflect.Value], 0, v.Len())
	iterator := v.MapRange()

	for iterator.Next() {
		current := iterator.Key()
		entries = append(entries, mapEntry[reflect.Value, reflect.Value]{
			key: current, value: iterator.Value(), name: mapKeyName(current),
		})
	}

	slices.SortFunc(entries, func(left, right mapEntry[reflect.Value, reflect.Value]) int {
		return compareSegments(left.name, right.name)
	})

	typ := v.Type()
	var copied reflect.Value

	if val.mut {
		copied = reflect.MakeMapWithSize(typ, len(entries))
	}

	for _, entry := range entries {
		if st.stopped() {
			return
		}

		st.push(entry.name)
		key.run(st, entry.key, reflect.Value{}, true)

		if val.mut {
			writable := reflect.New(typ.Elem()).Elem()
			writable.Set(entry.value)
			val.runPrepared(st, writable, reflect.Value{}, true)
			copied.SetMapIndex(entry.key, writable)
		} else {
			val.runPrepared(st, entry.value, reflect.Value{}, true)
		}

		st.pop()
	}

	if val.mut {
		v.Set(copied)
	}
}

// mapKeyName renders the unescaped JSON-v2 object name of an encodable key.
// StringifyNumbers mirrors the encoder's numeric map-key representation. Plain
// strings avoid codec allocation. Typed validation also accepts non-JSON keys:
// an encoding error or a nonstring result deliberately retains their legacy
// fmt.Sprint diagnostic name rather than failing otherwise valid Go input.
func mapKeyName(key reflect.Value) string {
	if key.Type() == reflect.TypeFor[string]() {
		return key.String()
	}

	encoded, err := json.Marshal(key.Interface(), json.StringifyNumbers(true))

	if err != nil {
		return fmt.Sprint(key.Interface())
	}

	var name string

	if err := json.Unmarshal(encoded, &name); err != nil {
		return fmt.Sprint(key.Interface())
	}

	return name
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.Map[string, int]()
//	if err := schema.Validate(context.Background(), map[string]int{"age": 3}); err != nil {
//		panic(err)
//	}
func (schema MapSchema[K, V]) Validate(ctx context.Context, v map[K]V) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Map[string, int]()
//	value, err := schema.Parse(context.Background(), map[string]int{"age": 3})
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema MapSchema[K, V]) Parse(ctx context.Context, v map[K]V) (map[K]V, error) {
	return execute(ctx, compileStandalone[map[K]V](schema), v)
}

// Default sets the map used by Parse when the input is nil. Each result
// gets its own copy.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.Default(map[string]int{"age": 0})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (entries *MapBuilder[K, V]) Default(value map[K]V) *MapBuilder[K, V] {
	value = cloneMap(value)

	entries.base = entries.setDefault(func() map[K]V {
		return cloneMap(value)
	})

	return entries
}

// cloneMap copies a map, keeping nil as nil.
func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}

	cloned := make(map[K]V, len(source))

	for key, value := range source {
		cloned[key] = value
	}

	return cloned
}

// Min requires at least n entries.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.Min(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (entries *MapBuilder[K, V]) Min(n int) *MapBuilder[K, V] {
	entries.base = entries.addRule(func(_ context.Context, v map[K]V) error {
		if len(v) >= n {
			return nil
		}

		return Fail("map.min", map[string]any{"min": n})
	}).annotate("minProperties", n)

	return entries
}

// Max allows at most n entries.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.Max(10)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (entries *MapBuilder[K, V]) Max(n int) *MapBuilder[K, V] {
	entries.base = entries.addRule(func(_ context.Context, v map[K]V) error {
		if len(v) <= n {
			return nil
		}

		return Fail("map.max", map[string]any{"max": n})
	}).annotate("maxProperties", n)

	return entries
}
