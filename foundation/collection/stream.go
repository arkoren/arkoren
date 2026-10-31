package collection

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"iter"
	"slices"
)

// TryLazySlice is a lazily-evaluated collection backed by an [iter.Seq2].
//
// The yielded value must be ignored whenever the yielded error is non-nil.
// Its nil zero value cannot be traversed. Traversals invoke the source anew;
// replayability and resource cleanup belong to the source. Continuing iteration
// after an error continues the source if it permits that. See the package docs
// for callback indices, which do not advance on streaming callback errors.
//
// Example:
//
//	for value, err := range collection.NewSlice([]int{1, 2}).Try() {
//		if err != nil {
//			panic(err)
//		}
//		fmt.Println(value) // 1, then 2
//	}
type TryLazySlice[T any] func(yield func(T, error) bool)

// NewTryLazySlice creates a new [TryLazySlice] from the given iterator function.
// It retains f and its captures without invoking them. Nil f panics when
// traversed. The source must stop after yield is false and release its own
// resources; blocking sources must capture and observe cancellation themselves.
//
// Example:
//
//	items := collection.NewTryLazySlice(func(yield func(int, error) bool) {
//		yield(4, nil)
//	})
//	values, err := items.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(values) // [4]
func NewTryLazySlice[T any](f func(yield func(T, error) bool)) TryLazySlice[T] {
	return TryLazySlice[T](f)
}

// Eager materializes the lazy slice into an eager [Slice].
//
// It stops on the first error and returns the items collected before that error.
// Outer storage is newly owned and shallow; no successful values returns nil.
// The source error is returned unchanged, and the failed value is ignored.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2}).Try().Eager()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items.Items()) // [1 2]
func (tryLazySlice TryLazySlice[T]) Eager() (Slice[T], error) {
	items, err := tryLazySlice.Items()

	return NewSlice(items), err
}

// EagerAll materializes the lazy slice into an eager [Slice].
//
// It consumes the entire sequence, collecting all successful items and joining
// all errors with [errors.Join].
// It requires a finite source and owns independent shallow result storage.
// No successful items returns nil; a terminal source cannot be resumed by All.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2}).Try().EagerAll()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items.Items()) // [1 2]
func (tryLazySlice TryLazySlice[T]) EagerAll() (Slice[T], error) {
	items, err := tryLazySlice.ItemsAll()

	return NewSlice(items), err
}

// Items materializes all successful items from the iterator into a Go slice.
//
// It stops on the first error and returns the items collected before that error.
// The prefix owns independent shallow storage; no successful values returns nil.
// An error is returned unchanged, never paired with its unsuccessful source value.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2}).Try().Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice TryLazySlice[T]) Items() ([]T, error) {
	// items remains nil if the sequence ends or fails before its first success.
	var items []T

	for v, err := range tryLazySlice {
		if err != nil {
			return items, err
		}

		items = append(items, v)
	}

	return items, nil
}

// ItemsAll materializes all successful items from the iterator into a Go slice.
//
// It consumes the entire sequence and joins all errors with [errors.Join].
// It owns independent shallow storage, nil when no successes occur. All means
// continuation only while the source can continue; it does not retry failures.
//
// Example:
//
//	parsed := collection.NewSlice([]string{"1", "bad", "2"}).Try().Map(func(_ int, text string) (int, error) {
//		return strconv.Atoi(text)
//	})
//	items, err := parsed.ItemsAll()
//	if err != nil {
//		fmt.Println(items) // [1 2]: successful values survive the conversion error
//	}
func (tryLazySlice TryLazySlice[T]) ItemsAll() ([]T, error) {
	// items acquires storage only for successes, not for forwarded failures.
	var items []T
	var errs []error

	for v, err := range tryLazySlice {
		if err != nil {
			errs = append(errs, err)

			continue
		}

		items = append(items, v)
	}

	return items, errors.Join(errs...)
}

// ItemsAs materializes all successful items into the given slice type.
//
// It stops on the first error and returns the items collected before that error.
// The named slice owns independent shallow storage and is nil before any success.
// It preserves the original error and successful prefix just like Items.
//
// Example:
//
//	type Numbers []int
//	items, err := collection.NewSlice([]int{1, 2}).Try().ItemsAs[Numbers]()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice TryLazySlice[T]) ItemsAs[S ~[]T]() (S, error) {
	items, err := tryLazySlice.Items()

	return S(items), err
}

// ItemsAsAll materializes all successful items into the given slice type.
//
// It consumes the entire sequence and joins all errors with [errors.Join].
// Like ItemsAll, it returns independently owned shallow storage, nil without
// successful values, but uses the requested named slice type.
//
// Example:
//
//	type Numbers []int
//	items, err := collection.NewSlice([]int{1, 2}).Try().ItemsAsAll[Numbers]()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice TryLazySlice[T]) ItemsAsAll[S ~[]T]() (S, error) {
	items, err := tryLazySlice.ItemsAll()

	return S(items), err
}

// Iter returns the underlying [iter.Seq2].
// It consumes nothing, preserves source order and leaves continuation after
// errors to the consumer. A nil receiver produces a nil iterator.
//
// Example:
//
//	for value, err := range collection.NewSlice([]int{1, 2}).Try().Iter() {
//		if err != nil {
//			panic(err)
//		}
//		fmt.Println(value) // 1, then 2
//	}
func (tryLazySlice TryLazySlice[T]) Iter() iter.Seq2[T, error] {
	return iter.Seq2[T, error](tryLazySlice)
}

