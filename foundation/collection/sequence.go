package collection

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"iter"
	"slices"
)

// LazySlice is a lazily-evaluated collection backed by an [iter.Seq2] of
// index and value pairs.
//
// Callbacks receive the index from this slice's source sequence. LazySlice
// transformations yield dense output indices, starting at zero, even when
// they filter, expand, reorder, or combine source items.
// Its nil zero value cannot be traversed. Each traversal invokes the source
// again, so replayability and resource ownership belong to the source.
//
// Example:
//
//	items := collection.NewSlice([]int{4, 5}).Lazy()
//	for index, value := range items {
//		fmt.Println(index, value) // 0 4, then 1 5
//	}
type LazySlice[T any] func(yield func(int, T) bool)

// NewLazySlice creates a new [LazySlice] from the given iterator function.
// It retains f and its captures without consuming anything. A nil f is not an
// empty iterator and panics on traversal. Sources must stop after yield is false
// and release their own resources; blocking sources must observe cancellation.
//
// Example:
//
//	items := collection.NewLazySlice(slices.All([]int{4, 5}))
//	fmt.Println(items.Items()) // [4 5]
func NewLazySlice[T any](f func(yield func(int, T) bool)) LazySlice[T] {
	return LazySlice[T](f)
}

// Eager materializes the lazy slice into an eager [Slice].
// It consumes the whole finite source now and owns independent shallow storage.
// No values produces a nil Slice; subsequent eager reads do not rerun the source.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2}).Lazy().Eager()
//	fmt.Println(items.Items()) // [1 2]
func (lazySlice LazySlice[T]) Eager() Slice[T] {
	return NewSlice(lazySlice.Items())
}

// Items materializes all items from the iterator into a Go slice.
// It consumes the finite source now and returns newly owned shallow storage,
// or nil if no items are yielded. Calling it again traverses the source again.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2}).Lazy().Items()) // [1 2]
func (lazySlice LazySlice[T]) Items() []T {
	var items []T

	for _, v := range lazySlice {
		items = append(items, v)
	}

	return items
}

// ItemsAs materializes all items from the iterator into the given slice type.
// Like Items, it consumes the finite source and owns the shallow result storage.
// No yielded values returns a nil value of the requested slice type.
//
// Example:
//
//	type Numbers []int
//	items := collection.NewSlice([]int{1, 2}).Lazy().ItemsAs[Numbers]()
//	fmt.Println(items) // [1 2]
func (lazySlice LazySlice[T]) ItemsAs[S ~[]T]() S {
	var items S

	for _, v := range lazySlice {
		items = append(items, v)
	}

	return items
}

// Iter returns the underlying iterator over slice indices and items.
// It consumes nothing, retains source indices and preserves the source's
// replayability; a nil receiver yields a nil iterator.
//
// Example:
//
//	for index, value := range collection.NewSlice([]int{4, 5}).Lazy().Iter() {
//		fmt.Println(index, value) // 0 4, then 1 5
//	}
func (lazySlice LazySlice[T]) Iter() iter.Seq2[int, T] {
	return iter.Seq2[int, T](lazySlice)
}

// Try returns a [TryLazySlice] backed by the items in the lazy slice.
// Conversion consumes nothing. It drops source indices and yields values with
// nil errors; the source's replayability and captures remain unchanged.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2}).Lazy().Try().Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (lazySlice LazySlice[T]) Try() TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		for _, v := range lazySlice {
			if !yield(v, nil) {
				return
			}
		}
	})
}

// IsEmpty reports whether the iterator yields no items.
// It consumes at most the first item and stops upstream immediately on a match.
// This observation can advance a single-use source.
//
// Example:
//
//	fmt.Println(collection.NewSlice[int](nil).Lazy().IsEmpty()) // true
func (lazySlice LazySlice[T]) IsEmpty() bool {
	for range lazySlice {
		return false
	}

	return true
}

// IsNotEmpty reports whether the iterator yields at least one item.
// It consumes at most one item, potentially advancing a single-use source.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1}).Lazy().IsNotEmpty()) // true
func (lazySlice LazySlice[T]) IsNotEmpty() bool {
	return !lazySlice.IsEmpty()
}

// Len returns the total number of items yielded by the iterator.
// It fully consumes the sequence.
// Duplicate values count separately. It does not cache or materialize the items.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 1}).Lazy().Len()) // 2
func (lazySlice LazySlice[T]) Len() int {
	count := 0

	for range lazySlice {
		count++
	}

	return count
}

