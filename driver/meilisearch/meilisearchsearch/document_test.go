package meilisearchsearch_test

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/search"
)

// TestWritesToAMissingIndexDoNotCreateIt checks that a Put before Sync fails
// instead of letting Meilisearch create an index with default settings.
func TestWritesToAMissingIndexDoNotCreateIt(t *testing.T) {
	t.Parallel()

	fx := newNoteFixture(t, "")

	if err := fx.engine.Drop(t.Context(), fx.index); err != nil {
		t.Fatal(err)
	}

	if err := fx.engine.Put(t.Context(), fx.index, note{
		ID:    "a",
		Title: "late",
	}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Put after Drop = %v, want ErrNotFound", err)
	}

	if err := fx.engine.Check(t.Context()); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Check after the rejected Put = %v, want ErrNotFound", err)
	}
}

// TestPutRejectsReservedFields checks that documents may not use the field
// names the driver adds, before any request is sent.
func TestPutRejectsReservedFields(t *testing.T) {
	t.Parallel()

	err := offlineDriver(t).Put(t.Context(), search.Schema{
		Name: "docs",
	},
		[]search.Document{{
			ID:     "a",
			Source: []byte(`{"title":"x","arkoren_id":"other"}`),
		}})

	if !errors.Is(err, search.ErrUnsupported) {
		t.Fatalf("Put error = %v, want ErrUnsupported", err)
	}
}

// TestPutRejectsRankingScoreCollision protects stored content from the native
// search score field, which would otherwise overwrite and remove that content.
func TestPutRejectsRankingScoreCollision(t *testing.T) {
	t.Parallel()

	err := offlineDriver(t).Put(t.Context(), search.Schema{Name: "docs"}, []search.Document{{
		ID: "a", Source: []byte(`{"title":"x","_rankingScore":"application value"}`),
	}})

	if !errors.Is(err, search.ErrUnsupported) {
		t.Fatalf("Put error=%v; want ErrUnsupported before HTTP", err)
	}
}