// IsEmpty reports whether the iterator yields no successful items.
//
// It stops on the first error.
// It consumes at most one result. An initial error returns false with that
// error; an exhausted source returns true with nil.
//
// Example:
//
//	empty, err := collection.NewSlice[int](nil).Try().IsEmpty()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(empty) // true
func (tryLazySlice TryLazySlice[T]) IsEmpty() (bool, error) {
	for _, err := range tryLazySlice {
		if err != nil {
			return false, err
		}

		return false, nil
	}

	return true, nil
}

// IsNotEmpty reports whether the iterator yields at least one successful item.
//
// It stops on the first error.
// It consumes at most one result. Check the error first: an initial error
// returns true with that error, not evidence of a successful item.
//
// Example:
//
//	found, err := collection.NewSlice([]int{1}).Try().IsNotEmpty()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(found) // true
func (tryLazySlice TryLazySlice[T]) IsNotEmpty() (bool, error) {
	result, err := tryLazySlice.IsEmpty()

	return !result, err
}

// Len returns the total number of successful items yielded by the iterator.
//
// It stops on the first error.
// On failure the count covers only the successful prefix; the original error
// is returned unchanged. With no failure it consumes the whole finite source.
//
// Example:
//
//	count, err := collection.NewSlice([]int{1, 2}).Try().Len()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(count) // 2
func (tryLazySlice TryLazySlice[T]) Len() (int, error) {
	count := 0

	for _, err := range tryLazySlice {
		if err != nil {
			return count, err
		}

		count++
	}

	return count, nil
}

// Every reports whether f returns true for every successful item yielded by the iterator.
//
// It stops on the first error.
// It also stops on the first false predicate. Empty input returns true and nil;
// source or callback errors return false and the unchanged cause.
//
// Example:
//
//	positive, err := collection.NewSlice([]int{1, 2}).Try().Every(func(_ int, value int) (bool, error) {
//		return value > 0, nil
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(positive) // true
func (tryLazySlice TryLazySlice[T]) Every(f func(int, T) (bool, error)) (bool, error) {
	index := 0

	for v, err := range tryLazySlice {
		matched, err := tryLazySliceMatch(v, err, index, f)

		if err != nil {
			return false, err
		}

		if !matched {
			return false, nil
		}

		index++
	}

	return true, nil
}

// Each calls f for every successful item yielded by the iterator.
//
// It stops on the first error, whether yielded by the iterator or returned by f.
// Source errors bypass f and are returned unchanged. Callbacks run synchronously
// with zero-based successful-source indices until the first failure.
//
// Example:
//
//	err := collection.NewSlice([]int{1, 2}).Try().Each(func(index, value int) error {
//		fmt.Println(index, value) // 0 1, then 1 2
//		return nil
//	})
//	if err != nil {
//		panic(err)
//	}
func (tryLazySlice TryLazySlice[T]) Each(f func(int, T) error) error {
	index := 0

	for v, err := range tryLazySlice {
		if err != nil {
			return err
		}

		if err := f(index, v); err != nil {
			return err
		}

		index++
	}

	return nil
}

// EachAll calls f for every successful item yielded by the iterator.
//
// It consumes the entire sequence and joins all iterator and callback errors
// with [errors.Join].
// Callback indices count successful source values, including ones whose callback
// fails. Source errors bypass f and do not advance its index. Unlike streaming
// transformations, a callback failure still advances the next callback's index.
//
// Example:
//
//	err := collection.NewSlice([]int{1, 2}).Try().EachAll(func(index, value int) error {
//		fmt.Println(index, value) // 0 1, then 1 2
//		return nil
//	})
//	if err != nil {
//		panic(err)
//	}
func (tryLazySlice TryLazySlice[T]) EachAll(f func(int, T) error) error {
	var errs []error
	index := 0

	for v, err := range tryLazySlice {
		if err != nil {
			errs = append(errs, err)

			continue
		}

		if err := f(index, v); err != nil {
			errs = append(errs, err)
		}

		index++
	}

	return errors.Join(errs...)
}

// TapEach returns a lazy slice that calls f on each successful item as it is consumed.
// Source and callback errors are forwarded instead of a value. They do not
// advance the callback index; successful callbacks do. Continuing iteration
// resumes with the next source result, not a retry of the failed item.
//
// Example:
//
//	items := collection.NewSlice([]int{1, 2}).Try().TapEach(func(index, value int) error {
//		fmt.Println(index, value) // 0 1, then 1 2, only during Items
//		return nil
//	})
//	if _, err := items.Items(); err != nil {
//		panic(err)
//	}
func (tryLazySlice TryLazySlice[T]) TapEach(f func(int, T) error) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		tryLazySliceTapEach(tryLazySlice, f, yield)
	})
}

// Filter returns a lazy slice containing only successful items for which f returns true.
// It forwards source and predicate errors, rather than filtering them out.
// Errors do not advance f's index; true and false successful results both do.
// Continuing iteration after an error resumes at the next source result.
//
// Example:
//
//	even := collection.NewSlice([]int{1, 2, 3}).Try().Filter(func(_ int, value int) (bool, error) {
//		return value%2 == 0, nil
//	})
//	items, err := even.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [2]
func (tryLazySlice TryLazySlice[T]) Filter(f func(int, T) (bool, error)) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		tryLazySliceFilter(tryLazySlice, f, yield)
	})
}

