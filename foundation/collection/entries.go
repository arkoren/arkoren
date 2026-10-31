package collection

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"iter"
)

// TryLazyMap is a lazily-evaluated map collection backed by an [iter.Seq2].
//
// The yielded value must be ignored whenever the yielded error is non-nil.
// A nil zero value cannot be traversed. Each traversal invokes the retained
// source anew, so replayability, cancellation and cleanup belong to the source.
// Continuing after an error resumes the source if it supports continuation.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Try()
//	for entry, err := range stock {
//		if err != nil {
//			panic(err)
//		}
//		fmt.Println(entry.Key, entry.Value) // apples 3
//	}
type TryLazyMap[K comparable, V any] func(yield func(MapEntry[K, V], error) bool)

// NewTryLazyMap creates a new [TryLazyMap] from the given iterator function.
// It retains f and its captures without invoking them. Nil f panics when
// traversed. The source must stop after yield returns false and release its
// resources; any blocking work must observe source-owned cancellation.
//
// Example:
//
//	stock := collection.NewTryLazyMap(func(yield func(collection.MapEntry[string, int], error) bool) {
//		yield(collection.NewMapEntry("apples", 3), nil)
//	})
//	items, err := stock.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items["apples"]) // 3
func NewTryLazyMap[K comparable, V any](f func(yield func(MapEntry[K, V], error) bool)) TryLazyMap[K, V] {
	return TryLazyMap[K, V](f)
}

// Eager materializes successful entries into an eager [Map].
//
// It stops on the first error and returns the entries collected before that error.
// The result owns outer map storage and shallowly copies values; the last
// successful duplicate key before the error wins.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().Eager()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock.Items()["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) Eager() (Map[K, V], error) {
	items, err := tryLazyMap.Items()

	return NewMap(items), err
}

// EagerAll materializes successful entries into an eager [Map].
//
// It consumes the entire sequence and joins all errors with [errors.Join].
// The last successful duplicate key wins; outer storage is independently owned.
// It requires a finite source and cannot recover a terminal source failure.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().EagerAll()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock.Items()["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) EagerAll() (Map[K, V], error) {
	items, err := tryLazyMap.ItemsAll()

	return NewMap(items), err
}

// Items materializes successful entries from the iterator into a Go map.
//
// It stops on the first error and returns the entries collected before that error.
// It always returns an allocated outer map with shallow values. The last
// successful duplicate key wins; the original source error is returned unchanged.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) Items() (map[K]V, error) {
	items := make(map[K]V)

	for entry, err := range tryLazyMap {
		if err != nil {
			return items, err
		}

		items[entry.Key] = entry.Value
	}

	return items, nil
}

// ItemsAll materializes successful entries from the iterator into a Go map.
//
// It consumes the entire sequence and joins all errors with [errors.Join].
// Unsuccessful entries are discarded. Outer storage is independently allocated
// even for empty input, and the last successful duplicate key wins.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().ItemsAll()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) ItemsAll() (map[K]V, error) {
	items := make(map[K]V)
	var errs []error

	for entry, err := range tryLazyMap {
		if err != nil {
			errs = append(errs, err)

			continue
		}

		items[entry.Key] = entry.Value
	}

	return items, errors.Join(errs...)
}

// ItemsAs materializes successful entries into the given map type.
//
// It stops on the first error and returns the entries collected before that error.
// The requested map owns newly allocated outer storage with shallow values.
// Duplicate keys keep the last successful value before the failure.
//
// Example:
//
//	type Stock map[string]int
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().ItemsAs[Stock]()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) ItemsAs[M ~map[K]V]() (M, error) {
	items := make(M)

	for entry, err := range tryLazyMap {
		if err != nil {
			return items, err
		}

		items[entry.Key] = entry.Value
	}

	return items, nil
}

