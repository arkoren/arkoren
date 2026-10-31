package collection

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"iter"
	"slices"
)

// Slice is an eager, in-memory collection of items of type T.
// Its zero value is empty and usable. Construction and Items share storage;
// allocating transformations copy elements shallowly and leave input unchanged.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2})
//	fmt.Println(items.Len()) // 2
type Slice[T any] []T

// NewSlice creates a new [Slice] from a plain Go slice of items of type T.
// No copy is made: writes through either view affect the same backing array.
// Nil input remains nil; use slices.Clone first when independent storage is needed.
//
// Example:
//
//	raw := []int{1, 2}
//	items := collection.NewSlice(raw)
//	items[0] = 9
//	fmt.Println(raw) // [9 2]
func NewSlice[T any](items []T) Slice[T] {
	return Slice[T](items)
}

// NewSliceAs creates a new [Slice] from the given slice type, preserving
// compatibility with named slice types whose underlying type is []T.
// It retains the same backing array and nil representation as items.
//
// Example:
//
//	type Numbers []int
//	items := collection.NewSliceAs(Numbers{1, 2})
//	fmt.Println(items.Items()) // [1 2]
func NewSliceAs[S ~[]T, T any](items S) Slice[T] {
	return NewSlice([]T(items))
}

// Iter returns an [iter.Seq2] over the items in the slice, suitable for
// use in a for-range loop. It yields ascending indices, stops when the consumer
// stops, and reads the shared backing array anew on each traversal.
//
// Example:
//
//	for index, value := range collection.NewSlice([]int{4, 5}).Iter() {
//		fmt.Println(index, value) // 0 4, then 1 5
//	}
func (slice Slice[T]) Iter() iter.Seq2[int, T] {
	return slices.All([]T(slice))
}

// Lazy returns a [LazySlice] backed by the items in the slice.
// Conversion consumes nothing and retains the source backing array, not a copy.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2, 3}).Lazy().Take(2)
//	fmt.Println(items.Items()) // [1 2]
func (slice Slice[T]) Lazy() LazySlice[T] {
	return NewLazySlice(slice.Iter())
}

// Try returns a [TryLazySlice] backed by the items in the slice.
// Conversion is lazy, retains the source storage and yields only nil errors.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2}).Try().Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (slice Slice[T]) Try() TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		for _, v := range slice {
			if !yield(v, nil) {
				return
			}
		}
	})
}

// Items returns the underlying slice of items.
// It does not copy or normalize nil. Writes through the result affect the receiver.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2})
//	items.Items()[0] = 9
//	fmt.Println(items[0]) // 9
func (slice Slice[T]) Items() []T {
	return []T(slice)
}

// ItemsAs returns the underlying items as the given slice type.
// The named result shares backing storage and preserves nil, just like Items.
//
// Example:
//
//	type Numbers []int
//	items := collection.NewSlice([]int{1, 2}).ItemsAs[Numbers]()
//	fmt.Println(items) // [1 2]
func (slice Slice[T]) ItemsAs[S ~[]T]() S {
	return S(slice)
}

// IsEmpty reports whether the slice contains no items.
// Nil and allocated empty slices both return true without invoking callbacks.
//
// Example:
//
//	fmt.Println(collection.NewSlice[int](nil).IsEmpty()) // true
func (slice Slice[T]) IsEmpty() bool {
	return len(slice) == 0
}

// IsNotEmpty reports whether the slice contains at least one item.
// It returns false for both nil and allocated empty slices.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1}).IsNotEmpty()) // true
func (slice Slice[T]) IsNotEmpty() bool {
	return len(slice) > 0
}

// Len returns the number of items in the slice.
// It is a constant-time observation and returns zero for nil storage.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2}).Len()) // 2
func (slice Slice[T]) Len() int {
	return len(slice)
}

// Every reports whether f returns true for every item in the slice.
// It stops at the first false result and returns true without calling f on empty.
//
// Example:
//
//	positive := collection.NewSlice([]int{1, 2}).Every(func(_ int, value int) bool {
//		return value > 0
//	})
//	fmt.Println(positive) // true
func (slice Slice[T]) Every(f func(int, T) bool) bool {
	for i, v := range slice {
		if !f(i, v) {
			return false
		}
	}

	return true
}

