package collection_test

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"arkoren.dev/foundation/collection"
)

// TestTryTapEachContinuesWithUnchangedIndexAfterCallbackError protects the
// streaming callback index and successful values when an observer fails.
func TestTryTapEachContinuesWithUnchangedIndexAfterCallbackError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("observer failed")
	var indices []int

	items, err := collection.NewSlice([]int{-1, 10, 20}).Try().TapEach(func(index, value int) error {
		indices = append(indices, index)

		if value < 0 {
			return wantErr
		}

		return nil
	}).ItemsAll()

	if !errors.Is(err, wantErr) || !slices.Equal(items, []int{10, 20}) {
		t.Fatalf("items = %v, error = %v, want [10 20] and observer error", items, err)
	}

	if !slices.Equal(indices, []int{0, 0, 1}) {
		t.Fatalf("indices = %v, want [0 0 1]", indices)
	}
}

// TestTryEachAllCountsCallbackFailures distinguishes terminal aggregation
// indices from streaming transformations without discarding callback failures.
func TestTryEachAllCountsCallbackFailures(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("observer failed")
	var indices []int

	err := collection.NewSlice([]int{-1, 10, 20}).Try().EachAll(func(index, value int) error {
		indices = append(indices, index)

		if value < 0 {
			return wantErr
		}

		return nil
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want observer error", err)
	}

	if !slices.Equal(indices, []int{0, 1, 2}) {
		t.Fatalf("indices = %v, want [0 1 2]", indices)
	}
}

// TestTryChunkStopsOnErrorWithoutEmittingBufferedPrefix verifies that refusing
// a source error does not flush an incomplete chunk or consume the next item.
func TestTryChunkStopsOnErrorWithoutEmittingBufferedPrefix(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("source failed")
	consumed := 0
	source := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		for _, value := range []int{1, -1, 2} {
			consumed++
			var err error

			if value < 0 {
				err = wantErr
			}

			if !yield(value, err) {
				return
			}
		}
	})

	chunks, err := collection.NewTryLazySlice(source.Chunk(2)).Items()

	if !errors.Is(err, wantErr) || len(chunks) != 0 {
		t.Fatalf("chunks = %v, error = %v, want no chunks and source error", chunks, err)
	}

	if consumed != 2 {
		t.Fatalf("consumed = %d, want 2", consumed)
	}
}

// TestTryFilterCountsRejectedValuesButNotCallbackErrors protects the source
// positions observed by a streaming predicate even when no first value survives.
func TestTryFilterCountsRejectedValuesButNotCallbackErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("predicate failed")
	var indices []int

	items, err := collection.NewSlice([]int{-1, 10, 20}).Try().Filter(func(index, value int) (bool, error) {
		indices = append(indices, index)

		if value < 0 {
			return false, wantErr
		}

		return value > 10, nil
	}).ItemsAll()

	if !errors.Is(err, wantErr) || !slices.Equal(items, []int{20}) {
		t.Fatalf("items = %v, error = %v, want [20] and predicate error", items, err)
	}

	if !slices.Equal(indices, []int{0, 0, 1}) {
		t.Fatalf("indices = %v, want [0 0 1]", indices)
	}
}

// TestTryMapContinuesWithUnchangedIndexAfterCallbackError protects conversion
// failure replacement and index progression through the public materializer.
func TestTryMapContinuesWithUnchangedIndexAfterCallbackError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("conversion failed")
	var indices []int

	items, err := collection.NewSlice([]int{-1, 10, 20}).Try().Map(func(index, value int) (int, error) {
		indices = append(indices, index)

		if value < 0 {
			return 99, wantErr
		}

		return value + index, nil
	}).ItemsAll()

	if !errors.Is(err, wantErr) || !slices.Equal(items, []int{10, 21}) {
		t.Fatalf("items = %v, error = %v, want [10 21] and conversion error", items, err)
	}

	if !slices.Equal(indices, []int{0, 0, 1}) {
		t.Fatalf("indices = %v, want [0 0 1]", indices)
	}
}

// TestTryFlatMapCountsEmptyExpansionButNotCallbackError protects the distinction
// between a successful empty expansion and an unsuccessful source conversion.
func TestTryFlatMapCountsEmptyExpansionButNotCallbackError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("expansion failed")
	var indices []int

	items, err := collection.NewSlice([]int{-1, 10, 20}).Try().FlatMap(func(index, value int) ([]int, error) {
		indices = append(indices, index)

		if value < 0 {
			return []int{99}, wantErr
		}

		if value == 10 {
			return nil, nil
		}

		return []int{value, index}, nil
	}).ItemsAll()

	if !errors.Is(err, wantErr) || !slices.Equal(items, []int{20, 1}) {
		t.Fatalf("items = %v, error = %v, want [20 1] and expansion error", items, err)
	}

	if !slices.Equal(indices, []int{0, 0, 1}) {
		t.Fatalf("indices = %v, want [0 0 1]", indices)
	}
}