// ItemsAsAll materializes successful entries into the given map type.
//
// It consumes the entire sequence and joins all errors with [errors.Join].
// Like ItemsAll, it returns an independently allocated map, including for no
// successful entries, and retains the last successful duplicate value.
//
// Example:
//
//	type Stock map[string]int
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().ItemsAsAll[Stock]()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) ItemsAsAll[M ~map[K]V]() (M, error) {
	items := make(M)
	var errs []error

	for entry, err := range tryLazyMap {
		if err != nil {
			errs = append(errs, err)

			continue
		}

		items[entry.Key] = entry.Value
	}

	return items, errors.Join(errs...)
}

// Iter returns the underlying iterator over entries and errors.
// It consumes nothing and preserves source order, duplicates and replayability.
// Ignore the entry whenever its accompanying error is non-nil.
//
// Example:
//
//	for entry, err := range collection.NewMap(map[string]int{"apples": 3}).Try().Iter() {
//		if err != nil {
//			panic(err)
//		}
//		fmt.Println(entry.Key, entry.Value) // apples 3
//	}
func (tryLazyMap TryLazyMap[K, V]) Iter() iter.Seq2[MapEntry[K, V], error] {
	return iter.Seq2[MapEntry[K, V], error](tryLazyMap)
}

// IsEmpty reports whether the iterator yields no successful entries.
// It stops at the first result: an error returns false and that error; one
// successful entry returns false and nil. An exhausted source returns true.
//
// Example:
//
//	empty, err := collection.NewMap[string, int](nil).Try().IsEmpty()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(empty) // true
func (tryLazyMap TryLazyMap[K, V]) IsEmpty() (bool, error) {
	for _, err := range tryLazyMap {
		if err != nil {
			return false, err
		}

		return false, nil
	}

	return true, nil
}

// IsNotEmpty reports whether the iterator yields at least one successful entry.
// It stops at the first result. Check the error before using the boolean; an
// initial error returns true with that error, not proof of a successful entry.
//
// Example:
//
//	found, err := collection.NewMap(map[string]int{"apples": 3}).Try().IsNotEmpty()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(found) // true
func (tryLazyMap TryLazyMap[K, V]) IsNotEmpty() (bool, error) {
	result, err := tryLazyMap.IsEmpty()

	return !result, err
}

// Len returns the total number of successful entries yielded by the iterator.
// It consumes the source until exhaustion or the first error, returning the
// successful prefix count on failure. Duplicate keys count separately.
//
// Example:
//
//	count, err := collection.NewMap(map[string]int{"apples": 3}).Try().Len()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(count) // 1
func (tryLazyMap TryLazyMap[K, V]) Len() (int, error) {
	count := 0

	for _, err := range tryLazyMap {
		if err != nil {
			return count, err
		}

		count++
	}

	return count, nil
}

// Every reports whether f returns true for every successful entry.
// It stops at the first false result or source/callback error. Errors return
// false with the unchanged cause; an empty source returns true and nil.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Try()
//	positive, err := stock.Every(func(_ string, value int) (bool, error) {
//		return value > 0, nil
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(positive) // true
func (tryLazyMap TryLazyMap[K, V]) Every(f func(K, V) (bool, error)) (bool, error) {
	for entry, err := range tryLazyMap {
		matched, err := tryLazyMapMatch(entry, err, f)

		if err != nil {
			return false, err
		}

		if !matched {
			return false, nil
		}
	}

	return true, nil
}

// Each calls f for every successful entry.
// It consumes now and stops at the first source or callback error, returning it
// unchanged. Source failures never invoke f; callback panics propagate.
//
// Example:
//
//	err := collection.NewMap(map[string]int{"apples": 3}).Try().Each(func(key string, value int) error {
//		fmt.Println(key, value) // apples 3
//		return nil
//	})
//	if err != nil {
//		panic(err)
//	}
func (tryLazyMap TryLazyMap[K, V]) Each(f func(K, V) error) error {
	for entry, err := range tryLazyMap {
		if err != nil {
			return err
		}

		if err := f(entry.Key, entry.Value); err != nil {
			return err
		}
	}

	return nil
}