// Every reports whether f returns true for every item yielded by the iterator.
// It stops at the first false result and returns true on an empty source.
// Callbacks run during this call with the source's indices.
//
// Example:
//
//	positive := collection.NewSlice([]int{1, 2}).Lazy().Every(func(_ int, value int) bool {
//		return value > 0
//	})
//	fmt.Println(positive) // true
func (lazySlice LazySlice[T]) Every(f func(int, T) bool) bool {
	for i, v := range lazySlice {
		if !f(i, v) {
			return false
		}
	}

	return true
}

// Each calls f for every item yielded by the iterator.
// It consumes the finite source now, synchronously, with its source indices.
// Callback panics propagate rather than becoming collection errors.
//
// Example:
//
//	collection.NewSlice([]int{4, 5}).Lazy().Each(func(index, value int) {
//		fmt.Println(index, value) // 0 4, then 1 5
//	})
func (lazySlice LazySlice[T]) Each(f func(int, T)) {
	for i, v := range lazySlice {
		f(i, v)
	}
}

// TapEach returns a lazy slice that calls f with each source index and item as
// it is consumed. The returned slice yields dense output indices.
// Construction consumes nothing; callback effects repeat if the source replays.
//
// Example:
//
//	total := 0
//	items := collection.NewSlice([]int{1, 2}).Lazy().TapEach(func(_ int, value int) {
//		total += value
//	})
//	fmt.Println(total) // 0
//	items.Each(func(_ int, _ int) {
//	})
//	fmt.Println(total) // 3
func (lazySlice LazySlice[T]) TapEach(f func(int, T)) LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		index := 0

		for i, v := range lazySlice {
			f(i, v)

			if !yield(index, v) {
				return
			}

			index++
		}
	})
}

// Filter returns a lazy slice containing only items for which f returns true.
// It calls f with source indices and yields dense output indices.
// The predicate runs only during iteration and no value is copied deeply.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2, 3}).Lazy().Filter(func(_ int, value int) bool {
//		return value%2 == 0
//	})
//	fmt.Println(items.Items()) // [2]
func (lazySlice LazySlice[T]) Filter(f func(int, T) bool) LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		index := 0

		for i, v := range lazySlice {
			if !f(i, v) {
				continue
			}

			if !yield(index, v) {
				return
			}

			index++
		}
	})
}

// Reject returns a lazy slice containing only items for which f returns false.
// It calls f with source indices and yields dense output indices.
// The predicate runs only when the returned sequence is consumed.
//
// Example:
//
//	odd := collection.NewSlice([]int{1, 2, 3}).Lazy().Reject(func(_ int, value int) bool {
//		return value%2 == 0
//	}).Items()
//	_ = odd // []int{1, 3}
func (lazySlice LazySlice[T]) Reject(f func(int, T) bool) LazySlice[T] {
	return lazySlice.Filter(func(i int, v T) bool {
		return !f(i, v)
	})
}

// Map transforms each item using f and returns a new lazy slice of type K.
// It calls f with source indices and yields dense output indices.
// Work runs during iteration and stops with the downstream consumer.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2}).Lazy().Map(func(_ int, value int) string {
//		return strconv.Itoa(value)
//	})
//	fmt.Println(items.Items()) // [1 2]
func (lazySlice LazySlice[T]) Map[K any](f func(int, T) K) LazySlice[K] {
	return NewLazySlice(func(yield func(int, K) bool) {
		index := 0

		for i, v := range lazySlice {
			if !yield(index, f(i, v)) {
				return
			}

			index++
		}
	})
}

// FlatMap transforms each item into zero or more items and yields the flattened
// results lazily. It calls f with source indices and yields dense output indices.
// Expansion order is retained; nil results add nothing. Only consumed expansions
// are visited, and the returned values are not copied deeply.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2}).Lazy().FlatMap(func(_ int, value int) []int {
//		return []int{value, value}
//	})
//	fmt.Println(items.Items()) // [1 1 2 2]
func (lazySlice LazySlice[T]) FlatMap[K any](f func(int, T) []K) LazySlice[K] {
	return NewLazySlice(func(yield func(int, K) bool) {
		index := 0

		lazySlice(func(i int, v T) bool {
			for _, mapped := range f(i, v) {
				if !yield(index, mapped) {
					return false
				}

				index++
			}

			return true
		})
	})
}