// TestTryTakeWhileContinuesAfterPredicateError protects continuation without
// treating an error's boolean value as the end of prefix selection.
func TestTryTakeWhileContinuesAfterPredicateError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("predicate failed")
	var indices []int

	items, err := collection.NewSlice([]int{-1, 10, 20, 30}).Try().TakeWhile(func(index, value int) (bool, error) {
		indices = append(indices, index)

		if value < 0 {
			return false, wantErr
		}

		return value < 20, nil
	}).ItemsAll()

	if !errors.Is(err, wantErr) || !slices.Equal(items, []int{10}) {
		t.Fatalf("items = %v, error = %v, want [10] and predicate error", items, err)
	}

	if !slices.Equal(indices, []int{0, 0, 1}) {
		t.Fatalf("indices = %v, want [0 0 1]", indices)
	}
}

// TestTrySkipWhileContinuesSkippingAfterPredicateError protects both the index
// and skipping phase, then verifies that the predicate never sees the suffix.
func TestTrySkipWhileContinuesSkippingAfterPredicateError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("predicate failed")
	var indices []int

	items, err := collection.NewSlice([]int{-1, 10, 20, 30}).Try().SkipWhile(func(index, value int) (bool, error) {
		indices = append(indices, index)

		if value < 0 {
			return false, wantErr
		}

		return value < 20, nil
	}).ItemsAll()

	if !errors.Is(err, wantErr) || !slices.Equal(items, []int{20, 30}) {
		t.Fatalf("items = %v, error = %v, want [20 30] and predicate error", items, err)
	}

	if !slices.Equal(indices, []int{0, 0, 1}) {
		t.Fatalf("indices = %v, want [0 0 1]", indices)
	}
}

// TestTryUniqueCountsDuplicateKeysButNotCallbackError protects successful input
// positions and ensures that failed projections do not occupy the seen-key set.
func TestTryUniqueCountsDuplicateKeysButNotCallbackError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("key failed")
	var indices []int

	items, err := collection.NewSlice([]int{-1, 10, 10, 20}).Try().Unique(func(index, value int) (int, error) {
		indices = append(indices, index)

		if value < 0 {
			return 10, wantErr
		}

		return value, nil
	}).ItemsAll()

	if !errors.Is(err, wantErr) || !slices.Equal(items, []int{10, 20}) {
		t.Fatalf("items = %v, error = %v, want [10 20] and key error", items, err)
	}

	if !slices.Equal(indices, []int{0, 0, 1, 2}) {
		t.Fatalf("indices = %v, want [0 0 1 2]", indices)
	}
}

// ExampleTryLazySlice demonstrates successful-value retention after conversion
// failures, using the same public operation as the ItemsAll documentation.
func ExampleTryLazySlice() {
	parsed := collection.NewSlice([]string{"1", "bad", "2"}).Try().Map(func(_ int, text string) (int, error) {
		return strconv.Atoi(text)
	})

	items, err := parsed.ItemsAll()

	fmt.Println(items, errors.Is(err, strconv.ErrSyntax))
	// Output: [1 2] true
}

// ExampleTryLazySlice_UnmarshalJSONFrom exercises successful replacement of a
// nil fallible iterator with a replayable decoded sequence.
func ExampleTryLazySlice_UnmarshalJSONFrom() {
	var sequence collection.TryLazySlice[int]

	err := sequence.UnmarshalJSONFrom(jsontext.NewDecoder(strings.NewReader(`[1,2]`)))

	if err != nil {
		panic(err)
	}

	items, err := sequence.Items()

	if err != nil {
		panic(err)
	}

	fmt.Println(items)
	// Output: [1 2]
}

// TestStrideTryNthClampsNegativeOffsets requires fallible selection to use
// the same zero-clamped starting position as eager and infallible selection.
func TestStrideTryNthClampsNegativeOffsets(t *testing.T) {
	t.Parallel()

	source := collection.NewSlice([]int{1, 2, 3, 4, 5}).Try()

	for _, offset := range []int{-1, math.MinInt} {
		got, err := source.Nth(2, offset).Items()

		if err != nil {
			t.Fatal(err)
		}

		if !slices.Equal(got, []int{1, 3, 5}) {
			t.Fatalf("Nth(2, %d) = %v, want [1 3 5]", offset, got)
		}
	}
}

// TestStrideTryNthErrorsDoNotConsumePositions checks errors before selection
// and within the following MaxInt-sized gap, preserving their emission order.
func TestStrideTryNthErrorsDoNotConsumePositions(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("source error")
	source := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		for _, value := range []int{1, -1, 2, -1, 3} {
			var err error

			if value == -1 {
				err = wantErr
			}

			if !yield(value, err) {
				return
			}
		}
	})

	var events []int

	for value, err := range source.Nth(math.MaxInt, 1) {
		if err != nil && !errors.Is(err, wantErr) {
			t.Fatal(err)
		}

		if err != nil {
			events = append(events, 0)

			continue
		}

		events = append(events, value)
	}

	if !slices.Equal(events, []int{0, 2, 0}) {
		t.Fatalf("events = %v, want [error 2 error]", events)
	}
}

