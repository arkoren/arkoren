package validate_test

import (
	"context"
	"testing"
)

// parseable is a schema that parses a value of type T into a value of type T.
type parseable[T any] interface {
	// Parse validates and normalizes input using ctx.
	Parse(ctx context.Context, input T) (T, error)
}

// requireParse fails unless schema parses in into want without error.
func requireParse[T comparable](t *testing.T, schema parseable[T], in, want T) {
	t.Helper()

	got, err := schema.Parse(context.Background(), in)

	if err != nil || got != want {
		t.Errorf("Parse(%v) = %v, %v; want %v", in, got, err, want)
	}
}