// KeyBy indexes yielded items by the key returned from f. When multiple items
// map to the same key, the last item wins.
// This aggregation consumes the whole finite source immediately and returns
// independently owned outer map storage with shallow values.
//
// Example:
//
//	items := collection.NewSlice([]string{"Ada"}).Lazy().KeyBy(func(_ int, value string) string {
//		return value
//	})
//	fmt.Println(items.Items()["Ada"]) // Ada
func (lazySlice LazySlice[T]) KeyBy[K comparable](f func(int, T) K) Map[K, T] {
	indexed := make(map[K]T)

	for i, v := range lazySlice {
		indexed[f(i, v)] = v
	}

	return NewMap(indexed)
}

// CountBy counts yielded items by the key returned from f.
// It consumes the finite source during this call and owns the returned map.
//
// Example:
//
//	counts := collection.NewSlice([]int{1, 2, 3}).Lazy().CountBy(func(_ int, value int) int {
//		return value % 2
//	})
//	fmt.Println(counts.Items()[1]) // 2
func (lazySlice LazySlice[T]) CountBy[K comparable](f func(int, T) K) Map[K, int] {
	counts := make(map[K]int)

	for i, v := range lazySlice {
		counts[f(i, v)]++
	}

	return NewMap(counts)
}

// FirstWhere returns the first item for which f returns true, along with a boolean
// indicating whether such an item was found.
// It stops at the first match and returns the zero T and false if none matches.
//
// Example:
//
//	value, found := collection.NewSlice([]int{1, 2}).Lazy().FirstWhere(func(_ int, value int) bool {
//		return value == 2
//	})
//	fmt.Println(value, found) // 2 true
func (lazySlice LazySlice[T]) FirstWhere(f func(int, T) bool) (result T, ok bool) {
	for i, v := range lazySlice {
		if f(i, v) {
			return v, true
		}
	}

	return result, false
}

// First returns the first item yielded by the iterator, along with a boolean
// indicating whether the iterator was non-empty.
// It consumes at most one value; an empty source returns the zero T and false.
//
// Example:
//
//	value, found := collection.NewSlice([]int{4, 5}).Lazy().First()
//	fmt.Println(value, found) // 4 true
func (lazySlice LazySlice[T]) First() (result T, ok bool) {
	for _, v := range lazySlice {
		return v, true
	}

	return result, false
}

// Last returns the last item yielded by the iterator, along with a boolean
// indicating whether the iterator was non-empty.
// It fully consumes the sequence.
// An empty source returns the zero T and false; values are shallow copies.
//
// Example:
//
//	value, found := collection.NewSlice([]int{4, 5}).Lazy().Last()
//	fmt.Println(value, found) // 5 true
func (lazySlice LazySlice[T]) Last() (result T, ok bool) {
	for _, v := range lazySlice {
		result = v
		ok = true
	}

	return result, ok
}

// Contains reports whether any item yielded by the iterator satisfies f.
// It stops at the first match; an empty source returns false without calling f.
//
// Example:
//
//	found := collection.NewSlice([]int{1, 2}).Lazy().Contains(func(_ int, value int) bool {
//		return value == 2
//	})
//	fmt.Println(found) // true
func (lazySlice LazySlice[T]) Contains(f func(int, T) bool) bool {
	_, found := lazySlice.FirstWhere(f)

	return found
}

// Take returns a lazy slice that yields at most limit items.
// Nonpositive limits consume nothing. Positive limits stop upstream immediately
// after the last selected value; output indices start at zero.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2, 3}).Lazy().Take(2).Items()) // [1 2]
func (lazySlice LazySlice[T]) Take(limit int) LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		lazySliceTake(lazySlice, limit, yield)
	})
}

// Skip returns a lazy slice that skips the first n items.
// Nonpositive n skips nothing. It counts iteration positions, not arbitrary
// source indices, and renumbers the retained values densely during iteration.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2, 3}).Lazy().Skip(1).Items()) // [2 3]
func (lazySlice LazySlice[T]) Skip(n int) LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		skipped := 0
		index := 0

		for _, v := range lazySlice {
			if skipped < n {
				skipped++

				continue
			}

			if !yield(index, v) {
				return
			}

			index++
		}
	})
}