// TestStrideTryNthNegativeOffsetContinuesAfterErrors checks that clamping
// keeps the same repeating stride before and after forwarded source errors.
func TestStrideTryNthNegativeOffsetContinuesAfterErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("source error")
	source := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		for _, value := range []int{1, -1, 2, 3, -1, 4, 5} {
			var err error

			if value == -1 {
				err = wantErr
			}

			if !yield(value, err) {
				return
			}
		}
	})

	var events []int

	for value, err := range source.Nth(2, -1) {
		if err != nil && !errors.Is(err, wantErr) {
			t.Fatal(err)
		}

		if err != nil {
			events = append(events, 0)

			continue
		}

		events = append(events, value)
	}

	if !slices.Equal(events, []int{1, 0, 3, 0, 5}) {
		t.Fatalf("events = %v, want [1 error 3 error 5]", events)
	}
}

// TestStrideTryNthStopsAtSelection ensures that lazy construction and
// downstream stopping are preserved when selecting with the largest step.
func TestStrideTryNthStopsAtSelection(t *testing.T) {
	t.Parallel()

	consumed := 0
	source := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		for value := 1; value <= 5; value++ {
			consumed++

			if !yield(value, nil) {
				return
			}
		}
	})

	selected := source.Nth(math.MaxInt, 1)

	if consumed != 0 {
		t.Fatal("Nth consumed source before iteration")
	}

	for value, err := range selected {
		if err != nil {
			t.Fatal(err)
		}

		if value != 2 {
			t.Fatalf("value = %d, want 2", value)
		}

		break
	}

	if consumed != 2 {
		t.Fatalf("consumed = %d, want 2", consumed)
	}
}

// TestStrideTryNthStopsAtError checks that refusing an error during the
// initial offset stops upstream before the first selected successful value.
func TestStrideTryNthStopsAtError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("stop before selection")
	consumed := 0
	source := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		for _, value := range []int{1, -1, 2} {
			consumed++
			var err error

			if value == -1 {
				err = wantErr
			}

			if !yield(value, err) {
				return
			}
		}
	})

	for _, err := range source.Nth(math.MaxInt, 1) {
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}

		break
	}

	if consumed != 2 {
		t.Fatalf("consumed = %d, want 2", consumed)
	}
}

// TestStrideTrySlidingMaxIntStepContinuesErrors checks that a large skipped
// gap does not suppress errors or cause another incomplete window to appear.
func TestStrideTrySlidingMaxIntStepContinuesErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("gap error")
	source := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		for _, value := range []int{1, -1, 2, -1, 3} {
			var err error

			if value == -1 {
				err = wantErr
			}

			if !yield(value, err) {
				return
			}
		}
	})

	var events [][]int

	for window, err := range source.Sliding(1, math.MaxInt) {
		if err != nil && !errors.Is(err, wantErr) {
			t.Fatal(err)
		}

		if err != nil {
			events = append(events, nil)

			continue
		}

		events = append(events, window)
	}

	if !reflect.DeepEqual(events, [][]int{{1}, nil, nil}) {
		t.Fatalf("events = %v, want [[1] error error]", events)
	}
}

// TestTryLazySliceItemsAllReturnsNilWithoutSuccess checks that aggregation
// keeps its joined error identities without allocating unsuccessful items.
func TestTryLazySliceItemsAllReturnsNilWithoutSuccess(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("source failed")

	items, err := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		yield(99, wantErr)
	}).ItemsAll()

	if !errors.Is(err, wantErr) || items != nil {
		t.Fatalf("items = %#v, error = %v, want nil and joined error", items, err)
	}
}

// TestTryLazySliceItemsReturnsNilBeforeFirstSuccess checks that a failure
// before any item preserves the original error with a nil successful prefix.
func TestTryLazySliceItemsReturnsNilBeforeFirstSuccess(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("source failed")

	items, err := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		yield(99, wantErr)
	}).Items()

	if !errors.Is(err, wantErr) || items != nil {
		t.Fatalf("items = %#v, error = %v, want nil and original error", items, err)
	}
}

// tryIntResult represents one item or error in a test sequence.
type tryIntResult struct {
	// value is the item yielded alongside err. Error-only fixtures leave it
	// at zero so assertions can distinguish successful items from failures.
	value int

	// err is the iterator failure for this result, or nil for an item.
	// Tests retain the same error value for errors.Is assertions.
	err error
}

// newTryIntSequence returns a sequence that yields results in order.
func newTryIntSequence(results ...tryIntResult) collection.TryLazySlice[int] {
	return collection.NewTryLazySlice(func(yield func(int, error) bool) {
		yieldTryIntResults(results, yield)
	})
}