// Reject returns a lazy slice containing only successful items for which f returns false.
// It inverts only successful predicate results. Source and callback errors are
// forwarded unchanged without advancing the predicate index, as with Filter.
//
// Example:
//
//	odd := collection.NewSlice([]int{1, 2, 3}).Try().Reject(func(_ int, value int) (bool, error) {
//		return value%2 == 0, nil
//	})
//	items, err := odd.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 3]
func (tryLazySlice TryLazySlice[T]) Reject(f func(int, T) (bool, error)) TryLazySlice[T] {
	return tryLazySlice.Filter(func(index int, v T) (bool, error) {
		ok, err := f(index, v)

		if err != nil {
			return false, err
		}

		return !ok, nil
	})
}

// Map transforms each successful item using f and returns a new lazy slice of type K.
// Source errors bypass f. Errors from either source or f are forwarded and do
// not advance the callback index; a successful conversion does. Work is lazy.
//
// Example:
//
//	parsed := collection.NewSlice([]string{"1", "2"}).Try().Map(func(_ int, text string) (int, error) {
//		return strconv.Atoi(text)
//	})
//	items, err := parsed.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice TryLazySlice[T]) Map[K any](f func(int, T) (K, error)) TryLazySlice[K] {
	return NewTryLazySlice(func(yield func(K, error) bool) {
		tryLazySliceMap(tryLazySlice, f, yield)
	})
}

// FlatMap transforms each successful item into zero or more items and yields the flattened results lazily.
// It preserves expansion order and forwards source or callback errors without
// advancing f's index. Every successful callback advances once, even for nil or
// empty output; expansion items are not copied deeply.
//
// Example:
//
//	expanded := collection.NewSlice([]int{1, 2}).Try().FlatMap(func(_ int, value int) ([]int, error) {
//		return []int{value, value}, nil
//	})
//	items, err := expanded.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 1 2 2]
func (tryLazySlice TryLazySlice[T]) FlatMap[K any](f func(int, T) ([]K, error)) TryLazySlice[K] {
	return NewTryLazySlice(func(yield func(K, error) bool) {
		tryLazySliceFlatMap(tryLazySlice, f, yield)
	})
}

// KeyBy indexes yielded items by the key returned from f.
// When multiple items map to the same key, the last item wins.
//
// It stops on the first error.
// It consumes now and returns the independently allocated prefix map plus the
// unchanged source/callback error. Values within the map are shallow copies.
//
// Example:
//
//	indexed, err := collection.NewSlice([]string{"Ada"}).Try().KeyBy(func(_ int, name string) (string, error) {
//		return name, nil
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(indexed.Items()["Ada"]) // Ada
func (tryLazySlice TryLazySlice[T]) KeyBy[K comparable](f func(int, T) (K, error)) (Map[K, T], error) {
	indexed := make(map[K]T)
	index := 0

	for v, err := range tryLazySlice {
		if err != nil {
			return NewMap(indexed), err
		}

		key, err := f(index, v)

		if err != nil {
			return NewMap(indexed), err
		}

		indexed[key] = v
		index++
	}

	return NewMap(indexed), nil
}

// CountBy counts yielded items by the key returned from f.
//
// It stops on the first error.
// It consumes now and returns the successful prefix counts plus the unchanged
// source/callback error. With no error it consumes the whole finite source.
//
// Example:
//
//	counts, err := collection.NewSlice([]int{1, 2, 3}).Try().CountBy(func(_ int, value int) (int, error) {
//		return value % 2, nil
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(counts.Items()[1]) // 2
func (tryLazySlice TryLazySlice[T]) CountBy[K comparable](f func(int, T) (K, error)) (Map[K, int], error) {
	counts := make(map[K]int)
	index := 0

	for v, err := range tryLazySlice {
		if err != nil {
			return NewMap(counts), err
		}

		key, err := f(index, v)

		if err != nil {
			return NewMap(counts), err
		}

		counts[key]++
		index++
	}

	return NewMap(counts), nil
}

// FirstWhere returns the first successful item for which f returns true.
//
// It stops on the first error.
// No match returns the zero T, false and nil. A source/callback failure returns
// the zero T, false and its unchanged error; no later items are read.
//
// Example:
//
//	value, found, err := collection.NewSlice([]int{1, 2}).Try().FirstWhere(func(_ int, value int) (bool, error) {
//		return value == 2, nil
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value, found) // 2 true
func (tryLazySlice TryLazySlice[T]) FirstWhere(f func(int, T) (bool, error)) (result T, ok bool, err error) {
	index := 0

	for v, err := range tryLazySlice {
		matched, err := tryLazySliceMatch(v, err, index, f)

		if err != nil {
			return result, false, err
		}

		if matched {
			return v, true, nil
		}

		index++
	}

	return result, false, nil
}

// First returns the first successful item yielded by the iterator.
//
// It stops on the first error.
// It consumes at most one result. Empty input returns the zero T, false and nil;
// an initial error returns the zero T, false and that error unchanged.
//
// Example:
//
//	value, found, err := collection.NewSlice([]int{4, 5}).Try().First()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value, found) // 4 true
func (tryLazySlice TryLazySlice[T]) First() (result T, ok bool, err error) {
	for v, err := range tryLazySlice {
		if err != nil {
			return result, false, err
		}

		return v, true, nil
	}

	return result, false, nil
}