// Each calls f for every item in the slice.
// Callbacks run synchronously in index order; callback panics propagate.
//
// Example:
//
//	collection.NewSlice([]int{1, 2}).Each(func(index, value int) {
//		fmt.Println(index, value) // 0 1, then 1 2
//	})
func (slice Slice[T]) Each(f func(int, T)) {
	for i, v := range slice {
		f(i, v)
	}
}

// TapEach calls f for every item and returns the slice unchanged.
// It runs immediately in index order and returns the same shared storage.
//
// Example:
//
//	total := 0
//	items := collection.NewSlice([]int{1, 2}).TapEach(func(_ int, value int) {
//		total += value
//	})
//	fmt.Println(total, items.Len()) // 3 2
func (slice Slice[T]) TapEach(f func(int, T)) Slice[T] {
	for i, v := range slice {
		f(i, v)
	}

	return slice
}

// Filter returns a new slice containing only items for which f returns true.
// Retained items keep their order and use independent, shallow result storage.
// No matches returns nil; f receives source indices, not result indices.
//
// Example:
//
//	even := collection.NewSlice([]int{1, 2, 3}).Filter(func(_ int, value int) bool {
//		return value%2 == 0
//	})
//	fmt.Println(even.Items()) // [2]
func (slice Slice[T]) Filter(f func(int, T) bool) Slice[T] {
	// filtered allocates only when a match exists, retaining nil for no matches.
	var filtered []T

	for i, v := range slice {
		if f(i, v) {
			filtered = append(filtered, v)
		}
	}

	return NewSlice(filtered)
}

// Reject returns a new slice containing only items for which f returns false.
// The original slice is unchanged, and retained items keep their source order.
// Like Filter, it owns shallow result storage and returns nil for no matches.
//
// Example:
//
//	odd := collection.NewSlice([]int{1, 2, 3}).Reject(func(_ int, value int) bool {
//		return value%2 == 0
//	}).Items()
//	_ = odd // []int{1, 3}
func (slice Slice[T]) Reject(f func(int, T) bool) Slice[T] {
	return slice.Filter(func(i int, v T) bool {
		return !f(i, v)
	})
}

// Map transforms each item using f and returns a new slice of type K.
// It calls f in index order, owns the result slice and returns nil for empty input.
// References in returned elements remain owned by the callback's caller.
//
// Example:
//
//	labels := collection.NewSlice([]int{1, 2}).Map(func(_ int, value int) string {
//		return strconv.Itoa(value)
//	})
//	fmt.Println(labels.Items()) // [1 2]
func (slice Slice[T]) Map[K any](f func(int, T) K) Slice[K] {
	if len(slice) == 0 {
		return nil
	}

	mapped := make([]K, len(slice))

	for i, v := range slice {
		mapped[i] = f(i, v)
	}

	return NewSlice(mapped)
}

// FlatMap transforms each item into zero or more items and flattens the
// results into a single slice of type K.
// Source and expansion order are preserved. Results have independent outer
// storage; no expanded items returns nil. Nil callback results add no items.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2}).FlatMap(func(_ int, value int) []int {
//		return []int{value, value}
//	})
//	fmt.Println(items.Items()) // [1 1 2 2]
func (slice Slice[T]) FlatMap[K any](f func(int, T) []K) Slice[K] {
	// flattened stays nil until a callback supplies at least one result.
	var flattened []K

	for i, v := range slice {
		flattened = append(flattened, f(i, v)...)
	}

	return NewSlice(flattened)
}

// KeyBy indexes items by the key returned from f. When multiple items map to
// the same key, the last item wins.
// The result owns its map storage; items themselves are copied shallowly.
//
// Example:
//
//	indexed := collection.NewSlice([]string{"Ada", "Bob"}).KeyBy(func(_ int, name string) string {
//		return name
//	})
//	fmt.Println(indexed.Items()["Ada"]) // Ada
func (slice Slice[T]) KeyBy[K comparable](f func(int, T) K) Map[K, T] {
	indexed := make(map[K]T, len(slice))

	for i, v := range slice {
		indexed[f(i, v)] = v
	}

	return NewMap(indexed)
}