// newClosedTryIntSequence returns a sequence that records when iteration stops.
func newClosedTryIntSequence(closed *bool, results ...tryIntResult) collection.TryLazySlice[int] {
	return collection.NewTryLazySlice(func(yield func(int, error) bool) {
		defer func() {
			*closed = true
		}()

		yieldTryIntResults(results, yield)
	})
}

// yieldTryIntResults yields results in order until yield requests termination.
func yieldTryIntResults(results []tryIntResult, yield func(int, error) bool) {
	for _, result := range results {
		if !yield(result.value, result.err) {
			return
		}
	}
}

// stopAtTwo returns a callback that fails when it receives value two.
func stopAtTwo(wantErr error) func(int, int) error {
	return func(_ int, value int) error {
		if value == 2 {
			return wantErr
		}

		return nil
	}
}

// collectTryIntSlices separates yielded slices from yielded errors.
func collectTryIntSlices(sequence func(func([]int, error) bool)) ([][]int, []error) {
	var collected [][]int
	var errs []error

	for items, err := range sequence {
		if err != nil {
			errs = append(errs, err)

			continue
		}

		collected = append(collected, slices.Clone(items))
	}

	return collected, errs
}

// TestSliceTryReturnsTryLazySliceWithAllItems verifies that Slice.Try bridges eager slices
// into TryLazySlice without introducing errors.
func TestSliceTryReturnsTryLazySliceWithAllItems(t *testing.T) {
	t.Parallel()

	items, err := collection.NewSlice([]int{
		1, 2, 3,
	}).Try().Items()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, items) {
		t.Fatal("values differ")
	}
}

// TestLazySliceTryReturnsTryLazySliceWithAllItems verifies that LazySlice.Try bridges lazy slices
// into TryLazySlice without introducing errors.
func TestLazySliceTryReturnsTryLazySliceWithAllItems(t *testing.T) {
	t.Parallel()

	items, err := collection.NewLazySlice(slices.All([]int{
		4, 5, 6,
	})).Try().Items()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		4, 5, 6,
	}, items) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceCanBeUsedDirectlyInForRangeLoop verifies that TryLazySlice[T]
// can be ranged over directly as a Seq2.
func TestTryLazySliceCanBeUsedDirectlyInForRangeLoop(t *testing.T) {
	t.Parallel()

	tryLazy := collection.NewSlice([]int{
		10, 20, 30,
	}).Try()

	var got []int

	for v, err := range tryLazy {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got = append(got, v)
	}

	if !reflect.DeepEqual([]int{
		10, 20, 30,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceMapIsLazy verifies that Map on TryLazySlice defers callback execution
// until the sequence is consumed.
func TestTryLazySliceMapIsLazy(t *testing.T) {
	t.Parallel()

	called := 0
	tryLazy := collection.NewSlice([]int{
		1, 2, 3,
	}).Try()

	mapped := tryLazy.Map(func(i int, v int) (int, error) {
		called++

		return v * 10, nil
	})

	if !reflect.DeepEqual(0, called) {
		t.Fatal("values differ")
	}

	items, err := mapped.Items()

	requireNoError(t, err)

	if !reflect.DeepEqual([]int{
		10, 20, 30,
	}, items) {
		t.Fatal("values differ")
	}

	if !reflect.DeepEqual(3, called) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceFilterIsLazy verifies that Filter on TryLazySlice defers predicate execution
// until the sequence is consumed.
func TestTryLazySliceFilterIsLazy(t *testing.T) {
	t.Parallel()

	called := 0
	tryLazy := collection.NewSlice([]int{
		1, 2, 3, 4,
	}).Try()

	filtered := tryLazy.Filter(func(i int, v int) (bool, error) {
		called++

		return v%2 == 0, nil
	})

	if !reflect.DeepEqual(0, called) {
		t.Fatal("values differ")
	}

	items, err := filtered.Items()

	requireNoError(t, err)

	if !reflect.DeepEqual([]int{
		2, 4,
	}, items) {
		t.Fatal("values differ")
	}

	if !reflect.DeepEqual(4, called) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceMapPropagatesMapperErrors verifies that Map yields mapper errors and
// allows a terminal operation to fail fast.
func TestTryLazySliceMapPropagatesMapperErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")

	items, err := collection.NewSlice([]int{
		1, 2, 3,
	}).Try().Map(func(i int, v int) (int, error) {
		if v == 2 {
			return 0, wantErr
		}

		return v * 10, nil
	}).Items()

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v to match %v", err, wantErr)
	}

	if !reflect.DeepEqual([]int{
		10,
	}, items) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceItemsAllJoinsErrors verifies that ItemsAll consumes the entire sequence,
// preserving valid items and joining all errors.
func TestTryLazySliceItemsAllJoinsErrors(t *testing.T) {
	t.Parallel()

	errA := errors.New("a")
	errB := errors.New("b")

	items, err := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		yield(1, nil)
		yield(0, errA)
		yield(2, nil)
		yield(0, errB)
	}).ItemsAll()

	if !reflect.DeepEqual([]int{
		1, 2,
	}, items) {
		t.Fatal("values differ")
	}

	if !errors.Is(err, errA) {
		t.Fatalf("expected error %v to match %v", err, errA)
	}

	if !errors.Is(err, errB) {
		t.Fatalf("expected error %v to match %v", err, errB)
	}
}

// TestTryLazySliceEachStopsOnCallbackError verifies that Each aborts the underlying sequence
// when the callback returns an error.
func TestTryLazySliceEachStopsOnCallbackError(t *testing.T) {
	t.Parallel()

	closed := false
	wantErr := errors.New("stop")
	tryLazy := newClosedTryIntSequence(&closed,
		tryIntResult{
			value: 1,
		},
		tryIntResult{
			value: 2,
		},
		tryIntResult{
			value: 3,
		},
	)

	err := tryLazy.Each(stopAtTwo(wantErr))

	requireErrorIs(t, err, wantErr)

	if !closed {
		t.Fatal("expected true")
	}
}

// TestTryLazySliceEachAllJoinsIteratorAndCallbackErrors verifies that EachAll consumes
// the entire sequence and joins both iterator and callback errors.
func TestTryLazySliceEachAllJoinsIteratorAndCallbackErrors(t *testing.T) {
	t.Parallel()

	errA := errors.New("iterator")
	errB := errors.New("callback")
	tryLazy := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		yield(1, nil)
		yield(0, errA)
		yield(2, nil)
	})

	err := tryLazy.EachAll(func(i int, v int) error {
		if v == 2 {
			return errB
		}

		return nil
	})

	if !errors.Is(err, errA) {
		t.Fatalf("expected error %v to match %v", err, errA)
	}

	if !errors.Is(err, errB) {
		t.Fatalf("expected error %v to match %v", err, errB)
	}
}