// Last returns the last successful item yielded by the iterator.
//
// It stops on the first error.
// On failure it returns the last successful prefix value, its found flag and the
// original error. Empty input returns the zero T, false and nil.
//
// Example:
//
//	value, found, err := collection.NewSlice([]int{4, 5}).Try().Last()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value, found) // 5 true
func (tryLazySlice TryLazySlice[T]) Last() (result T, ok bool, err error) {
	for v, err := range tryLazySlice {
		if err != nil {
			return result, ok, err
		}

		result = v
		ok = true
	}

	return result, ok, nil
}

// Contains reports whether any successful item yielded by the iterator satisfies f.
//
// It stops on the first error.
// It also stops at the first match. Empty input returns false and nil;
// source/callback failure returns false and the original error unchanged.
//
// Example:
//
//	found, err := collection.NewSlice([]int{1, 2}).Try().Contains(func(_ int, value int) (bool, error) {
//		return value == 2, nil
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(found) // true
func (tryLazySlice TryLazySlice[T]) Contains(f func(int, T) (bool, error)) (bool, error) {
	_, found, err := tryLazySlice.FirstWhere(f)

	return found, err
}

// Take returns a lazy slice that yields at most limit successful items.
// Nonpositive limits consume nothing, including no errors. Positive limits
// forward errors without counting them, stopping immediately at the last success.
// Continuing iteration after an error resumes the same successful-item limit.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2, 3}).Try().Take(2).Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice TryLazySlice[T]) Take(limit int) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		tryLazySliceTake(tryLazySlice, limit, yield)
	})
}

// Skip returns a lazy slice that skips the first n successful items.
// Nonpositive n skips nothing. All source errors are forwarded without consuming
// a skipped position; no work runs until iteration.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2, 3}).Try().Skip(1).Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [2 3]
func (tryLazySlice TryLazySlice[T]) Skip(n int) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		tryLazySliceSkip(tryLazySlice, n, yield)
	})
}

// TakeWhile returns a lazy slice that yields successful items until f first returns false.
// The first false item is consumed but excluded. Source and callback errors are
// forwarded without advancing f's index or ending selection if the consumer
// continues. Only a successful false result ends predicate-based selection.
//
// Example:
//
//	selected := collection.NewSlice([]int{1, 2, 3}).Try().TakeWhile(func(_ int, value int) (bool, error) {
//		return value < 3, nil
//	})
//	items, err := selected.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice TryLazySlice[T]) TakeWhile(f func(int, T) (bool, error)) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		tryLazySliceTakeWhile(tryLazySlice, f, yield)
	})
}

// SkipWhile returns a lazy slice that skips successful items until f first returns false,
// then yields all remaining successful items.
// The first false item is included and f is not called on the suffix. Errors
// are forwarded; source or predicate errors leave the skipping phase and f's
// index unchanged. Continued iteration resumes at the next source result.
//
// Example:
//
//	selected := collection.NewSlice([]int{1, 2, 3}).Try().SkipWhile(func(_ int, value int) (bool, error) {
//		return value < 2, nil
//	})
//	items, err := selected.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [2 3]
func (tryLazySlice TryLazySlice[T]) SkipWhile(f func(int, T) (bool, error)) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		tryLazySliceSkipWhile(tryLazySlice, f, yield)
	})
}

// TakeUntil returns a lazy slice that yields successful items until f first returns true.
// The matching item is excluded. As with TakeWhile, errors are forwarded without
// advancing the predicate index or ending selection if iteration continues.
//
// Example:
//
//	selected := collection.NewSlice([]int{1, 2, 3}).Try().TakeUntil(func(_ int, value int) (bool, error) {
//		return value == 3, nil
//	})
//	items, err := selected.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice TryLazySlice[T]) TakeUntil(f func(int, T) (bool, error)) TryLazySlice[T] {
	return tryLazySlice.TakeWhile(func(index int, v T) (bool, error) {
		ok, err := f(index, v)

		if err != nil {
			return false, err
		}

		return !ok, nil
	})
}

// SkipUntil returns a lazy slice that skips successful items until f first returns true,
// then yields all remaining successful items.
// The matching item is included. Errors are forwarded without advancing the
// predicate index or ending the skipping phase, as with SkipWhile.
//
// Example:
//
//	selected := collection.NewSlice([]int{1, 2, 3}).Try().SkipUntil(func(_ int, value int) (bool, error) {
//		return value == 2, nil
//	})
//	items, err := selected.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [2 3]
func (tryLazySlice TryLazySlice[T]) SkipUntil(f func(int, T) (bool, error)) TryLazySlice[T] {
	return tryLazySlice.SkipWhile(func(index int, v T) (bool, error) {
		ok, err := f(index, v)

		if err != nil {
			return false, err
		}

		return !ok, nil
	})
}

// Chunk returns an iterator over consecutive slices of the given size.
// It panics if size is less than or equal to zero.
// It groups successful values during iteration, forwarding errors without
// changing the partial chunk. A final short chunk is included. Every chunk owns
// shallow outer storage, independent of the source and other chunks.
//
// Example:
//
//	for chunk, err := range collection.NewSlice([]int{1, 2, 3}).Try().Chunk(2) {
//		if err != nil {
//			panic(err)
//		}
//		fmt.Println(chunk) // [1 2], then [3]
//	}
func (tryLazySlice TryLazySlice[T]) Chunk(size int) iter.Seq2[[]T, error] {
	if size <= 0 {
		panic("chunk size must be greater than zero")
	}

	return func(yield func([]T, error) bool) {
		tryLazySliceChunk(tryLazySlice, size, yield)
	}
}

