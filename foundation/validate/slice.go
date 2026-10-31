package validate

import (
	"context"
	"reflect"
	"slices"
)

// SliceSchema validates slices and their elements. Create one with
// [Slice]. A nil slice is absent; an empty non-nil slice is present.
// Elements are always present, so their zero values are checked too.
// A zero SliceSchema is uninitialized; construct one with Slice, even when
// per-element rules are omitted. Settings belong to SliceBuilder.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Min(1)
//	})
//	if err := schema.Validate(context.Background(), []string{"Ada"}); err != nil {
//		panic(err)
//	}
type SliceSchema[E any] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[[]E]

	// elem retains the optional exact-element-type schema applied to each slice
	// entry.
	elem anySchema
	// constructed distinguishes an explicit collection definition from zero.
	constructed bool
}

// Slice publishes a schema for []E. Callbacks run once in order on one fresh
// draft; Values optionally defines per-element rules. Retained drafts cannot
// change the result. Entry violations extend the collection pointer by index.
//
// Order: element normalizers run on a copy of every element, then the
// slice normalizers (Sort, Filter, Reject, RejectZero, Dedupe, Normalize) in
// written order, then presence, Default and the slice rules, then the element
// rules. Indices in violations refer to the slice after normalization.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Values(func(text *validate.StringBuilder[string]) {
//			text.Trim().Min(1)
//		})
//	})
//
//	value, err := schema.Parse(context.Background(), []string{" Ada "})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value)
func Slice[E any](configure ...SliceBuilderFunc[E]) SliceSchema[E] {
	draft := &SliceBuilder[E]{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema SliceSchema[E]) rtype() reflect.Type {
	return reflect.TypeFor[[]E]()
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema SliceSchema[E]) compile(e *env, rep *report, where string) *node {
	if !schema.constructed {
		rep.addf(where, "Slice schema is uninitialized; use Slice")
	}

	typ := reflect.TypeFor[[]E]()
	n := schema.build(e, rep, where, leafHooks[[]E]{
		typ: typ,
		get: func(v reflect.Value) []E {
			items, _ := v.Interface().([]E)

			return items
		},
	})

	elem := &node{}

	if schema.elem != nil {
		elem = schema.elem.compile(nil, rep, where+"[]")
	}

	n.mut = n.mut || elem.mut
	n.pre = func(st *state, v reflect.Value) {
		if v.IsNil() {
			return
		}

		cloned := reflect.MakeSlice(typ, v.Len(), v.Len())
		reflect.Copy(cloned, v)
		v.Set(cloned)

		for index := range v.Len() {
			if st.stopped() {
				return
			}

			elem.prepare(st, v.Index(index))
		}
	}

	n.child = func(st *state, v reflect.Value) {
		for index := range v.Len() {
			if st.stopped() {
				return
			}

			st.pushIndex(index)
			elem.runPrepared(st, v.Index(index), reflect.Value{}, true)
			st.pop()
		}
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
//	schema := validate.Slice[string]()
//	if err := schema.Validate(context.Background(), []string{"Ada"}); err != nil {
//		panic(err)
//	}
func (schema SliceSchema[E]) Validate(ctx context.Context, v []E) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Slice[string]()
//	value, err := schema.Parse(context.Background(), []string{"Ada"})
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema SliceSchema[E]) Parse(ctx context.Context, v []E) ([]E, error) {
	return execute(ctx, compileStandalone[[]E](schema), v)
}

// Default sets the slice used by Parse when the input is nil. Each
// result gets its own copy.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Default([]string{"Ada"})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) Default(value []E) *SliceBuilder[E] {
	value = slices.Clone(value)

	items.base = items.setDefault(func() []E {
		return slices.Clone(value)
	})

	return items
}

// Sort orders the elements with cmp, keeping equal elements in their
// original order. It sorts a copy. It is a normalizer: it runs after the
// element normalizers and before presence, Default and every rule, in the
// order written among the slice normalizers. A nil slice stays nil.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Sort(strings.Compare)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) Sort(cmp func(E, E) int) *SliceBuilder[E] {
	if cmp == nil {
		invalidf("Sort function is nil")
	}

	items.base = items.normalize(func(v []E) []E {
		if v == nil {
			return nil
		}

		sorted := slices.Clone(v)
		slices.SortStableFunc(sorted, cmp)

		return sorted
	})

	return items
}