// TestTryLazySliceTakeCountsOnlySuccessfulItems verifies that Take limits successful items
// without consuming past the requested count.
func TestTryLazySliceTakeCountsOnlySuccessfulItems(t *testing.T) {
	t.Parallel()

	errA := errors.New("iterator")

	items, err := newTryIntSequence(
		tryIntResult{
			value: 1,
		},
		tryIntResult{
			err: errA,
		},
		tryIntResult{
			value: 2,
		},
		tryIntResult{
			value: 3,
		},
		tryIntResult{
			value: 4,
		},
	).Take(2).ItemsAll()

	if !reflect.DeepEqual([]int{
		1, 2,
	}, items) {
		t.Fatal("values differ")
	}

	requireErrorIs(t, err, errA)
}

// TestTryLazySliceSkipSkipsOnlySuccessfulItems verifies that Skip ignores iterator errors
// when counting items to skip and still forwards them.
func TestTryLazySliceSkipSkipsOnlySuccessfulItems(t *testing.T) {
	t.Parallel()

	errA := errors.New("iterator")

	items, err := newTryIntSequence(
		tryIntResult{
			value: 1,
		},
		tryIntResult{
			err: errA,
		},
		tryIntResult{
			value: 2,
		},
		tryIntResult{
			value: 3,
		},
	).Skip(1).ItemsAll()

	if !reflect.DeepEqual([]int{
		2, 3,
	}, items) {
		t.Fatal("values differ")
	}

	requireErrorIs(t, err, errA)
}

// TestTryLazySliceReduceStopsOnFirstError verifies that Reduce fail-fast behavior returns
// the accumulator state collected before the error.
func TestTryLazySliceReduceStopsOnFirstError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")

	result, err := collection.NewSlice([]int{
		1, 2, 3,
	}).Try().Reduce(func(acc int, i int, v int) (int, error) {
		if v == 3 {
			return acc, wantErr
		}

		return acc + v, nil
	}, 0)

	if !reflect.DeepEqual(3, result) {
		t.Fatal("values differ")
	}

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v to match %v", err, wantErr)
	}
}

// TestTryLazySliceFirstReturnsFirstSuccessfulItem verifies that First ignores later items
// and returns the first successful one.
func TestTryLazySliceFirstReturnsFirstSuccessfulItem(t *testing.T) {
	t.Parallel()

	result, ok, err := collection.NewSlice([]int{
		7, 8, 9,
	}).Try().First()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(7, result) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceFirstReturnsErrorBeforeAnyValue verifies that First returns an error
// when the sequence begins with an error.
func TestTryLazySliceFirstReturnsErrorBeforeAnyValue(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")

	_, ok, err := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		yield(0, wantErr)
	}).First()

	if ok {
		t.Fatal("expected false")
	}

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v to match %v", err, wantErr)
	}
}

// TestTryLazySliceContainsPropagatesPredicateError verifies that Contains returns predicate errors.
func TestTryLazySliceContainsPropagatesPredicateError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")

	contains, err := collection.NewSlice([]int{
		1, 2, 3,
	}).Try().Contains(func(i int, v int) (bool, error) {
		if v == 2 {
			return false, wantErr
		}

		return v == 3, nil
	})

	if contains {
		t.Fatal("expected false")
	}

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v to match %v", err, wantErr)
	}
}