// Sliding returns complete windows of size, advancing their starts by step
// successful items. The first optional step defaults to one. When step exceeds size, successful
// items between windows are skipped. Errors are yielded without advancing the
// window or the step, including errors between windows. Each window is a fresh
// copy, and stopping iteration stops the source immediately.
// It panics if size or step is less than or equal to zero.
//
// Example:
//
//	for window, err := range collection.NewSlice([]int{1, 2, 3, 4, 5, 6}).Try().Sliding(2, 3) {
//		if err != nil {
//			panic(err)
//		}
//		fmt.Println(window) // [1 2], then [4 5].
//	}
func (tryLazySlice TryLazySlice[T]) Sliding(size int, step ...int) iter.Seq2[[]T, error] {
	s := slidingWindowStep(size, step)

	return func(yield func([]T, error) bool) {
		tryLazySliceSliding(tryLazySlice, size, s, yield)
	}
}

// Reduce reduces the lazy slice to a single value of type K by applying f
// to each successful item starting from initial.
//
// It stops on the first error.
// A source error returns the preceding accumulator; a callback error returns
// the accumulator supplied by that failing callback, paired with its error.
// Empty input returns initial and nil. This call consumes the source now.
//
// Example:
//
//	total, err := collection.NewSlice([]int{1, 2, 3}).Try().Reduce(func(sum, _ int, value int) (int, error) {
//		return sum + value, nil
//	}, 0)
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(total) // 6
func (tryLazySlice TryLazySlice[T]) Reduce[K any](f func(K, int, T) (K, error), initial K) (K, error) {
	acc := initial
	index := 0

	for v, err := range tryLazySlice {
		if err != nil {
			return acc, err
		}

		acc, err = f(acc, index, v)

		if err != nil {
			return acc, err
		}

		index++
	}

	return acc, nil
}

// Reverse materializes the iterator and returns a new lazy slice with items in reversed order.
//
// If materialization fails, the returned sequence yields that error and no items.
// Materialization happens during each traversal, not at this method call, and
// stops at the first source error. Partial successful prefixes are discarded.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2, 3}).Try().Reverse().Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [3 2 1]
func (tryLazySlice TryLazySlice[T]) Reverse() TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		items, err := tryLazySlice.Items()

		if err != nil {
			tryYieldError(yield, err)

			return
		}

		slices.Reverse(items)

		for _, v := range items {
			if !yield(v, nil) {
				return
			}
		}
	})
}

// Sort materializes the iterator, sorts items using the given comparison function,
// and returns a new lazy slice over the sorted items.
//
// If materialization fails, the returned sequence yields that error and no items.
// It consumes the whole finite source on each traversal before yielding. Sorting
// owns shallow storage and is not stable; cmp must define a strict weak ordering.
//
// Example:
//
//	items, err := collection.NewSlice([]int{3, 1, 2}).Try().Sort(cmp.Compare).Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2 3]
func (tryLazySlice TryLazySlice[T]) Sort(cmp func(T, T) int) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		items, err := tryLazySlice.Items()

		if err != nil {
			tryYieldError(yield, err)

			return
		}

		slices.SortFunc(items, cmp)

		for _, v := range items {
			if !yield(v, nil) {
				return
			}
		}
	})
}

// Unique returns a lazy slice containing only the first successful occurrence of each
// item, as determined by the key function.
// Each traversal owns a fresh seen-key set. Source and key errors are forwarded
// without advancing the callback index or adding a key. Successful key calls
// advance even for duplicates; keys follow Go equality.
//
// Example:
//
//	selected := collection.NewSlice([]int{1, 1, 2}).Try().Unique(func(_ int, value int) (int, error) {
//		return value, nil
//	})
//	items, err := selected.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice TryLazySlice[T]) Unique[K comparable](key func(int, T) (K, error)) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		tryLazySliceUnique(tryLazySlice, key, yield)
	})
}

// Partition splits the iterator into two lazy slices: the first contains items for which
// f returns true, the second contains the rest.
// It fully consumes the sequence to allow both results to be independently iterated.
//
// If materialization fails, both returned sequences yield that error and no items.
// Unlike streaming transformations, this call consumes the source immediately
// until exhaustion or the first source/callback error. Successful results are
// replayable shallow buffers; replaying them does not invoke f again.
//
// Example:
//
//	even, odd := collection.NewSlice([]int{1, 2, 3}).Try().Partition(func(_ int, value int) (bool, error) {
//		return value%2 == 0, nil
//	})
//	left, err := even.Items()
//	if err != nil {
//		panic(err)
//	}
//	right, err := odd.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(left, right) // [2] [1 3]
func (tryLazySlice TryLazySlice[T]) Partition(f func(int, T) (bool, error)) (TryLazySlice[T], TryLazySlice[T]) {
	return tryLazySlicePartition(tryLazySlice, f)
}

// Concat returns a lazy slice that yields all items from this slice followed by all items from other.
// It forwards source errors without ending concatenation if the consumer
// continues. Stopping in the first source prevents invoking the second source.
//
// Example:
//
//	first := collection.NewSlice([]int{1}).Try()
//	last := collection.NewSlice([]int{2}).Try()
//	items, err := first.Concat(last).Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice TryLazySlice[T]) Concat(other TryLazySlice[T]) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		if !yieldTryLazySlice(tryLazySlice, yield) {
			return
		}

		yieldTryLazySlice(other, yield)
	})
}

