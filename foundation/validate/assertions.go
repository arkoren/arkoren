package validate

import (
	"context"
	"iter"
	"slices"
)

// Contains requires a matching normalized slice entry. An allocated empty
// slice fails; nil optional slices skip the assertion. Match must be pure.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
//		items.Contains(func(index, value int) bool {
//			return value > 0
//		})
//	})
//
//	_ = schema
func (items *SliceBuilder[E]) Contains(match func(int, E) bool) *SliceBuilder[E] {
	validatePredicate(match)

	items.base = items.addRule(func(ctx context.Context, values []E) error {
		if containsEntry(contextEntries(ctx, sliceEntries(values)), match) {
			return nil
		}

		return Fail("slice.contains", nil)
	})

	return items
}

// Every checks every normalized entry and reports each failing index. Empty
// slices pass vacuously. Match must not mutate observed values.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
//		items.Every(func(index, value int) bool {
//			return value >= 0
//		})
//	})
//
//	_ = schema
func (items *SliceBuilder[E]) Every(match func(int, E) bool) *SliceBuilder[E] {
	validatePredicate(match)

	items.base = items.with(op[[]E]{
		kind: opEntries,
		entries: func(st *state, values []E) {
			reportEvery(st, contextEntries(st.ctx, sliceEntries(values)), match, "slice.every")
		},
	})

	return items
}

// UniqueBy requires distinct comparable projections. Each repeated projection
// fails at its index with the first conflicting index in violation parameters.
// Project must not mutate observed values.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
//		items.UniqueBy(func(index int, value string) int {
//			return len(value)
//		})
//	})
//
//	_ = schema
func (items *SliceBuilder[E]) UniqueBy[U comparable](project func(int, E) U) *SliceBuilder[E] {
	if project == nil {
		invalidf("UniqueBy projection is nil")
	}

	items.base = items.with(op[[]E]{
		kind: opEntries,
		entries: func(st *state, values []E) {
			reportDuplicates(st, contextEntries(st.ctx, sliceEntries(values)), project, "slice.unique_by")
		},
	})

	return items
}

// CountWhere validates the matching entry count. Zero is always present.
// Only pure numeric constraints are allowed, not defaults, normalization,
// presence, field references, conditions or operational Check functions.
//
// Example:
//
//	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
//		items.CountWhere(func(index, value int) bool {
//			return value > 0
//		}, func(count *validate.IntBuilder[int]) {
//			count.Min(1)
//		})
//	})
//
//	_ = schema
func (items *SliceBuilder[E]) CountWhere(match func(int, E) bool, configure ...IntBuilderFunc[int]) *SliceBuilder[E] {
	validatePredicate(match)

	constraint := countConstraint(configure)

	items.base = items.with(op[[]E]{
		kind: opEntries,
		entries: func(st *state, values []E) {
			reportCount(st, constraint, matchingCount(contextEntries(st.ctx, sliceEntries(values)), match), "slice.count_where")
		},
	})

	return items
}

// Contains requires a matching map entry in sorted key order. Key and value
// always come from one entry. Empty maps fail; nil optional maps skip the rule.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.Contains(func(key string, value int) bool {
//			return key == "admin" && value > 0
//		})
//	})
//
//	_ = schema
func (entries *MapBuilder[K, V]) Contains(match func(K, V) bool) *MapBuilder[K, V] {
	validatePredicate(match)

	entries.base = entries.addRule(func(ctx context.Context, values map[K]V) error {
		if containsEntry(contextEntries(ctx, mapEntries(values)), match) {
			return nil
		}

		return Fail("map.contains", nil)
	})

	return entries
}

// Every reports each failing map key separately in deterministic order. Empty
// maps pass vacuously. Match must not mutate values or their reachable data.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.Every(func(key string, value int) bool {
//			return value >= 0
//		})
//	})
//
//	_ = schema
func (entries *MapBuilder[K, V]) Every(match func(K, V) bool) *MapBuilder[K, V] {
	validatePredicate(match)

	entries.base = entries.with(op[map[K]V]{
		kind: opEntries,
		entries: func(st *state, values map[K]V) {
			reportEvery(st, contextEntries(st.ctx, mapEntries(values)), match, "map.every")
		},
	})

	return entries
}