// TakeWhile returns a lazy slice that yields items until f first returns false.
// It calls f with source indices and yields dense output indices.
// The failing item is consumed but not yielded; later source items are not read.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2, 3}).Lazy().TakeWhile(func(_ int, value int) bool {
//		return value < 3
//	})
//	fmt.Println(items.Items()) // [1 2]
func (lazySlice LazySlice[T]) TakeWhile(f func(int, T) bool) LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		index := 0

		for i, v := range lazySlice {
			if !f(i, v) {
				return
			}

			if !yield(index, v) {
				return
			}

			index++
		}
	})
}

// SkipWhile returns a lazy slice that skips items until f first returns false,
// then yields all remaining items. It calls f with source indices and yields
// dense output indices.
// The first failing item is included; f is not called on the remaining suffix.
// Construction does not consume the source.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2, 3}).Lazy().SkipWhile(func(_ int, value int) bool {
//		return value < 2
//	})
//	fmt.Println(items.Items()) // [2 3]
func (lazySlice LazySlice[T]) SkipWhile(f func(int, T) bool) LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		lazySliceSkipWhile(lazySlice, f, yield)
	})
}

// TakeUntil returns a lazy slice that yields items until f first returns true.
// It calls f with source indices and yields dense output indices.
// The matching item is excluded, and no later source items are consumed.
//
// Example:
//
//	prefix := collection.NewSlice([]int{1, 2, 3}).Lazy().TakeUntil(func(_ int, value int) bool {
//		return value == 3
//	}).Items()
//	_ = prefix // []int{1, 2}
func (lazySlice LazySlice[T]) TakeUntil(f func(int, T) bool) LazySlice[T] {
	return lazySlice.TakeWhile(func(i int, v T) bool {
		return !f(i, v)
	})
}

// SkipUntil returns a lazy slice that skips items until f first returns true,
// then yields all remaining items. It calls f with source indices and yields
// dense output indices.
// The matching item is included; no match produces an empty sequence.
//
// Example:
//
//	suffix := collection.NewSlice([]int{1, 2, 3}).Lazy().SkipUntil(func(_ int, value int) bool {
//		return value == 2
//	}).Items()
//	_ = suffix // []int{2, 3}
func (lazySlice LazySlice[T]) SkipUntil(f func(int, T) bool) LazySlice[T] {
	return lazySlice.SkipWhile(func(i int, v T) bool {
		return !f(i, v)
	})
}

// Chunk returns an iterator over consecutive slices of the given size.
// It panics if size is less than or equal to zero.
// It buffers during iteration and includes a final short remainder. Each chunk
// owns its shallow outer storage; chunk indices are dense from zero.
//
// Example:
//
//	for _, chunk := range collection.NewSlice([]int{1, 2, 3}).Lazy().Chunk(2) {
//		fmt.Println(chunk) // [1 2], then [3]
//	}
func (lazySlice LazySlice[T]) Chunk(size int) iter.Seq2[int, []T] {
	if size <= 0 {
		panic("chunk size must be greater than zero")
	}

	return func(yield func(int, []T) bool) {
		lazySliceChunk(lazySlice, size, yield)
	}
}

// Sliding returns complete windows, advancing their starts by the first optional
// step, which defaults to one. A step larger than size skips gaps; incomplete
// windows are omitted. Each window owns its shallow outer storage.
// Construction consumes nothing; iteration stops immediately with the consumer.
// It panics if size or step is less than or equal to zero.
//
// Example:
//
//	for _, window := range collection.NewSlice([]int{1, 2, 3, 4, 5}).Lazy().Sliding(2, 3) {
//		fmt.Println(window) // [1 2], then [4 5]
//	}
func (lazySlice LazySlice[T]) Sliding(size int, step ...int) iter.Seq2[int, []T] {
	if size <= 0 {
		panic("sliding window size must be greater than zero")
	}

	s := 1

	if len(step) > 0 {
		s = step[0]
	}

	if s <= 0 {
		panic("sliding window step must be greater than zero")
	}

	return func(yield func(int, []T) bool) {
		lazySliceSliding(lazySlice, size, s, yield)
	}
}