// GroupBy groups all successful items by the key returned by the key function,
// returning a [Map] of sub-slices. It consumes the finite source during this call.
//
// It stops on the first error.
// On failure it returns the successful prefix groups and the unchanged error.
// Groups retain source order and own shallow slice storage in a new outer map.
//
// Example:
//
//	groups, err := collection.NewSlice([]int{1, 2, 3}).Try().GroupBy(func(_ int, value int) (int, error) {
//		return value % 2, nil
//	})
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(groups.Items()[1].Items()) // [1 3]
func (tryLazySlice TryLazySlice[T]) GroupBy[K comparable](key func(int, T) (K, error)) (Map[K, Slice[T]], error) {
	grouped := make(map[K][]T)
	index := 0

	for v, err := range tryLazySlice {
		if err != nil {
			return tryGroupByResult(grouped), err
		}

		k, err := key(index, v)

		if err != nil {
			return tryGroupByResult(grouped), err
		}

		grouped[k] = append(grouped[k], v)
		index++
	}

	return tryGroupByResult(grouped), nil
}

// Nth lazily selects every nth successful item, starting at the first
// optional zero-based offset. Missing or negative offsets select zero; an
// offset beyond the successful source items yields no successful values.
// Errors are forwarded in source order without counting toward the offset
// or stride. Continuing after an error resumes the same selection.
//
// Construction does not consume the source, and stopping downstream stops
// consumption immediately, including on an error. Successful values are
// yielded without deep copying. Every positive int stride is accepted.
// Nth panics immediately if n is less than or equal to zero.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2, 3, 4, 5}).Try().Nth(2, -1).Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 3 5]
func (tryLazySlice TryLazySlice[T]) Nth(n int, offset ...int) TryLazySlice[T] {
	start := nthOffset(n, offset)

	return NewTryLazySlice(func(yield func(T, error) bool) {
		tryLazySliceNth(tryLazySlice, n, start, yield)
	})
}

// tryLazySliceMatch applies f to a successful item or returns its iterator error.
// Source errors bypass f. Predicate results and errors are otherwise preserved
// for terminal operations that stop at the first failure or matching result.
func tryLazySliceMatch[T any](item T, err error, index int, f func(int, T) (bool, error)) (bool, error) {
	if err != nil {
		return false, err
	}

	return f(index, item)
}

// tryLazySliceTapEach applies f and yields every iterator result.
// Only successful callbacks advance its index. Source and callback errors are
// forwarded without retrying the failed item or exposing its value.
func tryLazySliceTapEach[T any](
	tryLazySlice TryLazySlice[T],
	f func(int, T) error,
	yield func(T, error) bool,
) {
	index := 0

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		err = f(index, item)

		if err != nil {
			return tryYieldError(yield, err)
		}

		index++

		return yield(item, nil)
	})
}

// tryLazySliceFilter yields items accepted by f and iterator errors.
// Successful predicate calls advance the index even for nonmatches. Errors leave
// it unchanged and pass the continuation decision to the consumer.
func tryLazySliceFilter[T any](
	tryLazySlice TryLazySlice[T],
	f func(int, T) (bool, error),
	yield func(T, error) bool,
) {
	index := 0

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		matched, err := f(index, item)

		if err != nil {
			return tryYieldError(yield, err)
		}

		index++

		return !matched || yield(item, nil)
	})
}

// tryLazySliceMap transforms successful items and yields iterator errors.
// Only successful conversions advance the callback index. A failed conversion's
// value is discarded, and later inputs are visited only if downstream continues.
func tryLazySliceMap[T, K any](
	tryLazySlice TryLazySlice[T],
	f func(int, T) (K, error),
	yield func(K, error) bool,
) {
	index := 0

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		mapped, err := f(index, item)

		if err != nil {
			return tryYieldError(yield, err)
		}

		index++

		return yield(mapped, nil)
	})
}

// tryLazySliceFlatMap transforms successful items and yields iterator errors.
// One successful expansion advances the input index once, including empty output.
// Errors leave it unchanged; expansion traversal stops on downstream refusal.
func tryLazySliceFlatMap[T, K any](
	tryLazySlice TryLazySlice[T],
	f func(int, T) ([]K, error),
	yield func(K, error) bool,
) {
	index := 0

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		mapped, err := f(index, item)

		if err != nil {
			return tryYieldError(yield, err)
		}

		index++

		return yieldTryLazySliceItems(mapped, yield)
	})
}

// yieldTryLazySliceItems passes items to yield until it requests termination.
// Expansion order is preserved and every item is paired with nil error. False
// stops the enclosing source traversal, not only this expansion.
func yieldTryLazySliceItems[T any](items []T, yield func(T, error) bool) bool {
	for _, item := range items {
		if !yield(item, nil) {
			return false
		}
	}

	return true
}

// tryLazySliceTake yields no more than limit successful items from tryLazySlice.
// Nonpositive limits do not invoke the source. Errors are otherwise forwarded
// without decrementing the limit, and downstream refusal always stops upstream.
func tryLazySliceTake[T any](tryLazySlice TryLazySlice[T], limit int, yield func(T, error) bool) {
	if limit <= 0 {
		return
	}

	tryLazySliceTakePositive(tryLazySlice, limit, yield)
}