// Filter keeps the elements for which keep returns true. The callback
// receives the index and the element, like the foundation/collection
// Slice.Filter. The result is a new slice, empty but non-nil for a non-nil
// input, so it stays present. It is a normalizer, see Sort.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Filter(func(index int, value string) bool {
//			return value != ""
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) Filter(keep func(int, E) bool) *SliceBuilder[E] {
	if keep == nil {
		invalidf("Filter function is nil")
	}

	items.base = items.normalize(func(v []E) []E {
		return filterSlice(v, keep)
	})

	return items
}

// Reject drops the elements for which drop returns true. It is the
// opposite of Filter. It is a normalizer, see Sort.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Reject(func(index int, value string) bool {
//			return value == ""
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) Reject(drop func(int, E) bool) *SliceBuilder[E] {
	if drop == nil {
		invalidf("Reject function is nil")
	}

	items.base = items.normalize(func(v []E) []E {
		return filterSlice(v, func(index int, element E) bool {
			return !drop(index, element)
		})
	})

	return items
}

// RejectZero drops the elements that are the zero value of E. The result
// is empty but non-nil for a non-nil input, so RejectZero().Min(1) fails
// for []string{"", ""}. It is a normalizer, see Sort.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.RejectZero()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) RejectZero() *SliceBuilder[E] {
	items.base = items.normalize(func(v []E) []E {
		return filterSlice(v, func(_ int, element E) bool {
			return !isAbsent(reflect.ValueOf(&element).Elem())
		})
	})

	return items
}

// Dedupe drops repeated elements, keeping the first occurrence and the
// order. E must be comparable; Dedupe panics with [ErrInvalidSchema]
// otherwise. It is a normalizer, see Sort.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Dedupe()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) Dedupe() *SliceBuilder[E] {
	if elemType := reflect.TypeFor[E](); !elemType.Comparable() || elemType.Kind() == reflect.Interface {
		invalidf("Dedupe needs a comparable element type, got %s", elemType)
	}

	items.base = items.normalize(func(v []E) []E {
		seen := make(map[any]struct{}, len(v))

		return filterSlice(v, func(_ int, element E) bool {
			key := any(element)

			if _, duplicate := seen[key]; duplicate {
				return false
			}

			seen[key] = struct{}{}

			return true
		})
	})

	return items
}

// filterSlice returns a new slice of the elements keep accepts. A nil
// input stays nil.
func filterSlice[E any](v []E, keep func(int, E) bool) []E {
	if v == nil {
		return nil
	}

	kept := make([]E, 0, len(v))

	for index, element := range v {
		if keep(index, element) {
			kept = append(kept, element)
		}
	}

	return kept
}

// Min requires at least n elements.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Min(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) Min(n int) *SliceBuilder[E] {
	items.base = items.addRule(func(_ context.Context, v []E) error {
		if len(v) >= n {
			return nil
		}

		return Fail("slice.min", map[string]any{"min": n})
	}).annotate("minItems", n)

	return items
}

// Max allows at most n elements.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Max(10)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) Max(n int) *SliceBuilder[E] {
	items.base = items.addRule(func(_ context.Context, v []E) error {
		if len(v) <= n {
			return nil
		}

		return Fail("slice.max", map[string]any{"max": n})
	}).annotate("maxItems", n)

	return items
}

// Len requires exactly n elements.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Len(2)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) Len(n int) *SliceBuilder[E] {
	items.base = items.addRule(func(_ context.Context, v []E) error {
		if len(v) == n {
			return nil
		}

		return Fail("slice.len", map[string]any{"len": n})
	}).annotate("minItems", n).annotate("maxItems", n)

	return items
}

// Unique requires distinct elements. E must be comparable; Unique
// panics with [ErrInvalidSchema] otherwise.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.Unique()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (items *SliceBuilder[E]) Unique() *SliceBuilder[E] {
	if elemType := reflect.TypeFor[E](); !elemType.Comparable() || elemType.Kind() == reflect.Interface {
		invalidf("Unique needs a comparable element type, got %s", elemType)
	}

	items.base = items.addRule(func(_ context.Context, v []E) error {
		seen := make(map[any]struct{}, len(v))

		for _, element := range v {
			key := any(element)

			if _, duplicate := seen[key]; duplicate {
				return Fail("slice.unique", nil)
			}

			seen[key] = struct{}{}
		}

		return nil
	}).annotate("uniqueItems", true)

	return items
}