// TestTryLazySlicePartitionReturnsSeparatedResults verifies that Partition materializes both sides
// and returns them as independently iterable TryLazySlice values.
func TestTryLazySlicePartitionReturnsSeparatedResults(t *testing.T) {
	t.Parallel()

	even, odd := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	}).Try().Partition(func(_ int, value int) (bool, error) {
		return value%2 == 0, nil
	})

	evenItems, err := even.Items()

	requireNoError(t, err)

	oddItems, err := odd.Items()

	requireNoError(t, err)

	if !reflect.DeepEqual([]int{
		2, 4,
	}, evenItems) {
		t.Fatal("values differ")
	}

	if !reflect.DeepEqual([]int{
		1, 3, 5,
	}, oddItems) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceConcatYieldsValuesAndErrorsInOrder verifies that Concat preserves order
// for both items and iterator errors.
func TestTryLazySliceConcatYieldsValuesAndErrorsInOrder(t *testing.T) {
	t.Parallel()

	errA := errors.New("a")
	errB := errors.New("b")
	first := newTryIntSequence(
		tryIntResult{
			value: 1,
		},
		tryIntResult{
			err: errA,
		},
	)
	second := newTryIntSequence(
		tryIntResult{
			value: 2,
		},
		tryIntResult{
			err: errB,
		},
	)

	items, err := first.Concat(second).ItemsAll()

	if !reflect.DeepEqual([]int{
		1, 2,
	}, items) {
		t.Fatal("values differ")
	}

	requireErrorIs(t, err, errA)
	requireErrorIs(t, err, errB)
}

// TestTryLazySliceSortReturnsSortedItems verifies that Sort materializes and sorts successful items.
func TestTryLazySliceSortReturnsSortedItems(t *testing.T) {
	t.Parallel()

	items, err := collection.NewSlice([]int{
		3, 1, 4, 1, 5,
	}).Try().Sort(cmp.Compare[int]).Items()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		1, 1, 3, 4, 5,
	}, items) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceReverseReturnsReversedItems verifies that Reverse materializes and reverses successful items.
func TestTryLazySliceReverseReturnsReversedItems(t *testing.T) {
	t.Parallel()

	items, err := collection.NewSlice([]int{
		1, 2, 3,
	}).Try().Reverse().Items()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		3, 2, 1,
	}, items) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceUniquePropagatesKeyErrors verifies that Unique yields key errors and keeps
// only the first item for each key.
func TestTryLazySliceUniquePropagatesKeyErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")

	items, err := collection.NewSlice([]int{
		1, 2, 2, 3,
	}).Try().Unique(func(i int, v int) (int, error) {
		if v == 3 {
			return 0, wantErr
		}

		return v, nil
	}).Items()

	if !reflect.DeepEqual([]int{
		1, 2,
	}, items) {
		t.Fatal("values differ")
	}

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v to match %v", err, wantErr)
	}
}

// TestTryLazySliceChunkYieldsChunksAndErrors verifies that Chunk groups successful items
// while preserving iterator errors.
func TestTryLazySliceChunkYieldsChunksAndErrors(t *testing.T) {
	t.Parallel()

	errA := errors.New("iterator")
	seq := newTryIntSequence(
		tryIntResult{
			value: 1,
		},
		tryIntResult{
			value: 2,
		},
		tryIntResult{
			err: errA,
		},
		tryIntResult{
			value: 3,
		},
	).Chunk(2)

	chunks, errs := collectTryIntSlices(seq)

	if !reflect.DeepEqual([][]int{
		{
			1, 2,
		}, {
			3,
		},
	}, chunks) {
		t.Fatal("values differ")
	}

	requireErrorIs(t, errors.Join(errs...), errA)
}

// TestTryLazySliceSlidingYieldsWindowsAndErrors verifies that Sliding windows only successful items
// while preserving iterator errors.
func TestTryLazySliceSlidingYieldsWindowsAndErrors(t *testing.T) {
	t.Parallel()

	errA := errors.New("iterator")
	seq := newTryIntSequence(
		tryIntResult{
			value: 1,
		},
		tryIntResult{
			value: 2,
		},
		tryIntResult{
			err: errA,
		},
		tryIntResult{
			value: 3,
		},
		tryIntResult{
			value: 4,
		},
	).Sliding(2)

	windows, errs := collectTryIntSlices(seq)

	if !reflect.DeepEqual([][]int{
		{
			1, 2,
		}, {
			2, 3,
		}, {
			3, 4,
		},
	}, windows) {
		t.Fatal("values differ")
	}

	requireErrorIs(t, errors.Join(errs...), errA)
}