// tryLazySliceTakePositive yields exactly up to a positive successful-item limit.
// The caller validates limit. The source stops after the last selected success,
// even when downstream accepted that item; errors do not consume the budget.
func tryLazySliceTakePositive[T any](tryLazySlice TryLazySlice[T], limit int, yield func(T, error) bool) {
	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		limit--

		return yield(item, nil) && limit > 0
	})
}

// tryLazySliceSkip skips n successful items and yields all iterator errors.
// Nonpositive n skips nothing. Errors remain observable within skipped positions
// and do not decrement the successful-item countdown.
func tryLazySliceSkip[T any](tryLazySlice TryLazySlice[T], n int, yield func(T, error) bool) {
	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		if n > 0 {
			n--

			return true
		}

		return yield(item, nil)
	})
}

// tryLazySliceTakeWhile yields items while f returns true and iterator errors.
// Only a successful false predicate ends selection; an accepted error continues
// with the next input and the unchanged callback index.
func tryLazySliceTakeWhile[T any](
	tryLazySlice TryLazySlice[T],
	f func(int, T) (bool, error),
	yield func(T, error) bool,
) {
	index := 0

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		matched, err := f(index, item)

		if err != nil {
			return tryYieldError(yield, err)
		}

		index++

		return matched && yield(item, nil)
	})
}

// tryLazySliceSkipWhile skips items while f returns true and yields iterator errors.
// Source and callback errors do not end the skipping phase or advance f's index.
// After a successful nonmatch, f is never invoked on the remaining suffix.
func tryLazySliceSkipWhile[T any](
	tryLazySlice TryLazySlice[T],
	f func(int, T) (bool, error),
	yield func(T, error) bool,
) {
	// skipping changes only after a successful predicate result rejects an item.
	skipping := true
	index := 0

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		if !skipping {
			return yield(item, nil)
		}

		matched, err := f(index, item)

		if err != nil {
			return tryYieldError(yield, err)
		}

		index++
		skipping = matched

		return matched || yield(item, nil)
	})
}

// tryLazySliceChunk yields complete chunks and then an optional final remainder.
// Errors preserve the partial chunk only while downstream accepts continuation.
// Every emitted chunk owns independent shallow outer storage.
func tryLazySliceChunk[T any](tryLazySlice TryLazySlice[T], size int, yield func([]T, error) bool) {
	var chunk []T

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			if tryYieldError(yield, err) {
				return true
			}

			// Discard the buffered prefix when downstream refuses the error,
			// so the final remainder cannot be emitted after iteration stopped.
			chunk = nil

			return false
		}

		chunk = append(chunk, item)

		if len(chunk) != size {
			return true
		}

		// completed owns this chunk independently of the next buffered chunk.
		completed := chunk
		chunk = nil

		return yield(completed, nil)
	})

	yieldTryLazySliceRemainder(chunk, yield)
}

// yieldTryLazySliceRemainder yields the final incomplete chunk when present.
// It runs after source exhaustion. Its final continuation result is intentionally
// ignored because no subsequent item or callback remains.
func yieldTryLazySliceRemainder[T any](chunk []T, yield func([]T, error) bool) {
	if len(chunk) > 0 {
		_ = yield(chunk, nil)
	}
}

// tryLazySliceSliding yields complete, detached windows and forwards source
// errors. Only successful items advance the gap between windows; an error never
// consumes a skipped position. A false yield stops consumption immediately.
func tryLazySliceSliding[T any](tryLazySlice TryLazySlice[T], size, step int, yield func([]T, error) bool) {
	window := make([]T, 0, size)
	skip := 0

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		if skip > 0 {
			skip--

			return true
		}

		window = append(window, item)

		if len(window) < size {
			return true
		}

		completed := slices.Clone(window)
		window = window[min(step, size):]
		skip = max(step-size, 0)

		return yield(completed, nil)
	})
}

// tryLazySliceUnique yields the first successful item for each key.
// Successful projections advance the index even for duplicates. Errors neither
// advance it nor populate the per-traversal seen set.
func tryLazySliceUnique[T any, K comparable](
	tryLazySlice TryLazySlice[T],
	key func(int, T) (K, error),
	yield func(T, error) bool,
) {
	// seen is local to this traversal; failed key callbacks never populate it.
	seen := make(map[K]struct{})
	index := 0

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		mapped, err := key(index, item)

		if err != nil {
			return tryYieldError(yield, err)
		}

		index++

		if _, exists := seen[mapped]; exists {
			return true
		}

		seen[mapped] = struct{}{}

		return yield(item, nil)
	})
}

// tryLazySlicePartition materializes matching and nonmatching input separately.
// The first source or predicate error stops consumption and becomes the sole
// result of both replayable outputs; successful prefixes are discarded.
func tryLazySlicePartition[T any](
	tryLazySlice TryLazySlice[T],
	f func(int, T) (bool, error),
) (TryLazySlice[T], TryLazySlice[T]) {
	var matching, rest []T
	index := 0

	for item, err := range tryLazySlice {
		matched, err := tryLazySliceMatch(item, err, index, f)

		if err != nil {
			return tryErrorSlice[T](err), tryErrorSlice[T](err)
		}

		if matched {
			matching = append(matching, item)
		} else {
			rest = append(rest, item)
		}

		index++
	}

	return NewSlice(matching).Try(), NewSlice(rest).Try()
}

// yieldTryLazySlice passes iterator results from tryLazySlice to yield.
// False means downstream refused a value or error, allowing Concat to avoid
// invoking its next source. Accepted errors do not terminate concatenation.
func yieldTryLazySlice[T any](tryLazySlice TryLazySlice[T], yield func(T, error) bool) bool {
	for item, err := range tryLazySlice {
		if !yield(item, err) {
			return false
		}
	}

	return true
}

