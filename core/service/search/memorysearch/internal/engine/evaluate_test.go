package engine_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
)

// TestSearchObservesCancellationAfterEntry checks a canceled read reports its
// context error rather than returning an apparently complete result window.
func TestSearchObservesCancellationAfterEntry(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	_, err := driver.Search(canceledAtBoundary(t), schema, search.Spec{}, 10, 0)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Search = %v, want context.Canceled", err)
	}
}