// CountBy counts items by the key returned from f.
// The new map contains only observed keys; absent keys read as zero.
//
// Example:
//
//	counts := collection.NewSlice([]int{1, 2, 3}).CountBy(func(_ int, value int) int {
//		return value % 2
//	})
//	fmt.Println(counts.Items()[1]) // 2
func (slice Slice[T]) CountBy[K comparable](f func(int, T) K) Map[K, int] {
	counts := make(map[K]int, len(slice))

	for i, v := range slice {
		counts[f(i, v)]++
	}

	return NewMap(counts)
}

// FirstWhere returns the first item for which f returns true, along with a boolean
// indicating whether such an item was found.
// It stops at the first match; no match returns the zero T and false.
//
// Example:
//
//	value, found := collection.NewSlice([]int{1, 2}).FirstWhere(func(_ int, value int) bool {
//		return value%2 == 0
//	})
//	fmt.Println(value, found) // 2 true
func (slice Slice[T]) FirstWhere(f func(int, T) bool) (result T, ok bool) {
	for i, v := range slice {
		if f(i, v) {
			return v, true
		}
	}

	return result, false
}

// First returns the first item in the slice, along with a boolean
// indicating whether the slice is non-empty.
// Empty input returns the zero T and false without panicking.
//
// Example:
//
//	value, found := collection.NewSlice([]int{4, 5}).First()
//	fmt.Println(value, found) // 4 true
func (slice Slice[T]) First() (result T, ok bool) {
	if len(slice) == 0 {
		return result, false
	}

	return slice[0], true
}

// Last returns the last item in the slice, along with a boolean
// indicating whether the slice is non-empty.
// Empty input returns the zero T and false without panicking.
//
// Example:
//
//	value, found := collection.NewSlice([]int{4, 5}).Last()
//	fmt.Println(value, found) // 5 true
func (slice Slice[T]) Last() (result T, ok bool) {
	if len(slice) == 0 {
		return result, false
	}

	return slice[len(slice)-1], true
}

// Contains reports whether any item in the slice satisfies f.
// It stops at the first match and returns false without calling f on empty input.
//
// Example:
//
//	found := collection.NewSlice([]int{1, 2}).Contains(func(_ int, value int) bool {
//		return value == 2
//	})
//	fmt.Println(found) // true
func (slice Slice[T]) Contains(f func(int, T) bool) bool {
	_, found := slice.FirstWhere(f)

	return found
}

// Take returns a new slice with at most limit items from the start.
// It shares the source backing array. Nonpositive limits select an empty
// window; limits exceeding Len select the whole slice.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2, 3}).Take(2).Items()) // [1 2]
func (slice Slice[T]) Take(limit int) Slice[T] {
	limit = max(0, min(limit, len(slice)))

	return NewSlice(slice[:limit])
}

// Skip returns a new slice with the first n items removed.
// It shares the source backing array. Nonpositive n skips nothing; n at or
// beyond Len selects an empty window.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2, 3}).Skip(1).Items()) // [2 3]
func (slice Slice[T]) Skip(n int) Slice[T] {
	n = max(0, min(n, len(slice)))

	return NewSlice(slice[n:])
}

// TakeWhile returns a new slice with items from the start until f first
// returns false.
// The first failing item is excluded. It copies the selected prefix shallowly
// into independent storage; an empty prefix returns nil.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2, 3}).TakeWhile(func(_ int, value int) bool {
//		return value < 3
//	})
//	fmt.Println(items.Items()) // [1 2]
func (slice Slice[T]) TakeWhile(f func(int, T) bool) Slice[T] {
	// taken acquires storage only if the selected prefix is nonempty.
	var taken []T

	for i, v := range slice {
		if !f(i, v) {
			break
		}

		taken = append(taken, v)
	}

	return NewSlice(taken)
}