// EachAll calls f for every successful entry, joining all errors.
// It consumes the finite source, skips f for source failures and joins both
// callback and source errors. An error cannot make a terminal source resume.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Try()
//	err := stock.EachAll(func(key string, value int) error {
//		fmt.Println(key, value) // apples 3
//		return nil
//	})
//	if err != nil {
//		panic(err)
//	}
func (tryLazyMap TryLazyMap[K, V]) EachAll(f func(K, V) error) error {
	var errs []error

	for entry, err := range tryLazyMap {
		if err != nil {
			errs = append(errs, err)

			continue
		}

		if err := f(entry.Key, entry.Value); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// TapEach returns a lazy map that calls f on each successful entry as it is consumed.
// Source errors bypass f; source and callback errors are forwarded, replacing
// the failed entry. Continuing iteration continues with the next source result.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Try()
//	observed := stock.TapEach(func(key string, value int) error {
//		fmt.Println(key, value) // apples 3, only during Items
//		return nil
//	})
//	if _, err := observed.Items(); err != nil {
//		panic(err)
//	}
func (tryLazyMap TryLazyMap[K, V]) TapEach(f func(K, V) error) TryLazyMap[K, V] {
	return NewTryLazyMap(func(yield func(MapEntry[K, V], error) bool) {
		tryLazyMapTapEach(tryLazyMap, f, yield)
	})
}

// Contains reports whether any successful entry satisfies f.
// It consumes now, stopping at a match or the first source/callback error.
// Empty input returns false and nil; failure returns false and the original error.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Try()
//	found, err := stock.Contains(func(_ string, value int) (bool, error) {
//		return value == 3, nil
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(found) // true
func (tryLazyMap TryLazyMap[K, V]) Contains(f func(K, V) (bool, error)) (bool, error) {
	for entry, err := range tryLazyMap {
		matched, err := tryLazyMapMatch(entry, err, f)

		if err != nil {
			return false, err
		}

		if matched {
			return true, nil
		}
	}

	return false, nil
}

// HasAny reports whether any of the given keys exists in the lazy map.
// It stops at a match or the first source error. No requested keys returns
// false and nil without invoking the source, so it observes no source errors.
//
// Example:
//
//	found, err := collection.NewMap(map[string]int{"apples": 0}).Try().HasAny("apples")
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(found) // true
func (tryLazyMap TryLazyMap[K, V]) HasAny(keys ...K) (bool, error) {
	if len(keys) == 0 {
		return false, nil
	}

	return tryLazyMapHasAny(tryLazyMap, mapKeySet(keys))
}

// Filter returns a lazy map containing only successful entries for which f returns true.
// Source errors bypass f. Source and predicate errors are forwarded in order,
// rather than filtered out; continued iteration can yield later successes.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3, "pears": 0}).Try()
//	available := stock.Filter(func(_ string, value int) (bool, error) {
//		return value > 0, nil
//	})
//	items, err := available.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) Filter(f func(K, V) (bool, error)) TryLazyMap[K, V] {
	return NewTryLazyMap(func(yield func(MapEntry[K, V], error) bool) {
		tryLazyMapFilter(tryLazyMap, f, yield)
	})
}

// Reject returns a lazy map containing only successful entries for which f returns false.
// It inverts only successful predicate results. Source and predicate errors are
// forwarded without inversion or loss, with continuation controlled by consumers.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3, "pears": 0}).Try()
//	available := stock.Reject(func(_ string, value int) (bool, error) {
//		return value == 0, nil
//	})
//	items, err := available.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) Reject(f func(K, V) (bool, error)) TryLazyMap[K, V] {
	return tryLazyMap.Filter(func(k K, v V) (bool, error) {
		ok, err := f(k, v)

		if err != nil {
			return false, err
		}

		return !ok, nil
	})
}