// Reduce reduces the lazy slice to a single value of type K by applying f
// to each item starting from initial.
// It consumes the finite source now; empty input returns initial unchanged.
//
// Example:
//
//	total := collection.NewSlice([]int{1, 2, 3}).Lazy().Reduce(func(sum, _ int, value int) int {
//		return sum + value
//	}, 0)
//	fmt.Println(total) // 6
func (lazySlice LazySlice[T]) Reduce[K any](f func(K, int, T) K, initial K) K {
	acc := initial

	for i, v := range lazySlice {
		acc = f(acc, i, v)
	}

	return acc
}

// Reverse materializes the iterator and returns a new lazy slice with
// items in reversed order.
// Materialization happens on each traversal, not at construction. It reads the
// whole finite source before yielding and owns independent shallow storage.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2, 3}).Lazy().Reverse().Items()) // [3 2 1]
func (lazySlice LazySlice[T]) Reverse() LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		index := 0
		items := lazySlice.Items()
		slices.Reverse(items)

		for _, v := range items {
			if !yield(index, v) {
				return
			}

			index++
		}
	})
}

// Sort materializes the iterator, sorts items using the given comparison function,
// and returns a new lazy slice over the sorted items.
// Each traversal consumes the whole finite source before yielding. Sorting uses
// independent shallow storage and does not guarantee stability for equal values.
// cmp must define a strict weak ordering.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{3, 1, 2}).Lazy().Sort(cmp.Compare).Items()) // [1 2 3]
func (lazySlice LazySlice[T]) Sort(cmp func(T, T) int) LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		index := 0
		items := lazySlice.Items()
		slices.SortFunc(items, cmp)

		for _, v := range items {
			if !yield(index, v) {
				return
			}

			index++
		}
	})
}

// Unique returns a lazy slice containing only the first occurrence of each
// item, as determined by the key function. It calls key with source indices
// and yields dense output indices.
// A fresh seen-key set is allocated per traversal; key is evaluated for every
// visited source item, including duplicates. Keys follow Go equality.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 1, 2}).Lazy().Unique(func(_ int, value int) int {
//		return value
//	})
//	fmt.Println(items.Items()) // [1 2]
func (lazySlice LazySlice[T]) Unique[K comparable](key func(int, T) K) LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		seen := make(map[K]struct{})
		index := 0

		for i, v := range lazySlice {
			k := key(i, v)

			if _, exists := seen[k]; exists {
				continue
			}

			seen[k] = struct{}{}

			if !yield(index, v) {
				return
			}

			index++
		}
	})
}

// Partition splits the iterator into two lazy slices: the first contains
// items for which f returns true, the second contains the rest. It calls f
// with source indices; both returned slices yield dense output indices.
// It fully consumes the sequence to allow both results to be independently iterated.
// Consumption occurs during this call. Results retain source order in separate
// shallowly copied buffers; traversing them does not rerun the predicate.
//
// Example:
//
//	even, odd := collection.NewSlice([]int{1, 2, 3}).Lazy().Partition(func(_ int, value int) bool {
//		return value%2 == 0
//	})
//	fmt.Println(even.Items(), odd.Items()) // [2] [1 3]
func (lazySlice LazySlice[T]) Partition(f func(int, T) bool) (LazySlice[T], LazySlice[T]) {
	var matching, rest []T

	for i, v := range lazySlice {
		if f(i, v) {
			matching = append(matching, v)
		} else {
			rest = append(rest, v)
		}
	}

	return NewLazySlice(slices.All(matching)), NewLazySlice(slices.All(rest))
}

// Concat returns a lazy slice that yields all items from this slice
// followed by all items from other.
// Neither source is consumed until iteration. Stopping in the first source
// prevents the second source from being invoked; output indices are dense.
//
// Example:
//
//	items := collection.NewSlice([]int{1}).Lazy().Concat(collection.NewSlice([]int{2}).Lazy())
//	fmt.Println(items.Items()) // [1 2]
func (lazySlice LazySlice[T]) Concat(other LazySlice[T]) LazySlice[T] {
	return NewLazySlice(func(yield func(int, T) bool) {
		lazySliceConcat(lazySlice, other, yield)
	})
}