// UniqueBy requires distinct comparable projections in sorted map-key order.
// Each duplicate fails at its key with the first conflicting key in parameters.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.UniqueBy(func(key string, value int) int {
//			return value
//		})
//	})
//
//	_ = schema
func (entries *MapBuilder[K, V]) UniqueBy[U comparable](project func(K, V) U) *MapBuilder[K, V] {
	if project == nil {
		invalidf("UniqueBy projection is nil")
	}

	entries.base = entries.with(op[map[K]V]{
		kind: opEntries,
		entries: func(st *state, values map[K]V) {
			reportDuplicates(st, contextEntries(st.ctx, mapEntries(values)), project, "map.unique_by")
		},
	})

	return entries
}

// CountWhere validates the matching entry count with forced presence. Only
// pure numeric constraints are allowed; invalid settings fail construction.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.CountWhere(func(key string, value int) bool {
//			return value > 0
//		}, func(count *validate.IntBuilder[int]) {
//			count.Max(3)
//		})
//	})
//
//	_ = schema
func (entries *MapBuilder[K, V]) CountWhere(match func(K, V) bool, configure ...IntBuilderFunc[int]) *MapBuilder[K, V] {
	validatePredicate(match)

	constraint := countConstraint(configure)

	entries.base = entries.with(op[map[K]V]{
		kind: opEntries,
		entries: func(st *state, values map[K]V) {
			reportCount(st, constraint, matchingCount(contextEntries(st.ctx, mapEntries(values)), match), "map.count_where")
		},
	})

	return entries
}

// Has requires key existence, independent of the stored value's presence.
// Nil optional maps skip the rule; allocated empty maps fail.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.Has("admin")
//	})
//	_ = schema
func (entries *MapBuilder[K, V]) Has(key K) *MapBuilder[K, V] {
	entries.base = entries.addRule(func(_ context.Context, values map[K]V) error {
		if _, ok := values[key]; ok {
			return nil
		}

		return Fail("map.has", map[string]any{"key": key})
	})

	return entries
}

// HasAny requires at least one listed key. The input list is copied and an
// empty list always fails for a present map, regardless of stored zero values.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.HasAny("admin", "editor")
//	})
//	_ = schema
func (entries *MapBuilder[K, V]) HasAny(keys ...K) *MapBuilder[K, V] {
	keys = slices.Clone(keys)

	entries.base = entries.addRule(func(_ context.Context, values map[K]V) error {
		for _, key := range keys {
			if _, ok := values[key]; ok {
				return nil
			}
		}

		return Fail("map.has_any", map[string]any{"keys": slices.Clone(keys)})
	})

	return entries
}

// HasAll requires every listed key. The input list is copied; an empty list
// passes vacuously. Values at existing keys may be zero.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.HasAll("name", "age")
//	})
//	_ = schema
func (entries *MapBuilder[K, V]) HasAll(keys ...K) *MapBuilder[K, V] {
	keys = slices.Clone(keys)

	entries.base = entries.addRule(func(_ context.Context, values map[K]V) error {
		for _, key := range keys {
			if _, ok := values[key]; !ok {
				return Fail("map.has_all", map[string]any{"keys": slices.Clone(keys), "missing": key})
			}
		}

		return nil
	})

	return entries
}

// Len requires exactly size entries, annotating minProperties and maxProperties
// in JSON Schema. Keys count even when their stored values are zero.
//
// Example:
//
//	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
//		entries.Len(2)
//	})
//	_ = schema
func (entries *MapBuilder[K, V]) Len(size int) *MapBuilder[K, V] {
	entries.base = entries.addRule(func(_ context.Context, values map[K]V) error {
		if len(values) == size {
			return nil
		}

		return Fail("map.len", map[string]any{"len": size})
	}).annotate("minProperties", size).annotate("maxProperties", size)

	return entries
}