// Only returns a lazy map containing only the entries whose keys were given.
// It captures the requested keys now, then filters during iteration. All source
// errors are forwarded, including errors that carry an unrequested key.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().Only("apples").Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) Only(keys ...K) TryLazyMap[K, V] {
	wanted := make(map[K]struct{}, len(keys))

	for _, key := range keys {
		wanted[key] = struct{}{}
	}

	return tryLazyMap.Filter(func(k K, v V) (bool, error) {
		_, ok := wanted[k]

		return ok, nil
	})
}

// Except returns a lazy map containing all entries except those whose keys were given.
// It captures the requested keys now. Source errors are always forwarded,
// including errors carrying a key that would otherwise be excluded.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().Except("pears").Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) Except(keys ...K) TryLazyMap[K, V] {
	blocked := make(map[K]struct{}, len(keys))

	for _, key := range keys {
		blocked[key] = struct{}{}
	}

	return tryLazyMap.Filter(func(k K, v V) (bool, error) {
		_, ok := blocked[k]

		return !ok, nil
	})
}

// MapValues transforms values using f and returns a new lazy map with the same keys.
// Source errors bypass f; source and callback errors replace failed entries and
// are forwarded. Successful duplicate keys remain until materialization.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Try()
//	labels := stock.MapValues(func(_ string, value int) (string, error) {
//		return strconv.Itoa(value), nil
//	})
//	items, err := labels.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) MapValues[W any](f func(K, V) (W, error)) TryLazyMap[K, W] {
	return NewTryLazyMap(func(yield func(MapEntry[K, W], error) bool) {
		tryLazyMapMapValues(tryLazyMap, f, yield)
	})
}

// MapKeys transforms keys using f and returns a new lazy map with the same values.
// Source errors bypass f; callback errors replace the failed entry. Errors are
// forwarded in order, and materialization retains the last successful duplicate.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Try()
//	upper := stock.MapKeys(func(key string, _ int) (string, error) {
//		return strings.ToUpper(key), nil
//	})
//	items, err := upper.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items["APPLES"]) // 3
func (tryLazyMap TryLazyMap[K, V]) MapKeys[J comparable](f func(K, V) (J, error)) TryLazyMap[J, V] {
	return NewTryLazyMap(func(yield func(MapEntry[J, V], error) bool) {
		tryLazyMapMapKeys(tryLazyMap, f, yield)
	})
}

// LazyKeys returns an iterator yielding all keys and errors.
// It consumes nothing until iteration and preserves duplicates and source order.
// Ignore a yielded key whenever its accompanying error is non-nil.
//
// Example:
//
//	for key, err := range collection.NewMap(map[string]int{"apples": 3}).Try().LazyKeys() {
//		if err != nil {
//			panic(err)
//		}
//		fmt.Println(key) // apples
//	}
func (tryLazyMap TryLazyMap[K, V]) LazyKeys() iter.Seq2[K, error] {
	return func(yield func(K, error) bool) {
		tryLazyMapKeys(tryLazyMap, yield)
	}
}

// LazyValues returns an iterator yielding all values and errors.
// It preserves source order and yields shallow values only during iteration.
// Ignore a yielded value whenever its accompanying error is non-nil.
//
// Example:
//
//	for value, err := range collection.NewMap(map[string]int{"apples": 3}).Try().LazyValues() {
//		if err != nil {
//			panic(err)
//		}
//		fmt.Println(value) // 3
//	}
func (tryLazyMap TryLazyMap[K, V]) LazyValues() iter.Seq2[V, error] {
	return func(yield func(V, error) bool) {
		tryLazyMapValues(tryLazyMap, yield)
	}
}

// Take returns a lazy map that yields at most n successful entries.
// Nonpositive n consumes nothing, including no errors. Positive n forwards
// errors without counting them and stops after the nth success; duplicates count
// separately. Continued iteration after an error resumes the same limit.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().Take(1).Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) Take(n int) TryLazyMap[K, V] {
	return NewTryLazyMap(func(yield func(MapEntry[K, V], error) bool) {
		tryLazyMapTake(tryLazyMap, n, yield)
	})
}