// TestTryLazySliceSlidingStepLargerThanSize keeps the same window starts as
// LazySlice when successful items must be skipped between windows.
func TestTryLazySliceSlidingStepLargerThanSize(t *testing.T) {
	t.Parallel()

	source := collection.NewSlice([]int{1, 2, 3, 4, 5, 6})

	windows, errs := collectTryIntSlices(source.Try().Sliding(2, 3))
	want := [][]int{{1, 2}, {4, 5}}

	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %v, want %v", windows, want)
	}

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	var lazyWindows [][]int

	for _, window := range source.Lazy().Sliding(2, 3) {
		lazyWindows = append(lazyWindows, window)
	}

	if !reflect.DeepEqual(lazyWindows, want) {
		t.Fatalf("LazySlice windows = %v, want %v", lazyWindows, want)
	}
}

// TestTryLazySliceSlidingErrorsDoNotConsumeGap checks that errors encountered
// between disjoint windows are forwarded without counting toward the step.
func TestTryLazySliceSlidingErrorsDoNotConsumeGap(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("gap error")
	source := newTryIntSequence(
		tryIntResult{value: 1}, tryIntResult{value: 2}, tryIntResult{err: wantErr},
		tryIntResult{value: 3}, tryIntResult{value: 4}, tryIntResult{value: 5},
	)

	windows, errs := collectTryIntSlices(source.Sliding(2, 3))

	if want := [][]int{{1, 2}, {4, 5}}; !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %v, want %v", windows, want)
	}

	if len(errs) != 1 || !errors.Is(errs[0], wantErr) {
		t.Fatalf("errors = %v, want [%v]", errs, wantErr)
	}
}

// TestTryLazySliceSlidingStopsBeforeGap verifies that an early stop at a window
// does not request any of the successful items that would form the next gap.
func TestTryLazySliceSlidingStopsBeforeGap(t *testing.T) {
	t.Parallel()

	consumed := 0
	source := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		for item := 1; item <= 6; item++ {
			consumed++

			if !yield(item, nil) {
				return
			}
		}
	})

	for _, err := range source.Sliding(2, 3) {
		if err != nil {
			t.Fatal(err)
		}

		break
	}

	if consumed != 2 {
		t.Fatalf("consumed %d items, want 2", consumed)
	}
}

// TestTryLazySliceSlidingStopsAtGapError verifies that refusing a forwarded
// error stops the source even while the iterator is between windows.
func TestTryLazySliceSlidingStopsAtGapError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("stop in gap")
	consumed := 0
	source := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		consumed++

		if !yield(1, nil) {
			return
		}

		consumed++

		if !yield(0, wantErr) {
			return
		}

		consumed++
		yield(2, nil)
	})

	var gotErr error

	for _, err := range source.Sliding(1, 3) {
		if err != nil {
			gotErr = err

			break
		}
	}

	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("error = %v, want %v", gotErr, wantErr)
	}

	if consumed != 2 {
		t.Fatalf("consumed %d results, want 2", consumed)
	}
}

// TestTryLazySliceSlidingWindowsAreDetached keeps yielded windows directly so
// reuse of the iterator's backing storage cannot hide behind a collecting copy.
func TestTryLazySliceSlidingWindowsAreDetached(t *testing.T) {
	t.Parallel()

	var windows [][]int

	for window, err := range collection.NewSlice([]int{1, 2, 3, 4}).Try().Sliding(2) {
		if err != nil {
			t.Fatal(err)
		}

		windows = append(windows, window)
	}

	windows[0][1] = 99

	if want := [][]int{{1, 99}, {2, 3}, {3, 4}}; !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %v, want %v", windows, want)
	}
}

// TestTryLazySliceGroupByReturnsGroupedItems verifies that GroupBy materializes grouped results.
func TestTryLazySliceGroupByReturnsGroupedItems(t *testing.T) {
	t.Parallel()

	grouped, err := collection.NewSlice([]int{
		1, 2, 3, 4,
	}).Try().GroupBy(evenOddTryKey)

	requireNoError(t, err)

	requireMapSliceValue(t, grouped, "odd", []int{
		1, 3,
	})

	requireMapSliceValue(t, grouped, "even", []int{
		2, 4,
	})
}

// TestTryLazySliceMarshalJSONReturnsErrorOnIteratorError verifies that JSON encoding
// fails when materialization encounters an error.
func TestTryLazySliceMarshalJSONReturnsErrorOnIteratorError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")

	_, err := collection.NewTryLazySlice(func(yield func(int, error) bool) {
		yield(1, nil)
		yield(0, wantErr)
	}).MarshalJSON()

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v to match %v", err, wantErr)
	}
}

// TestTryLazySliceMarshalJSONMarshalsSuccessfulSequence verifies that TryLazySlice marshals
// exactly like a normal slice when no errors are yielded.
func TestTryLazySliceMarshalJSONMarshalsSuccessfulSequence(t *testing.T) {
	t.Parallel()

	data, err := collection.NewSlice([]int{
		1, 2, 3,
	}).Try().MarshalJSON()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requireSameJSON(t, `[1,2,3]`, string(data))
}