// GroupBy groups all items by the key returned by the key function, returning a
// [Map] of sub-slices. It fully consumes the sequence.
// Consumption occurs during this call. The result owns its map and each group's
// shallow slice storage; items within each group retain source order.
//
// Example:
//
//	groups := collection.NewSlice([]int{1, 2, 3}).Lazy().GroupBy(func(_ int, value int) int {
//		return value % 2
//	})
//	fmt.Println(groups.Items()[1].Items()) // [1 3]
func (lazySlice LazySlice[T]) GroupBy[K comparable](key func(int, T) K) Map[K, Slice[T]] {
	grouped := make(map[K][]T)

	for i, v := range lazySlice {
		k := key(i, v)
		grouped[k] = append(grouped[k], v)
	}

	result := make(map[K]Slice[T], len(grouped))

	for k, items := range grouped {
		result[k] = NewSlice(items)
	}

	return NewMap(result)
}

// Nth lazily selects every nth item, starting at the first optional
// zero-based offset. Missing or negative offsets select zero; an offset
// beyond the source yields no items. Selection counts iteration positions,
// not source indices, and output indices are dense, starting at zero.
//
// No source items are consumed until iteration, and stopping downstream
// stops consumption immediately. Values are yielded without deep copying.
// Every positive int stride is accepted. Nth panics immediately if n is
// less than or equal to zero.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2, 3, 4, 5}).Lazy().Nth(2, -1).Items()
//	fmt.Println(items) // [1 3 5]
func (lazySlice LazySlice[T]) Nth(n int, offset ...int) LazySlice[T] {
	start := nthOffset(n, offset)

	return NewLazySlice(func(yield func(int, T) bool) {
		lazySliceNth(lazySlice, n, start, yield)
	})
}

// ForPage returns a lazy slice containing the items for the given
// one-indexed page number and page size.
// It composes Skip((page-1)*perPage) and Take(perPage), consuming nothing until
// iteration. Validate positive arguments and a representable product yourself;
// this method does not check argument validity or integer overflow.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2, 3, 4}).Lazy().ForPage(2, 2).Items()) // [3 4]
func (lazySlice LazySlice[T]) ForPage(page, perPage int) LazySlice[T] {
	return lazySlice.Skip((page - 1) * perPage).Take(perPage)
}

// lazySliceTake yields no more than limit items from lazySlice.
// Nonpositive limits do not invoke the source. Positive limits use dense output
// indices and stop before consuming a value beyond the requested prefix.
func lazySliceTake[T any](lazySlice LazySlice[T], limit int, yield func(int, T) bool) {
	if limit <= 0 {
		return
	}

	lazySliceTakePositive(lazySlice, limit, yield)
}

// lazySliceTakePositive yields exactly up to a positive item limit.
// Its caller has validated the limit. Both source exhaustion and consumer refusal
// terminate immediately; yielded values are shallow copies.
func lazySliceTakePositive[T any](lazySlice LazySlice[T], limit int, yield func(int, T) bool) {
	index := 0

	for _, v := range lazySlice {
		if !yield(index, v) {
			return
		}

		limit--

		if limit == 0 {
			return
		}

		index++
	}
}

// lazySliceSkipWhile skips source items while f returns true.
// After the first false result it never invokes f again. It preserves retained
// order, assigns dense output indices and propagates downstream stopping.
func lazySliceSkipWhile[T any](lazySlice LazySlice[T], f func(int, T) bool, yield func(int, T) bool) {
	skipping := true
	index := 0

	for i, v := range lazySlice {
		if skipping && f(i, v) {
			continue
		}

		skipping = false

		if !yield(index, v) {
			return
		}

		index++
	}
}

// lazySliceChunk yields complete chunks and then an optional final remainder.
// Each yielded chunk owns its shallow outer storage. Consumer refusal discards
// the buffered remainder and stops the source without an extra yield.
func lazySliceChunk[T any](lazySlice LazySlice[T], size int, yield func(int, []T) bool) {
	var chunk []T
	index := 0

	for _, v := range lazySlice {
		chunk = append(chunk, v)

		if len(chunk) != size {
			continue
		}

		if !yield(index, chunk) {
			return
		}

		index++
		chunk = nil
	}

	yieldLazySliceRemainder(chunk, index, yield)
}

// yieldLazySliceRemainder yields the final incomplete chunk when present.
// It runs only after source exhaustion; the result of this final yield is
// intentionally unused because no subsequent items or callbacks remain.
func yieldLazySliceRemainder[T any](chunk []T, index int, yield func(int, []T) bool) {
	if len(chunk) > 0 {
		_ = yield(index, chunk)
	}
}