// Skip returns a lazy map that skips the first n successful entries.
// Nonpositive n skips nothing. Errors are always forwarded and do not consume
// skipped positions; duplicate keys count separately. Work runs during iteration.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().Skip(1).Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(len(stock)) // 0
func (tryLazyMap TryLazyMap[K, V]) Skip(n int) TryLazyMap[K, V] {
	return NewTryLazyMap(func(yield func(MapEntry[K, V], error) bool) {
		tryLazyMapSkip(tryLazyMap, n, yield)
	})
}

// Unique returns a lazy map that yields only the first entry for each key.
// Only successful entries populate its per-traversal seen set. Errors are
// forwarded and never suppress a later successful entry's key.
//
// Example:
//
//	first := collection.NewMap(map[string]int{"apples": 3}).Try()
//	last := collection.NewMap(map[string]int{"apples": 4}).Try()
//	items, err := first.Concat(last).Unique().Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items["apples"]) // 3
func (tryLazyMap TryLazyMap[K, V]) Unique() TryLazyMap[K, V] {
	return NewTryLazyMap(func(yield func(MapEntry[K, V], error) bool) {
		tryLazyMapUnique(tryLazyMap, yield)
	})
}

// Concat returns a lazy map that yields all entries from this map followed by other.
// Source errors are forwarded without ending concatenation if the consumer
// continues. Stopping in the first source prevents invoking the second.
//
// Example:
//
//	first := collection.NewMap(map[string]int{"apples": 3}).Try()
//	last := collection.NewMap(map[string]int{"apples": 4}).Try()
//	items, err := first.Concat(last).Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items["apples"]) // 4
func (tryLazyMap TryLazyMap[K, V]) Concat(other TryLazyMap[K, V]) TryLazyMap[K, V] {
	return NewTryLazyMap(func(yield func(MapEntry[K, V], error) bool) {
		if !yieldTryLazyMap(tryLazyMap, yield) {
			return
		}

		yieldTryLazyMap(other, yield)
	})
}

// tryLazyMapMatch applies f to a successful entry or returns its iterator error.
// Source errors bypass the callback. Callback errors are returned unchanged with
// false, so terminal predicates can stop without losing the original cause.
func tryLazyMapMatch[K comparable, V any](entry MapEntry[K, V], err error, f func(K, V) (bool, error)) (bool, error) {
	if err != nil {
		return false, err
	}

	matched, err := f(entry.Key, entry.Value)

	if err != nil {
		return false, err
	}

	return matched, nil
}

// tryLazyMapTapEach applies f and yields every iterator result.
// Errors replace the unsuccessful entry; continued consumption can observe later
// successes. Downstream refusal ends traversal immediately.
func tryLazyMapTapEach[K comparable, V any](
	tryLazyMap TryLazyMap[K, V],
	f func(K, V) error,
	yield func(MapEntry[K, V], error) bool,
) {
	for entry, err := range tryLazyMap {
		if !tryLazyMapTapEachItem(entry, err, f, yield) {
			return
		}
	}
}

// tryLazyMapTapEachItem applies f to one successful entry.
// Source errors bypass f and callback errors suppress the entry's value. Its
// result is the downstream continuation signal, including for error results.
func tryLazyMapTapEachItem[K comparable, V any](
	entry MapEntry[K, V],
	err error,
	f func(K, V) error,
	yield func(MapEntry[K, V], error) bool,
) bool {
	if err != nil {
		return tryYieldMapError(yield, err)
	}

	err = f(entry.Key, entry.Value)

	if err != nil {
		return tryYieldMapError(yield, err)
	}

	return yield(entry, nil)
}

// tryLazyMapHasAny reports whether tryLazyMap contains any key in wanted.
// It stops at the first matching successful entry or source error; failed
// entries never participate in membership checks.
func tryLazyMapHasAny[K comparable, V any](tryLazyMap TryLazyMap[K, V], wanted map[K]struct{}) (bool, error) {
	for entry, err := range tryLazyMap {
		if err != nil {
			return false, err
		}

		if _, ok := wanted[entry.Key]; ok {
			return true, nil
		}
	}

	return false, nil
}