// Contains observes existence of a matching collection entry. Slices require
// K to be exact int; maps require exact K,V. It emits no validation errors.
//
// Example:
//
//	var positive validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.Contains("Scores", func(index, value int) bool {
//			return value > 0
//		})
//	}
//
//	_ = positive
func (predicate *ConditionBuilder) Contains[K comparable, V any](path string, match func(K, V) bool) *ConditionBuilder {
	validatePredicate(match)

	return predicate.add(collectionObservation(path, func(entries iter.Seq2[K, V]) bool {
		return containsEntry(entries, match)
	}))
}

// Every observes universal matching, including present empty collections.
// Nil collections use empty iteration; unreachable nested paths return false.
// Match must be pure and must not mutate observed values.
//
// Example:
//
//	var positive validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.Every("Scores", func(index, value int) bool {
//			return value > 0
//		})
//	}
//
//	_ = positive
func (predicate *ConditionBuilder) Every[K comparable, V any](path string, match func(K, V) bool) *ConditionBuilder {
	validatePredicate(match)

	return predicate.add(collectionObservation(path, func(entries iter.Seq2[K, V]) bool {
		return everyEntry(entries, match)
	}))
}

// UniqueBy observes projection uniqueness without emitting diagnostics. Exact
// key/value types are checked at construction and projections must be pure.
//
// Example:
//
//	var distinct validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.UniqueBy("Scores", func(index, value int) int {
//			return value
//		})
//	}
//
//	_ = distinct
func (predicate *ConditionBuilder) UniqueBy[K comparable, V any, U comparable](path string, project func(K, V) U) *ConditionBuilder {
	if project == nil {
		invalidf("UniqueBy projection is nil")
	}

	return predicate.add(collectionObservation(path, func(entries iter.Seq2[K, V]) bool {
		return uniqueEntries(entries, project)
	}))
}

// CountWhere observes pure numeric constraints on the matching entry count.
// Zero is checked as present, and invalid constraint settings fail construction.
//
// Example:
//
//	var enough validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.CountWhere("Scores", func(index, value int) bool {
//			return value > 0
//		}, func(count *validate.IntBuilder[int]) {
//			count.Min(2)
//		})
//	}
//
//	_ = enough
func (predicate *ConditionBuilder) CountWhere[K comparable, V any](path string, match func(K, V) bool, configure ...IntBuilderFunc[int]) *ConditionBuilder {
	validatePredicate(match)

	constraint := countConstraint(configure)

	return predicate.add(collectionObservation(path, func(entries iter.Seq2[K, V]) bool {
		return countHolds(constraint, matchingCount(entries, match))
	}))
}

// Has observes exact map-key existence, even when the entry's value is zero.
// Its key type is inferred and checked when the enclosing schema is built.
//
// Example:
//
//	var admin validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.Has("Scores", "admin")
//	}
//	_ = admin
func (predicate *ConditionBuilder) Has[K comparable](path string, key K) *ConditionBuilder {
	return predicate.add(keyObservation(path, []K{key}, func(count, total int) bool {
		return count == total
	}))
}

// HasAny observes whether any listed map key exists. Empty lists return false;
// caller-owned key slices are copied before the observation is retained.
//
// Example:
//
//	var staff validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.HasAny("Scores", "admin", "editor")
//	}
//	_ = staff
func (predicate *ConditionBuilder) HasAny[K comparable](path string, keys ...K) *ConditionBuilder {
	return predicate.add(keyObservation(path, keys, func(count, total int) bool {
		return count > 0
	}))
}

// HasAll observes whether every map key exists, independent of stored values.
// An empty list is true even for nil maps. Unreachable nested paths are false.
//
// Example:
//
//	var complete validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.HasAll("Scores", "name", "age")
//	}
//	_ = complete
func (predicate *ConditionBuilder) HasAll[K comparable](path string, keys ...K) *ConditionBuilder {
	return predicate.add(keyObservation(path, keys, func(count, total int) bool {
		return count == total
	}))
}