// SkipWhile returns a new slice with leading items removed until f first
// returns false.
// The first failing item is retained. The result shares the source backing
// array, and the predicate is not called again after the first false result.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2, 3}).SkipWhile(func(_ int, value int) bool {
//		return value < 2
//	})
//	fmt.Println(items.Items()) // [2 3]
func (slice Slice[T]) SkipWhile(f func(int, T) bool) Slice[T] {
	i := 0

	for i < len(slice) && f(i, slice[i]) {
		i++
	}

	return NewSlice(slice[i:])
}

// TakeUntil returns a new slice with items from the start until f first
// returns true.
// The matching item is excluded; if no item matches, all items are retained.
// Like TakeWhile, it copies the selected prefix shallowly into independent
// storage and returns nil when no items are selected.
//
// Example:
//
//	prefix := collection.NewSlice([]int{1, 2, 3}).TakeUntil(func(_ int, value int) bool {
//		return value == 3
//	}).Items()
//	_ = prefix // []int{1, 2}
func (slice Slice[T]) TakeUntil(f func(int, T) bool) Slice[T] {
	return slice.TakeWhile(func(i int, v T) bool {
		return !f(i, v)
	})
}

// SkipUntil returns a new slice with leading items removed until f first
// returns true.
// The matching item is retained; if no item matches, the result is empty.
// Like SkipWhile, it shares the source backing array, including its capacity.
//
// Example:
//
//	suffix := collection.NewSlice([]int{1, 2, 3}).SkipUntil(func(_ int, value int) bool {
//		return value == 2
//	}).Items()
//	_ = suffix // []int{2, 3}
func (slice Slice[T]) SkipUntil(f func(int, T) bool) Slice[T] {
	return slice.SkipWhile(func(i int, v T) bool {
		return !f(i, v)
	})
}

// Chunk splits the slice into consecutive sub-slices of the given size.
// It panics if size is less than or equal to zero.
// A final short chunk is included. Empty input returns nil. Chunk windows
// share the original backing array; changing a window can change the source.
//
// Example:
//
//	chunks := collection.NewSlice([]int{1, 2, 3}).Chunk(2)
//	fmt.Println(chunks[0].Items(), chunks[1].Items()) // [1 2] [3]
func (slice Slice[T]) Chunk(size int) []Slice[T] {
	if size <= 0 {
		panic("chunk size must be greater than zero")
	}

	var chunks []Slice[T]

	for i := 0; i < len(slice); i += size {
		end := min(i+size, len(slice))
		chunks = append(chunks, NewSlice(slice[i:end]))
	}

	return chunks
}

// Sliding returns complete windows of size, advancing their starts by step
// items. The first optional step is used, defaulting to one. A step larger
// than size skips items between windows; incomplete windows are omitted.
// If no complete window fits, Sliding returns nil.
//
// Windows share the source's backing array, including overlapping items.
// Every positive int step is accepted, even if the next start is beyond the
// source. It panics if size or the selected step is less than or equal to zero.
//
// Example:
//
//	windows := collection.NewSlice([]int{1, 2, 3, 4, 5, 6}).Sliding(2, 3)
//	fmt.Println(windows[0].Items(), windows[1].Items()) // [1 2] [4 5]
func (slice Slice[T]) Sliding(size int, step ...int) []Slice[T] {
	s := slidingWindowStep(size, step)
	var windows []Slice[T]
	last := len(slice) - size

	for i := 0; i <= last; {
		windows = append(windows, NewSlice(slice[i:i+size]))

		if s > last-i {
			break
		}

		i += s
	}

	return windows
}

// slidingWindowStep validates window arguments and returns the requested step.
// It selects the first supplied step or one by default, ignoring extra steps.
// Invalid size or selected step panics before any iterator can be consumed.
func slidingWindowStep(size int, step []int) int {
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

	return s
}