// tryLazyMapFilter yields entries accepted by f and iterator errors.
// Predicate failures are forwarded, not interpreted as false matches. Refusal
// of either an entry or an error stops source consumption immediately.
func tryLazyMapFilter[K comparable, V any](
	tryLazyMap TryLazyMap[K, V],
	f func(K, V) (bool, error),
	yield func(MapEntry[K, V], error) bool,
) {
	for entry, err := range tryLazyMap {
		if !tryLazyMapFilterItem(entry, err, f, yield) {
			return
		}
	}
}

// tryLazyMapFilterItem handles one iterator result for tryLazyMapFilter.
// A successful nonmatch asks upstream to continue without yielding. Source and
// predicate errors instead propagate the consumer's error-continuation decision.
func tryLazyMapFilterItem[K comparable, V any](
	entry MapEntry[K, V],
	err error,
	f func(K, V) (bool, error),
	yield func(MapEntry[K, V], error) bool,
) bool {
	if err != nil {
		return tryYieldMapError(yield, err)
	}

	matched, err := f(entry.Key, entry.Value)

	if err != nil {
		return tryYieldMapError(yield, err)
	}

	if !matched {
		return true
	}

	return yield(entry, nil)
}

// tryLazyMapMapValues transforms successful entry values and yields iterator errors.
// Source errors bypass the conversion callback; callback errors suppress its
// result. Keys, ordering and duplicates survive successful conversions unchanged.
func tryLazyMapMapValues[K comparable, V, W any](
	tryLazyMap TryLazyMap[K, V],
	f func(K, V) (W, error),
	yield func(MapEntry[K, W], error) bool,
) {
	for entry, err := range tryLazyMap {
		if !tryLazyMapMapValue(entry, err, f, yield) {
			return
		}
	}
}

// tryLazyMapMapValue transforms and yields one iterator result.
// It forwards either error without inspecting or publishing a failed value.
// Successful output retains the original key and downstream continuation signal.
func tryLazyMapMapValue[K comparable, V, W any](
	entry MapEntry[K, V],
	err error,
	f func(K, V) (W, error),
	yield func(MapEntry[K, W], error) bool,
) bool {
	if err != nil {
		return tryYieldMapError(yield, err)
	}

	value, err := f(entry.Key, entry.Value)

	if err != nil {
		return tryYieldMapError(yield, err)
	}

	return yield(NewMapEntry(entry.Key, value), nil)
}

// tryLazyMapMapKeys transforms successful entry keys and yields iterator errors.
// Source errors bypass the callback. Successful collisions remain in the stream;
// the consumer chooses whether to deduplicate or keep the last materialized key.
func tryLazyMapMapKeys[K, J comparable, V any](
	tryLazyMap TryLazyMap[K, V],
	f func(K, V) (J, error),
	yield func(MapEntry[J, V], error) bool,
) {
	for entry, err := range tryLazyMap {
		if !tryLazyMapMapKey(entry, err, f, yield) {
			return
		}
	}
}

// tryLazyMapMapKey transforms and yields one iterator result.
// Callback errors suppress the unsuccessful key. Successful output shallowly
// retains the original value and propagates the consumer's continuation signal.
func tryLazyMapMapKey[K, J comparable, V any](
	entry MapEntry[K, V],
	err error,
	f func(K, V) (J, error),
	yield func(MapEntry[J, V], error) bool,
) bool {
	if err != nil {
		return tryYieldMapError(yield, err)
	}

	key, err := f(entry.Key, entry.Value)

	if err != nil {
		return tryYieldMapError(yield, err)
	}

	return yield(NewMapEntry(key, entry.Value), nil)
}

