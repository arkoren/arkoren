package validate

import (
	"context"
	"iter"
	"reflect"
	"slices"
)

// mapEntry retains an actual ranged key/value pair and its diagnostic name.
// Sorting records rather than keys preserves values for non-reflexive keys
// such as NaN, which cannot be retrieved with a subsequent map lookup.
type mapEntry[K comparable, V any] struct {
	// key is the original map key, without normalization or replacement.
	key K
	// value is the value obtained in the same range iteration as key.
	value V
	// name is the rendered key used consistently for sorting and diagnostics.
	name string
}

// contextEntries stops runtime collection observations before another predicate
// callback when ctx is canceled. It does not alter pure construction-bound
// condition observations or the order of entries produced by the collection.
func contextEntries[K comparable, V any](ctx context.Context, entries iter.Seq2[K, V]) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for key, value := range entries {
			if ctx.Err() != nil {
				return
			}

			if !yield(key, value) {
				return
			}
		}
	}
}

// sliceEntries preserves the exact index/value pair and normalized order.
func sliceEntries[E any](values []E) iter.Seq2[int, E] {
	return func(yield func(int, E) bool) {
		for index, value := range values {
			if !yield(index, value) {
				return
			}
		}
	}
}

// mapEntries visits entries in the same diagnostic-name order as validation.
// Distinct keys with identical rendered names have unspecified relative order.
// Predicates always receive a key and value from one actual ranged entry.
func mapEntries[K comparable, V any](values map[K]V) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		entries := make([]mapEntry[K, V], 0, len(values))

		for key, value := range values {
			entries = append(entries, mapEntry[K, V]{key: key, value: value, name: entryName(key)})
		}

		slices.SortFunc(entries, func(left, right mapEntry[K, V]) int {
			return compareSegments(left.name, right.name)
		})

		for _, entry := range entries {
			if !yield(entry.key, entry.value) {
				return
			}
		}
	}
}

// entryName renders the same JSON Pointer segment used by the map engine.
func entryName[K comparable](key K) string {
	return mapKeyName(reflect.ValueOf(&key).Elem())
}

// matchingCount counts entries satisfying a pure predicate without changing
// the collection. Zero is a real derived value, not an absent integer.
func matchingCount[K comparable, V any](entries iter.Seq2[K, V], match func(K, V) bool) int {
	count := 0

	for key, value := range entries {
		if match(key, value) {
			count++
		}
	}

	return count
}

// containsEntry implements existential collection observations and assertions.
func containsEntry[K comparable, V any](entries iter.Seq2[K, V], match func(K, V) bool) bool {
	for key, value := range entries {
		if match(key, value) {
			return true
		}
	}

	return false
}

// everyEntry implements universal observations; an empty collection is true.
func everyEntry[K comparable, V any](entries iter.Seq2[K, V], match func(K, V) bool) bool {
	for key, value := range entries {
		if !match(key, value) {
			return false
		}
	}

	return true
}

// uniqueEntries tests projection uniqueness using the same traversal and
// comparable-key algorithm as the diagnostic assertion.
func uniqueEntries[K comparable, V any, U comparable](entries iter.Seq2[K, V], project func(K, V) U) bool {
	seen := make(map[U]struct{})

	for key, value := range entries {
		projection := project(key, value)

		if _, ok := seen[projection]; ok {
			return false
		}

		seen[projection] = struct{}{}
	}

	return true
}

// reportEvery emits exactly one violation at each failing entry, retaining
// both key and value association even when the surrounding map is reordered.
func reportEvery[K comparable, V any](st *state, entries iter.Seq2[K, V], match func(K, V) bool, code string) {
	for key, value := range entries {
		if match(key, value) {
			continue
		}

		st.push(entryName(key))
		st.violate(code, map[string]any{"key": key})
		st.pop()
	}
}

// reportDuplicates identifies each duplicate after its first occurrence and
// includes that first key/index in conflict parameters for useful diagnostics.
func reportDuplicates[K comparable, V any, U comparable](st *state, entries iter.Seq2[K, V], project func(K, V) U, code string) {
	seen := make(map[U]K)

	for key, value := range entries {
		projection := project(key, value)
		first, duplicate := seen[projection]

		if !duplicate {
			seen[projection] = key
			continue
		}

		st.push(entryName(key))
		st.violate(code, map[string]any{"key": key, "conflict": first, "value": projection})
		st.pop()
	}
}