// TestTryLazySliceMarshalJSONToEncodesSuccessfulSequence verifies that MarshalJSONTo writes JSON output.
func TestTryLazySliceMarshalJSONToEncodesSuccessfulSequence(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)

	err := collection.NewSlice([]int{
		4, 5, 6,
	}).Try().MarshalJSONTo(enc)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requireSameJSON(t, `[4,5,6]`, buf.String())
}

// TestTryLazySliceUnmarshalJSONDecodesJSONArrayAndYieldsItems verifies that UnmarshalJSON
// populates a TryLazySlice from JSON.
func TestTryLazySliceUnmarshalJSONDecodesJSONArrayAndYieldsItems(t *testing.T) {
	t.Parallel()

	var tryLazy collection.TryLazySlice[int]

	if err := tryLazy.UnmarshalJSON([]byte(`[10,20,30]`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	items, err := tryLazy.Items()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		10, 20, 30,
	}, items) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceUnmarshalJSONFromDecodesJSONArrayAndYieldsItems verifies that UnmarshalJSONFrom
// populates a TryLazySlice from a decoder.
func TestTryLazySliceUnmarshalJSONFromDecodesJSONArrayAndYieldsItems(t *testing.T) {
	t.Parallel()

	dec := jsontext.NewDecoder(strings.NewReader(`[7,8,9]`))

	var tryLazy collection.TryLazySlice[int]

	if err := tryLazy.UnmarshalJSONFrom(dec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	items, err := tryLazy.Items()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		7, 8, 9,
	}, items) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceEagerAllReturnsSliceAndJoinedErrors verifies that EagerAll returns the eager collection form
// together with joined errors.
func TestTryLazySliceEagerAllReturnsSliceAndJoinedErrors(t *testing.T) {
	t.Parallel()

	errA := errors.New("a")
	errB := errors.New("b")

	eager, err := newTryIntSequence(
		tryIntResult{
			value: 1,
		},
		tryIntResult{
			err: errA,
		},
		tryIntResult{
			value: 2,
		},
		tryIntResult{
			err: errB,
		},
	).EagerAll()

	if !reflect.DeepEqual([]int{
		1, 2,
	}, eager.Items()) {
		t.Fatal("values differ")
	}

	requireErrorIs(t, err, errA)
	requireErrorIs(t, err, errB)
}

// TestTryLazySliceSupportsMethodChaining verifies that the error-aware lazy API preserves fluent chains.
func TestTryLazySliceSupportsMethodChaining(t *testing.T) {
	t.Parallel()

	items, err := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	}).Try().Filter(func(i int, v int) (bool, error) {
		return v%2 == 1, nil
	}).Map(func(i int, v int) (string, error) {
		return strings.Repeat("x", v), nil
	}).Take(2).Items()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]string{
		"x", "xxx",
	}, items) {
		t.Fatal("values differ")
	}
}

// TestTryLazySliceStopsGeneratorOnFirstErrorDuringItems verifies that fail-fast materialization
// stops the source and triggers its cleanup immediately.
func TestTryLazySliceStopsGeneratorOnFirstErrorDuringItems(t *testing.T) {
	t.Parallel()

	closed := false
	wantErr := errors.New("scan failed")

	items, err := newClosedTryIntSequence(&closed,
		tryIntResult{
			value: 1,
		},
		tryIntResult{
			err: wantErr,
		},
		tryIntResult{
			value: 2,
		},
	).Items()

	if !reflect.DeepEqual([]int{
		1,
	}, items) {
		t.Fatal("values differ")
	}

	requireErrorIs(t, err, wantErr)

	if !closed {
		t.Fatal("expected true")
	}
}

// TestTryLazySliceUnmarshalJSONReturnsErrorOnInvalidJSON verifies that UnmarshalJSON rejects malformed JSON.
func TestTryLazySliceUnmarshalJSONReturnsErrorOnInvalidJSON(t *testing.T) {
	t.Parallel()

	var tryLazy collection.TryLazySlice[int]

	if tryLazy.UnmarshalJSON([]byte(`not-json`)) == nil {
		t.Fatal("expected an error")
	}
}

// TestTryLazySliceUnmarshalJSONFromReturnsErrorOnInvalidJSON verifies that UnmarshalJSONFrom rejects malformed JSON.
func TestTryLazySliceUnmarshalJSONFromReturnsErrorOnInvalidJSON(t *testing.T) {
	t.Parallel()

	dec := jsontext.NewDecoder(strings.NewReader(`not-json`))

	var tryLazy collection.TryLazySlice[int]

	if tryLazy.UnmarshalJSONFrom(dec) == nil {
		t.Fatal("expected an error")
	}
}

// TestTryLazySliceMarshalJSONRoundTripsWithEncodingJSON verifies encoding/json compatibility.
func TestTryLazySliceMarshalJSONRoundTripsWithEncodingJSON(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(collection.NewSlice([]int{
		1, 2, 3,
	}).Try())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded []int

	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, decoded) {
		t.Fatal("values differ")
	}
}