// tryLazyMapKeys yields successful keys and iterator errors.
// Duplicate keys and source order are retained. Stopping on an error or key stops
// the source without consuming a later result.
func tryLazyMapKeys[K comparable, V any](tryLazyMap TryLazyMap[K, V], yield func(K, error) bool) {
	for entry, err := range tryLazyMap {
		if !tryLazyMapKeyItem(entry, err, yield) {
			return
		}
	}
}

// tryLazyMapKeyItem yields one iterator result as a key.
// An error is paired with the zero K rather than exposing an unsuccessful entry;
// the consumer's continuation decision is returned unchanged.
func tryLazyMapKeyItem[K comparable, V any](entry MapEntry[K, V], err error, yield func(K, error) bool) bool {
	if err != nil {
		return tryYieldError(yield, err)
	}

	return yield(entry.Key, nil)
}

// tryLazyMapValues yields successful values and iterator errors.
// Values remain shallow copies in source order. Source errors are forwarded
// without publishing their accompanying entry values.
func tryLazyMapValues[K comparable, V any](tryLazyMap TryLazyMap[K, V], yield func(V, error) bool) {
	for entry, err := range tryLazyMap {
		if !tryLazyMapValueItem(entry, err, yield) {
			return
		}
	}
}

// tryLazyMapValueItem yields one iterator result as a value.
// Errors are paired with the zero V, and downstream refusal of either result
// stops the enclosing traversal immediately.
func tryLazyMapValueItem[K comparable, V any](entry MapEntry[K, V], err error, yield func(V, error) bool) bool {
	if err != nil {
		return tryYieldError(yield, err)
	}

	return yield(entry.Value, nil)
}

// tryLazyMapTake yields no more than n successful entries from tryLazyMap.
// Nonpositive n does not invoke the source. Otherwise source errors are forwarded
// without consuming the successful-entry budget.
func tryLazyMapTake[K comparable, V any](tryLazyMap TryLazyMap[K, V], n int, yield func(MapEntry[K, V], error) bool) {
	if n <= 0 {
		return
	}

	tryLazyMapTakePositive(tryLazyMap, n, yield)
}

// tryLazyMapTakePositive yields exactly up to a positive successful-entry limit.
// The caller validates n. The source is stopped after the last selected success,
// even when downstream accepts it; errors do not decrement n.
func tryLazyMapTakePositive[K comparable, V any](
	tryLazyMap TryLazyMap[K, V],
	n int,
	yield func(MapEntry[K, V], error) bool,
) {
	tryLazyMap(func(entry MapEntry[K, V], err error) bool {
		if err != nil {
			return tryYieldMapError(yield, err)
		}

		n--

		return yield(entry, nil) && n > 0
	})
}

// tryLazyMapSkip skips n successful entries and yields all iterator errors.
// Nonpositive n skips nothing. Duplicate keys count as separate positions and
// errors never consume a skipped position.
func tryLazyMapSkip[K comparable, V any](tryLazyMap TryLazyMap[K, V], n int, yield func(MapEntry[K, V], error) bool) {
	tryLazyMap(func(entry MapEntry[K, V], err error) bool {
		if err != nil {
			return tryYieldMapError(yield, err)
		}

		if n > 0 {
			n--

			return true
		}

		return yield(entry, nil)
	})
}

// tryLazyMapUnique yields the first successful entry for each key.
// The seen-key set belongs to this traversal. Source errors do not occupy keys,
// so later successful entries remain eligible after consumers accept an error.
func tryLazyMapUnique[K comparable, V any](tryLazyMap TryLazyMap[K, V], yield func(MapEntry[K, V], error) bool) {
	seen := make(map[K]struct{})

	for entry, err := range tryLazyMap {
		if !tryLazyMapUniqueItem(entry, err, seen, yield) {
			return
		}
	}
}

// tryLazyMapUniqueItem handles one iterator result while removing duplicate keys.
// Errors are forwarded before inspecting the key. A successful duplicate asks
// upstream to continue without invoking yield or replacing its first value.
func tryLazyMapUniqueItem[K comparable, V any](
	entry MapEntry[K, V],
	err error,
	seen map[K]struct{},
	yield func(MapEntry[K, V], error) bool,
) bool {
	if err != nil {
		return tryYieldMapError(yield, err)
	}

	if _, exists := seen[entry.Key]; exists {
		return true
	}

	seen[entry.Key] = struct{}{}

	return yield(entry, nil)
}

