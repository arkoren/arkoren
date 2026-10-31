package collection_test

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"arkoren.dev/foundation/collection"
)

// ExampleTryLazyMap demonstrates continuing past a failed entry without treating
// its value as valid or consuming one of Take's successful-entry positions.
func ExampleTryLazyMap() {
	failure := errors.New("source failed")
	first := collection.NewMap(map[string]int{"apples": 3}).Try()
	failed := collection.NewTryLazyMap(func(yield func(collection.MapEntry[string, int], error) bool) {
		yield(collection.NewMapEntry("apples", 99), failure)
	})
	last := collection.NewMap(map[string]int{"apples": 4}).Try()

	items, err := first.Concat(failed).Concat(last).Take(2).ItemsAll()

	fmt.Println(items["apples"], errors.Is(err, failure))
	// Output: 4 true
}

// ExampleTryLazyMap_MarshalJSONTo exercises the fallible streaming JSON boundary.
func ExampleTryLazyMap_MarshalJSONTo() {
	var encoded bytes.Buffer
	stock := collection.NewMap(map[string]int{"apples": 3}).Try()

	if err := stock.MarshalJSONTo(jsontext.NewEncoder(&encoded)); err != nil {
		panic(err)
	}

	fmt.Println(encoded.Len() > 0)
	// Output: true
}

// TestTryMapTakeCountsSuccessesAndStopsSource checks that errors do not consume
// the entry limit and that duplicate keys are separate successful positions.
func TestTryMapTakeCountsSuccessesAndStopsSource(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("source failed")
	consumed := 0
	source := collection.NewTryLazyMap(func(yield func(collection.MapEntry[string, int], error) bool) {
		for _, value := range []int{1, -1, 2, 3} {
			consumed++
			var err error

			if value < 0 {
				err = wantErr
			}

			if !yield(collection.NewMapEntry("apples", value), err) {
				return
			}
		}
	})

	items, err := source.Take(2).ItemsAll()

	if !errors.Is(err, wantErr) || items["apples"] != 2 {
		t.Fatalf("items = %v, error = %v, want apples:2 and source error", items, err)
	}

	if consumed != 3 {
		t.Fatalf("consumed = %d, want 3", consumed)
	}
}

// TestTryMapSkipForwardsErrorsInSkippedPositions checks that skipped successes
// neither hide intervening errors nor suppress the remaining duplicate key.
func TestTryMapSkipForwardsErrorsInSkippedPositions(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("source failed")
	source := collection.NewTryLazyMap(func(yield func(collection.MapEntry[string, int], error) bool) {
		for _, value := range []int{1, -1, 2} {
			var err error

			if value < 0 {
				err = wantErr
			}

			if !yield(collection.NewMapEntry("apples", value), err) {
				return
			}
		}
	})

	items, err := source.Skip(1).ItemsAll()

	if !errors.Is(err, wantErr) || items["apples"] != 2 {
		t.Fatalf("items = %v, error = %v, want apples:2 and source error", items, err)
	}
}

// TestMapTryReturnsTryLazyMapWithAllItems verifies that Try yields every map entry.
func TestMapTryReturnsTryLazyMapWithAllItems(t *testing.T) {
	t.Parallel()

	items, err := collection.NewMap(map[string]int{
		"a": 1, "b": 2,
	}).Try().Items()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "b": 2,
	}, items) {
		t.Fatal("values differ")
	}
}

// TestTryLazyMapMapValuesPropagatesErrors verifies that MapValues forwards mapper errors.
func TestTryLazyMapMapValuesPropagatesErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")

	items, err := collection.NewMap(map[string]int{
		"a": 1, "b": 2,
	}).Try().MapValues(func(k string, v int) (int, error) {
		if v == 2 {
			return 0, wantErr
		}

		return v * 10, nil
	}).Items()

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v to match %v", err, wantErr)
	}

	_ = items // Map iteration order determines whether a partial result exists.
}

// TestTryLazyMapItemsAsPreservesFirstError verifies that ItemsAs returns the
// named partial map and the first error.
func TestTryLazyMapItemsAsPreservesFirstError(t *testing.T) {
	t.Parallel()

	type namedMap map[string]int
	wantErr := errors.New("boom")
	tryMap := collection.NewTryLazyMap(func(yield func(collection.MapEntry[string, int], error) bool) {
		if !yield(collection.NewMapEntry("a", 1), nil) {
			return
		}

		yield(collection.MapEntry[string, int]{}, wantErr)
	})

	got, err := tryMap.ItemsAs[namedMap]()
	want := namedMap{"a": 1}

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v to match %v", err, wantErr)
	}

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// TestTryLazyMapEachAllJoinsErrors verifies that EachAll joins yielded and callback errors.
func TestTryLazyMapEachAllJoinsErrors(t *testing.T) {
	t.Parallel()

	errA := errors.New("a")
	errB := errors.New("b")
	tryMap := collection.NewTryLazyMap(func(yield func(collection.MapEntry[string, int], error) bool) {
		yield(collection.NewMapEntry("a", 1), nil)
		yield(collection.MapEntry[string, int]{}, errA)
		yield(collection.NewMapEntry("b", 2), nil)
	})

	err := tryMap.EachAll(func(k string, v int) error {
		if k == "b" {
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

// TestTryLazyMapItemsAsAllJoinsErrors verifies that ItemsAsAll retains
// successful entries and joins every error.
func TestTryLazyMapItemsAsAllJoinsErrors(t *testing.T) {
	t.Parallel()

	type namedMap map[string]int
	errA := errors.New("a")
	errB := errors.New("b")
	tryMap := collection.NewTryLazyMap(func(yield func(collection.MapEntry[string, int], error) bool) {
		yield(collection.NewMapEntry("a", 1), nil)
		yield(collection.MapEntry[string, int]{}, errA)
		yield(collection.NewMapEntry("b", 2), nil)
		yield(collection.MapEntry[string, int]{}, errB)
	})

	got, err := tryMap.ItemsAsAll[namedMap]()
	want := namedMap{"a": 1, "b": 2}

	if !errors.Is(err, errA) {
		t.Fatalf("expected error %v to match %v", err, errA)
	}

	if !errors.Is(err, errB) {
		t.Fatalf("expected error %v to match %v", err, errB)
	}

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