// lazySliceSliding yields successive sliding windows from lazySlice. When
// step exceeds size, the items between windows are skipped.
func lazySliceSliding[T any](lazySlice LazySlice[T], size, step int, yield func(int, []T) bool) {
	window := make([]T, 0, size)
	index, skip := 0, 0

	for _, v := range lazySlice {
		if skip > 0 {
			skip--

			continue
		}

		window = append(window, v)

		if len(window) < size {
			continue
		}

		if !yield(index, slices.Clone(window)) {
			return
		}

		index++

		window = window[min(step, size):]
		skip = max(step-size, 0)
	}
}

// lazySliceConcat yields all items from lazySlice followed by other.
// It assigns one dense output index stream across both sources. Refusal in the
// first source prevents invoking the second source at all.
func lazySliceConcat[T any](lazySlice, other LazySlice[T], yield func(int, T) bool) {
	index := 0

	if !yieldLazySlice(lazySlice, &index, yield) {
		return
	}

	yieldLazySlice(other, &index, yield)
}

// yieldLazySlice passes items from lazySlice to yield with dense output indices.
// index is shared by the two concatenated traversals and advances only after an
// accepted yield. False reports consumer refusal, not ordinary source exhaustion.
func yieldLazySlice[T any](lazySlice LazySlice[T], index *int, yield func(int, T) bool) bool {
	for _, v := range lazySlice {
		if !yield(*index, v) {
			return false
		}

		(*index)++
	}

	return true
}

// lazySliceNth selects source positions using a countdown rather than a
// growing source index. The nonnegative start and positive n come from
// nthOffset, so resetting the gap to n-1 cannot overflow. Each traversal
// has its own countdown and yields dense output indices until stopped.
func lazySliceNth[T any](lazySlice LazySlice[T], n, start int, yield func(int, T) bool) {
	skip := start
	index := 0

	for _, v := range lazySlice {
		if skip > 0 {
			skip--

			continue
		}

		if !yield(index, v) {
			return
		}

		index++
		skip = n - 1
	}
}

// MarshalJSONTo materializes the iterator and encodes it as a JSON array into enc.
// It consumes the finite source now. Encoding and writer errors are returned.
//
// Example:
//
//	var encoded bytes.Buffer
//	err := collection.NewSlice([]int{1, 2}).Lazy().MarshalJSONTo(jsontext.NewEncoder(&encoded))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(encoded.Len() > 0) // true
func (lazySlice LazySlice[T]) MarshalJSONTo(enc *jsontext.Encoder) error {
	return json.MarshalEncode(enc, lazySlice.Items())
}

// UnmarshalJSONFrom decodes a JSON array from dec into the lazy slice,
// backed by the resulting slice.
// Only successful decoding replaces the iterator. Errors leave its previous
// source unchanged; the new source can be traversed repeatedly.
//
// Example:
//
//	var items collection.LazySlice[int]
//	err := items.UnmarshalJSONFrom(jsontext.NewDecoder(strings.NewReader(`[1,2]`)))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items.Items()) // [1 2]
func (lazySlice *LazySlice[T]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var items []T

	if err := json.UnmarshalDecode(dec, &items); err != nil {
		return err
	}

	*lazySlice = NewLazySlice(slices.All(items))

	return nil
}

// MarshalJSON implements [encoding/json.Marshaler] for v1 compatibility.
// When used with [encoding/json/v2], [LazySlice.MarshalJSONTo] takes precedence.
// It consumes the finite source immediately and returns JSON v2 encoding errors.
//
// Example:
//
//	data, err := collection.NewSlice([]int{1, 2}).Lazy().MarshalJSON()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(string(data)) // [1,2]
func (lazySlice LazySlice[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(lazySlice.Items())
}

// UnmarshalJSON implements [encoding/json.Unmarshaler] for v1 compatibility.
// When used with [encoding/json/v2], [LazySlice.UnmarshalJSONFrom] takes precedence.
// It uses JSON v2 decoding and replaces the source only on success.
//
// Example:
//
//	var items collection.LazySlice[int]
//	if err := items.UnmarshalJSON([]byte(`[1,2]`)); err != nil {
//		panic(err)
//	}
//	fmt.Println(items.Items()) // [1 2]
func (lazySlice *LazySlice[T]) UnmarshalJSON(data []byte) error {
	var items []T

	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}

	*lazySlice = NewLazySlice(slices.All(items))

	return nil
}