// yieldTryLazyMap passes iterator results from tryLazyMap to yield.
// It returns false only on downstream refusal. Accepting an error allows the
// source and eventual concatenated source to continue normally.
func yieldTryLazyMap[K comparable, V any](tryLazyMap TryLazyMap[K, V], yield func(MapEntry[K, V], error) bool) bool {
	for entry, err := range tryLazyMap {
		if !yield(entry, err) {
			return false
		}
	}

	return true
}

// MarshalJSONTo materializes the iterator and encodes it as a JSON object into enc.
// It stops at the first source error before writing any entries and returns that
// error unchanged. Otherwise JSON encoding and writer errors are returned.
//
// Example:
//
//	var encoded bytes.Buffer
//	stock := collection.NewMap(map[string]int{"apples": 3}).Try()
//	if err := stock.MarshalJSONTo(jsontext.NewEncoder(&encoded)); err != nil {
//		panic(err)
//	}
//	fmt.Println(encoded.Len() > 0) // true
func (tryLazyMap TryLazyMap[K, V]) MarshalJSONTo(enc *jsontext.Encoder) error {
	items, err := tryLazyMap.Items()

	if err != nil {
		return err
	}

	return json.MarshalEncode(enc, items)
}

// UnmarshalJSONFrom decodes a JSON object from dec into the lazy map.
// Errors leave the previous source unchanged. Success installs a replayable
// map-backed source, with unspecified iteration order and nil source errors.
//
// Example:
//
//	var stock collection.TryLazyMap[string, int]
//	err := stock.UnmarshalJSONFrom(jsontext.NewDecoder(strings.NewReader(`{"apples":3}`)))
//	if err != nil {
//		panic(err)
//	}
//	items, err := stock.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items["apples"]) // 3
func (tryLazyMap *TryLazyMap[K, V]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var items map[K]V

	if err := json.UnmarshalDecode(dec, &items); err != nil {
		return err
	}

	*tryLazyMap = NewMap(items).Try()

	return nil
}

// MarshalJSON implements [encoding/json.Marshaler] for v1 compatibility.
// It consumes the source now, stopping at the first error with no encoded result.
// Otherwise it uses JSON v2 defaults and returns any encoding error.
//
// Example:
//
//	data, err := collection.NewMap(map[string]int{"apples": 3}).Try().MarshalJSON()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(string(data)) // {"apples":3}
func (tryLazyMap TryLazyMap[K, V]) MarshalJSON() ([]byte, error) {
	items, err := tryLazyMap.Items()

	if err != nil {
		return nil, err
	}

	return json.Marshal(items)
}

// UnmarshalJSON implements [encoding/json.Unmarshaler] for v1 compatibility.
// It uses JSON v2 decoding and replaces the source only on success. Successful
// decoding installs a replayable source with nil errors and unspecified map order.
//
// Example:
//
//	var stock collection.TryLazyMap[string, int]
//	if err := stock.UnmarshalJSON([]byte(`{"apples":3}`)); err != nil {
//		panic(err)
//	}
//	items, err := stock.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items["apples"]) // 3
func (tryLazyMap *TryLazyMap[K, V]) UnmarshalJSON(data []byte) error {
	var items map[K]V

	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}

	*tryLazyMap = NewMap(items).Try()

	return nil
}

// tryYieldMapError yields err with the zero value of MapEntry.
// This prevents publishing an unsuccessful key or value. Its boolean result is
// the consumer's decision whether iteration can continue after the error.
func tryYieldMapError[K comparable, V any](yield func(MapEntry[K, V], error) bool, err error) bool {
	var zero MapEntry[K, V]

	return yield(zero, err)
}