// Reduce reduces the slice to a single value of type K by applying f
// to each item starting from initial.
// Callbacks run in index order. Empty input returns initial unchanged.
//
// Example:
//
//	total := collection.NewSlice([]int{1, 2, 3}).Reduce(func(sum, _ int, value int) int {
//		return sum + value
//	}, 0)
//	fmt.Println(total) // 6
func (slice Slice[T]) Reduce[K any](f func(K, int, T) K, initial K) K {
	acc := initial

	for i, v := range slice {
		acc = f(acc, i, v)
	}

	return acc
}

// Reverse returns a new slice with items in reversed order.
// It shallow-copies the backing array, leaving source order unchanged.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2, 3}).Reverse().Items()) // [3 2 1]
func (slice Slice[T]) Reverse() Slice[T] {
	reversed := slices.Clone([]T(slice))
	slices.Reverse(reversed)

	return NewSlice(reversed)
}

// Sort returns a new slice with items sorted using the given comparison function.
// It shallow-copies the source before sorting. cmp must define a strict weak
// ordering; equal items are not guaranteed to keep their original order.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{3, 1, 2}).Sort(cmp.Compare).Items()) // [1 2 3]
func (slice Slice[T]) Sort(cmp func(T, T) int) Slice[T] {
	sorted := slices.Clone([]T(slice))
	slices.SortFunc(sorted, cmp)

	return NewSlice(sorted)
}

// Unique returns a new slice containing only the first occurrence of each
// item, as determined by the key function.
// First occurrences retain source order. The result has independent shallow
// storage and is nil for empty input; comparable keys follow Go equality.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 1, 2}).Unique(func(_ int, value int) int {
//		return value
//	})
//	fmt.Println(items.Items()) // [1 2]
func (slice Slice[T]) Unique[K comparable](key func(int, T) K) Slice[T] {
	seen := make(map[K]struct{}, len(slice))
	// unique allocates only after the first previously unseen key is observed.
	var unique []T

	for i, v := range slice {
		k := key(i, v)

		if _, exists := seen[k]; !exists {
			seen[k] = struct{}{}
			unique = append(unique, v)
		}
	}

	return NewSlice(unique)
}

// Partition splits the slice into two: the first contains items for which
// f returns true, the second contains the rest.
// Both preserve source order and own independent shallow storage. A partition
// that receives no values is nil; the predicate runs once per source item.
//
// Example:
//
//	even, odd := collection.NewSlice([]int{1, 2, 3}).Partition(func(_ int, value int) bool {
//		return value%2 == 0
//	})
//	fmt.Println(even.Items(), odd.Items()) // [2] [1 3]
func (slice Slice[T]) Partition(f func(int, T) bool) (Slice[T], Slice[T]) {
	// matching and rest acquire independent storage only for nonempty partitions.
	var matching, rest []T

	for i, v := range slice {
		if f(i, v) {
			matching = append(matching, v)
		} else {
			rest = append(rest, v)
		}
	}

	return NewSlice(matching), NewSlice(rest)
}

// Concat returns a new slice with the items of other appended.
// Neither input is changed. The result owns its outer storage; element
// references remain shared with the inputs.
//
// Example:
//
//	items := collection.NewSlice([]int{1}).Concat(collection.NewSlice([]int{2}))
//	fmt.Println(items.Items()) // [1 2]
func (slice Slice[T]) Concat(other Slice[T]) Slice[T] {
	return NewSlice(append(slices.Clone([]T(slice)), other...))
}

// GroupBy groups items by the key returned by the key function, returning a
// [Map] of sub-slices.
// Each group retains source order. The outer map and group slices are newly
// owned; elements are copied shallowly. Empty input produces no groups.
//
// Example:
//
//	groups := collection.NewSlice([]int{1, 2, 3}).GroupBy(func(_ int, value int) int {
//		return value % 2
//	})
//	fmt.Println(groups.Items()[1].Items()) // [1 3]
func (slice Slice[T]) GroupBy[K comparable](key func(int, T) K) Map[K, Slice[T]] {
	grouped := make(map[K][]T, len(slice))

	for i, v := range slice {
		k := key(i, v)
		grouped[k] = append(grouped[k], v)
	}

	result := make(map[K]Slice[T], len(grouped))

	for k, items := range grouped {
		result[k] = NewSlice(items)
	}

	return NewMap(result)
}

