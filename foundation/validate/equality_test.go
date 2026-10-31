package validate

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
)

// TestJSONObjectKeysCancelsDuringSort enters the sorting phase before
// cancellation. Checking only before or after a library sort would not stop
// exactly at this checkpoint, so this guards the merge loop itself.
func TestJSONObjectKeysCancelsDuringSort(t *testing.T) {
	t.Parallel()

	object := make(map[string]any, 128)

	for index := range 128 {
		object[strconv.Itoa(index)] = nil
	}

	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &jsonWorkContext{
		Context:     base,
		cancelAfter: len(object) + 8,
		cancel:      cancel,
	}
	st := &state{
		ctx: ctx,
	}
	keys := jsonObjectKeys(st, object)

	if keys != nil || !errors.Is(st.fatal, context.Canceled) || ctx.checks != ctx.cancelAfter {
		t.Fatalf("canceled sort = %v, %v after %d checkpoints", keys, st.fatal, ctx.checks)
	}
}

// TestJSONObjectKeysSortsWithoutMutation verifies key ordering for partial
// merge runs and checks that values and the original map stay untouched.
func TestJSONObjectKeysSortsWithoutMutation(t *testing.T) {
	t.Parallel()

	object := map[string]any{"z": 1, "a": 2, "b": 3, "": 4, "aa": 5}
	st := &state{
		ctx: context.Background(),
	}
	keys := jsonObjectKeys(st, object)

	if !reflect.DeepEqual(keys, []string{"", "a", "aa", "b", "z"}) || st.fatal != nil {
		t.Fatalf("sorted keys = %v, %v", keys, st.fatal)
	}

	if !reflect.DeepEqual(object, map[string]any{"z": 1, "a": 2, "b": 3, "": 4, "aa": 5}) {
		t.Fatalf("object changed: %v", object)
	}
}

// TestJSONEqualCancelsInsideNestedValue bounds work for enum/const equality,
// which must not leave a separate uninterruptible recursive traversal after
// uniqueItems has been fixed. Distinct containers avoid identity shortcuts.
func TestJSONEqualCancelsInsideNestedValue(t *testing.T) {
	t.Parallel()

	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &jsonWorkContext{
		Context:     base,
		cancelAfter: 16,
		cancel:      cancel,
	}
	st := &state{
		ctx: ctx,
	}
	left, right := make([]any, 128), make([]any, 128)

	if jsonEqual(st, map[string]any{"a": left}, map[string]any{"a": right}) {
		t.Fatal("canceled equality reported equal")
	}

	if !errors.Is(st.fatal, context.Canceled) || ctx.checks != ctx.cancelAfter {
		t.Fatalf("canceled equality = %v after %d checkpoints", st.fatal, ctx.checks)
	}
}

// TestJSONStructuralKeyCancelsWithinLargeString verifies chunk checkpoints
// inside a single item's string rather than relying on checks between items.
// The canceled builder must not return a partially encoded usable key.
func TestJSONStructuralKeyCancelsWithinLargeString(t *testing.T) {
	t.Parallel()

	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &jsonWorkContext{
		Context:     base,
		cancelAfter: 3,
		cancel:      cancel,
	}
	st := &state{
		ctx: ctx,
	}
	key, ok := appendJSONKey(st, nil, string(make([]byte, 16384)))

	if ok || key != nil || !errors.Is(st.fatal, context.Canceled) || ctx.checks != ctx.cancelAfter {
		t.Fatalf("canceled string key = %v, %v, %v after %d checkpoints", key, ok, st.fatal, ctx.checks)
	}
}