// countConstraint constructs a restricted integer execution plan. Only pure
// numeric validation is accepted: no defaults, normalization, presence,
// conditions, field references or arbitrary operational Check functions.
func countConstraint(configure []IntBuilderFunc[int]) *node {
	count := &IntBuilder[int]{}

	for _, callback := range configure {
		callback(count)
	}

	if len(count.norms) > 0 || count.def != nil || count.custom {
		invalidf("CountWhere accepts only pure numeric constraints")
	}

	for _, operation := range count.ops {
		if operation.kind != opRule {
			invalidf("CountWhere accepts only pure numeric constraints")
		}
	}

	return compileStandalone[int](count.snapshot())
}

// countHolds evaluates a count constraint with forced presence and isolated
// state. Conditions observe only the boolean result and emit no diagnostics.
func countHolds(constraint *node, count int) bool {
	st := &state{
		ctx: context.Background(),
	}

	constraint.run(st, reflect.ValueOf(&count).Elem(), reflect.Value{}, true)

	return st.result() == nil
}

// reportCount maps each failed numeric constraint to a collection-level
// diagnostic. The original constraint code and params remain available.
func reportCount(st *state, constraint *node, count int, code string) {
	probe := &state{
		ctx: st.ctx,
	}

	constraint.run(probe, reflect.ValueOf(&count).Elem(), reflect.Value{}, true)

	for _, violation := range probe.errs[""] {
		params := cloneParams(violation.Params)

		if params == nil {
			params = make(map[string]any)
		}

		params["count"], params["constraint"], params["rule"] = count, violation.Message, violation.Code

		st.violate(code, params)
	}
}

// collectionObservation binds a typed iterator to a Go field path. Slice
// indexes must be exact int; maps must match both exact key and value types.
func collectionObservation[K comparable, V any](path string, test func(iter.Seq2[K, V]) bool) condition {
	return condition{
		bind: func(current *env, rep *report, where string) condFunc {
			field := current.lookupField(path, rep, where)

			if field == nil {
				return never
			}

			entries := collectionReader[K, V](field.Type)

			if entries == nil {
				rep.addf(where, "collection %q has type %s, incompatible with (%s, %s)", path, field.Type, reflect.TypeFor[K](), reflect.TypeFor[V]())

				return never
			}

			return func(object reflect.Value) bool {
				value := field.Value(object)

				if !value.IsValid() {
					return false
				}

				return test(entries(value))
			}
		},
	}
}

// collectionReader creates a typed view only after exact construction-time
// checks. Reflection is confined to the field binding, not predicate dispatch.
func collectionReader[K comparable, V any](typ reflect.Type) func(reflect.Value) iter.Seq2[K, V] {
	if typ.Kind() == reflect.Map && typ == reflect.TypeFor[map[K]V]() {
		return func(value reflect.Value) iter.Seq2[K, V] {
			entries, _ := value.Interface().(map[K]V)

			return mapEntries(entries)
		}
	}

	if typ.Kind() != reflect.Slice || typ != reflect.TypeFor[[]V]() || reflect.TypeFor[K]() != reflect.TypeFor[int]() {
		return nil
	}

	return func(value reflect.Value) iter.Seq2[K, V] {
		values, _ := value.Interface().([]V)

		return func(yield func(K, V) bool) {
			for index, element := range values {
				key, _ := any(index).(K)

				if !yield(key, element) {
					return
				}
			}
		}
	}
}

// validatePredicate rejects nil runtime functions during construction rather
// than turning programming errors into false collection observations.
func validatePredicate[K comparable, V any](predicate func(K, V) bool) {
	if predicate == nil {
		invalidf("collection predicate is nil")
	}
}

// mapHas observes existence even when the stored value is zero.
func mapHas[K comparable](value reflect.Value, key K) bool {
	return value.MapIndex(reflect.ValueOf(&key).Elem()).IsValid()
}

// keyObservation binds map membership without requiring a value type argument.
func keyObservation[K comparable](path string, keys []K, test func(int, int) bool) condition {
	keys = slices.Clone(keys)

	return condition{
		bind: func(current *env, rep *report, where string) condFunc {
			field := current.lookupField(path, rep, where)

			if field == nil {
				return never
			}

			if field.Type.Kind() != reflect.Map || field.Type.Key() != reflect.TypeFor[K]() {
				rep.addf(where, "Has(%q) requires a map with exact key type %s", path, reflect.TypeFor[K]())

				return never
			}

			return func(object reflect.Value) bool {
				value := field.Value(object)

				if !value.IsValid() {
					return false
				}

				count := 0

				for _, key := range keys {
					if mapHas(value, key) {
						count++
					}
				}

				return test(count, len(keys))
			}
		},
	}
}