// Nth returns every nth item, starting at the first optional zero-based
// offset. The offset defaults to zero, and negative offsets are clamped to
// zero. An offset at or beyond the source length returns a nil slice.
//
// The result has independent slice storage; elements are copied shallowly.
// Every positive int stride is accepted, even if the next selected position
// is beyond the source. Nth panics if n is less than or equal to zero.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2, 3, 4, 5}).Nth(2, -1).Items()
//	fmt.Println(items) // [1 3 5]
func (slice Slice[T]) Nth(n int, offset ...int) Slice[T] {
	start := nthOffset(n, offset)
	var result []T

	for i := start; i < len(slice); {
		result = append(result, slice[i])

		if n >= len(slice)-i {
			break
		}

		i += n
	}

	return NewSlice(result)
}

// nthOffset validates the positive stride and normalizes the first optional
// offset for all three slice forms. Missing or negative offsets select zero;
// other offsets are retained without adding the stride, avoiding overflow.
// Invalid strides panic before any lazy source can be consumed.
func nthOffset(n int, offset []int) int {
	if n <= 0 {
		panic("nth step must be greater than zero")
	}

	if len(offset) > 0 {
		return max(offset[0], 0)
	}

	return 0
}

// ForPage returns a new slice containing the items for the given
// one-indexed page number and page size.
// It shares the source backing array and composes Skip((page-1)*perPage) with
// Take(perPage). Arguments and integer overflow are not checked: validate a
// positive page and size and a representable product before calling it.
//
// Example:
//
//	fmt.Println(collection.NewSlice([]int{1, 2, 3, 4}).ForPage(2, 2).Items()) // [3 4]
func (slice Slice[T]) ForPage(page, perPage int) Slice[T] {
	return slice.Skip((page - 1) * perPage).Take(perPage)
}

// MarshalJSONTo encodes the slice into enc, identical to marshaling the
// underlying slice directly. Byte slices use base64 strings; other slices
// use JSON arrays with the encoder's options.
// Encoding and writer errors are returned. The encoder controls streaming options.
//
// Example:
//
//	var encoded bytes.Buffer
//	err := collection.NewSlice([]int{1, 2}).MarshalJSONTo(jsontext.NewEncoder(&encoded))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(encoded.Len() > 0) // true
func (slice Slice[T]) MarshalJSONTo(enc *jsontext.Encoder) error {
	return json.MarshalEncode(enc, []T(slice))
}

// UnmarshalJSONFrom decodes a JSON array from dec into the slice.
// Decoder errors are returned; an error can leave a partially updated receiver.
//
// Example:
//
//	var items collection.Slice[int]
//	err := items.UnmarshalJSONFrom(jsontext.NewDecoder(strings.NewReader(`[1,2]`)))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items.Items()) // [1 2]
func (slice *Slice[T]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return json.UnmarshalDecode(dec, (*[]T)(slice))
}

// MarshalJSON implements [encoding/json.Marshaler] for v1 compatibility.
// When used with [encoding/json/v2], [Slice.MarshalJSONTo] takes precedence.
// It uses JSON v2 defaults even when called through a legacy interface.
//
// Example:
//
//	data, err := collection.NewSlice([]int{1, 2}).MarshalJSON()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(string(data)) // [1,2]
func (slice Slice[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal([]T(slice))
}

// UnmarshalJSON implements [encoding/json.Unmarshaler] for v1 compatibility.
// When used with [encoding/json/v2], [Slice.UnmarshalJSONFrom] takes precedence.
// Errors are returned from JSON v2 decoding; partial receiver updates can remain.
//
// Example:
//
//	var items collection.Slice[int]
//	if err := items.UnmarshalJSON([]byte(`[1,2]`)); err != nil {
//		panic(err)
//	}
//	fmt.Println(items.Items()) // [1 2]
func (slice *Slice[T]) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, (*[]T)(slice))
}