// tryLazySliceNth selects successful items using a nonnegative countdown,
// initialized with the normalized offset and reset to n-1 after selection.
// Errors leave this position unchanged and preserve the downstream stop
// signal, so neither large strides nor error continuation require index sums.
func tryLazySliceNth[T any](tryLazySlice TryLazySlice[T], n, start int, yield func(T, error) bool) {
	skip := start

	tryLazySlice(func(item T, err error) bool {
		if err != nil {
			return tryYieldError(yield, err)
		}

		if skip > 0 {
			skip--

			return true
		}

		skip = n - 1

		return yield(item, nil)
	})
}

// ForPage returns a lazy slice containing the successful items for the given
// one-indexed page number and page size.
// It composes Skip((page-1)*perPage) and Take(perPage) without consuming the
// source until iteration. Validate positive arguments and a representable product
// yourself; there is no validity or overflow check. Errors do not consume page
// positions, but errors in skipped positions are still forwarded.
//
// Example:
//
//	items, err := collection.NewSlice([]int{1, 2, 3, 4}).Try().ForPage(2, 2).Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [3 4]
func (tryLazySlice TryLazySlice[T]) ForPage(page, perPage int) TryLazySlice[T] {
	return tryLazySlice.Skip((page - 1) * perPage).Take(perPage)
}

// MarshalJSONTo materializes the iterator and encodes it as a JSON array into enc.
// It consumes now and stops at the first source error before writing any array
// data. Otherwise it returns JSON v2 encoding or writer errors.
//
// Example:
//
//	var encoded bytes.Buffer
//	err := collection.NewSlice([]int{1, 2}).Try().MarshalJSONTo(jsontext.NewEncoder(&encoded))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(encoded.Len() > 0) // true
func (tryLazySlice TryLazySlice[T]) MarshalJSONTo(enc *jsontext.Encoder) error {
	items, err := tryLazySlice.Items()

	if err != nil {
		return err
	}

	return json.MarshalEncode(enc, items)
}

// UnmarshalJSONFrom decodes a JSON array from dec into the lazy slice,
// backed by the resulting slice.
// Errors leave the previous source intact. Success installs a replayable source
// with the decoded items in order and only nil source errors.
//
// Example:
//
//	var sequence collection.TryLazySlice[int]
//	err := sequence.UnmarshalJSONFrom(jsontext.NewDecoder(strings.NewReader(`[1,2]`)))
//	if err != nil {
//		panic(err)
//	}
//	items, err := sequence.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice *TryLazySlice[T]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var items []T

	if err := json.UnmarshalDecode(dec, &items); err != nil {
		return err
	}

	*tryLazySlice = NewSlice(items).Try()

	return nil
}

// MarshalJSON implements [encoding/json.Marshaler] for v1 compatibility.
// When used with [encoding/json/v2], [TryLazySlice.MarshalJSONTo] takes precedence.
// It consumes now and returns the first source error with nil bytes. Otherwise
// it returns JSON v2 encoding results and errors; no partial prefix is encoded.
//
// Example:
//
//	data, err := collection.NewSlice([]int{1, 2}).Try().MarshalJSON()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(string(data)) // [1,2]
func (tryLazySlice TryLazySlice[T]) MarshalJSON() ([]byte, error) {
	items, err := tryLazySlice.Items()

	if err != nil {
		return nil, err
	}

	return json.Marshal(items)
}

// UnmarshalJSON implements [encoding/json.Unmarshaler] for v1 compatibility.
// When used with [encoding/json/v2], [TryLazySlice.UnmarshalJSONFrom] takes precedence.
// It uses JSON v2 decoding and replaces the source only on success, installing a
// replayable sequence of decoded values with nil source errors.
//
// Example:
//
//	var sequence collection.TryLazySlice[int]
//	if err := sequence.UnmarshalJSON([]byte(`[1,2]`)); err != nil {
//		panic(err)
//	}
//	items, err := sequence.Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(items) // [1 2]
func (tryLazySlice *TryLazySlice[T]) UnmarshalJSON(data []byte) error {
	var items []T

	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}

	*tryLazySlice = NewSlice(items).Try()

	return nil
}

// tryErrorSlice returns a [TryLazySlice] that yields the given error and no items.
// Each traversal emits the same retained cause once. It is used to make both
// partition results report a shared materialization failure without retrying.
func tryErrorSlice[T any](err error) TryLazySlice[T] {
	return NewTryLazySlice(func(yield func(T, error) bool) {
		tryYieldError(yield, err)
	})
}

// tryGroupByResult converts grouped items into a [Map] of eager [Slice] values.
// It allocates a new outer map but retains each already-owned group slice. The
// caller transfers those group buffers rather than mutating them after return.
func tryGroupByResult[K comparable, T any](grouped map[K][]T) Map[K, Slice[T]] {
	result := make(map[K]Slice[T], len(grouped))

	for k, items := range grouped {
		result[k] = NewSlice(items)
	}

	return NewMap(result)
}

// tryYieldError yields err with the zero value of T.
// The zero prevents exposing unsuccessful callback/source values. The result
// preserves the consumer's decision whether iteration continues after failure.
func tryYieldError[T any](yield func(T, error) bool, err error) bool {
	var zero T

	return yield(zero, err)
}
