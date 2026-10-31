package validate

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestNodeRunPreparesOnce characterizes the unprepared entry: child preparation
// precedes normalization, and rules observe the resulting value exactly once.
func TestNodeRunPreparesOnce(t *testing.T) {
	t.Parallel()

	preparations, normalizations := 0, 0
	compiled := &node{
		pre: func(_ *state, value reflect.Value) {
			preparations++
			value.SetInt(value.Int() + 1)
		},
		norm: func(_ *state, value reflect.Value) {
			normalizations++
			value.SetInt(value.Int() * 2)
		},
	}
	value := 2

	compiled.run(&state{ctx: context.Background()}, reflect.ValueOf(&value).Elem(), reflect.Value{}, false)

	if value != 6 || preparations != 1 || normalizations != 1 {
		t.Fatalf("value=%d preparations=%d normalizations=%d", value, preparations, normalizations)
	}
}

// TestNodePreparedDoesNotReplayPreparation checks a fully prepared value does
// not repeat either child preparation or its own normalization during rules.
func TestNodePreparedDoesNotReplayPreparation(t *testing.T) {
	t.Parallel()

	preparations, normalizations := 0, 0
	compiled := &node{
		pre: func(_ *state, value reflect.Value) {
			preparations++
			value.SetInt(value.Int() + 1)
		},
		norm: func(_ *state, value reflect.Value) {
			normalizations++
			value.SetInt(value.Int() * 2)
		},
	}
	value := 2
	writable := reflect.ValueOf(&value).Elem()

	compiled.prepare(&state{ctx: context.Background()}, writable)
	compiled.runPrepared(&state{ctx: context.Background()}, writable, reflect.Value{}, false)

	if value != 6 || preparations != 1 || normalizations != 1 {
		t.Fatalf("value=%d preparations=%d normalizations=%d", value, preparations, normalizations)
	}
}

// TestNodeFatalRunSkipsPreparation protects the fatal guard when preparation
// moves to the unprepared entry: no mutation or user callback may occur.
func TestNodeFatalRunSkipsPreparation(t *testing.T) {
	t.Parallel()

	compiled := &node{
		pre: func(*state, reflect.Value) {
			t.Fatal("prepared after fatal failure")
		},
		norm: func(*state, reflect.Value) {
			t.Fatal("normalized after fatal failure")
		},
	}
	value := 2

	compiled.run(&state{fatal: errors.New("stopped")}, reflect.ValueOf(&value).Elem(), reflect.Value{}, false)
}

// TestNodeFatalPreparedRunSkipsDefaultAndRules ensures a fatal state prevents
// default assignment and rule/child execution even for an absent prepared value.
func TestNodeFatalPreparedRunSkipsDefaultAndRules(t *testing.T) {
	t.Parallel()

	compiled := &node{
		def: func(reflect.Value) {
			t.Fatal("defaulted after fatal failure")
		},
		steps: []step{func(*state, *cursor) bool {
			t.Fatal("ran rule after fatal failure")

			return false
		}},
		child: func(*state, reflect.Value) {
			t.Fatal("ran child after fatal failure")
		},
	}
	value := 0

	compiled.runPrepared(&state{fatal: errors.New("stopped")}, reflect.ValueOf(&value).Elem(), reflect.Value{}, false)
}
