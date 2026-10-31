package meilisearchsearch_test

import (
	"errors"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/search"
)

// TestMultiwordNonphraseExclusionIsUnsupported checks the capability boundary
// remains explicit with a real populated backend instead of broadening matches.
func TestMultiwordNonphraseExclusionIsUnsupported(t *testing.T) {
	t.Parallel()

	fixture := newNoteFixture(t, "")

	if err := fixture.engine.Put(t.Context(), fixture.index, note{ID: "a", Title: "t blue shirt"}); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.engine.Search(t.Context(), search.NewQuery(fixture.index, "-t-shirt"), pagination.PageParams{Page: 1, PerPage: 10})

	if !errors.Is(err, search.ErrUnsupported) {
		t.Fatalf("Search error=%v; want ErrUnsupported", err)
	}
}

// TestIDsOrderByByteOrder checks that empty queries list IDs in byte order
// although Meilisearch compares strings without regard to case.
func TestIDsOrderByByteOrder(t *testing.T) {
	t.Parallel()

	fx := newNoteFixture(t, "")

	ids := []string{"a", "B", "Z", "_", "-", "9", "ab", "Ab"}

	for _, id := range ids {
		if err := fx.engine.Put(t.Context(), fx.index, note{
			ID:    id,
			Title: "note",
		}); err != nil {
			t.Fatal(err)
		}
	}

	wantIDs(t, fx.ids(t, search.NewQuery(fx.index, "")), "-", "9", "Ab", "B", "Z", "_", "a", "ab")
}

// TestEqualOrdersBreakTiesByRelevance checks that documents with the same
// sort key come in relevance order rather than ID order.
func TestEqualOrdersBreakTiesByRelevance(t *testing.T) {
	t.Parallel()

	fx := newNoteFixture(t, "")

	err := fx.engine.Put(t.Context(), fx.index,
		note{
			ID:    "a",
			Title: "red running shoes for the road",
			Size:  num(1),
		},
		note{
			ID:    "b",
			Title: "red",
			Size:  num(1),
		},
		note{
			ID:    "c",
			Title: "red",
			Size:  num(0),
		})

	if err != nil {
		t.Fatal(err)
	}

	wantIDs(t, fx.ids(t, search.NewQuery(fx.index, "red", func(query *search.QueryBuilder) {
		query.OrderBy("size")
	})), "c", "b", "a")
}

// TestNegationNeverMatchesMissingOrNullValues checks the contract's rule that
// a document without a value matches no comparison, != and not in included.
func TestNegationNeverMatchesMissingOrNullValues(t *testing.T) {
	t.Parallel()

	fx := newNoteFixture(t, "")

	err := fx.engine.Put(t.Context(), fx.index,
		note{
			ID:    "a",
			Title: "one",
			Kind:  str("x"),
		},
		note{
			ID:    "b",
			Title: "two",
			Kind:  str("y"),
		},
		note{
			ID:    "c",
			Title: "three",
		})

	if err != nil {
		t.Fatal(err)
	}

	wantIDs(t, fx.ids(t, search.NewQuery(fx.index, "", func(query *search.QueryBuilder) {
		query.Where("kind", "!=", "x")
	})), "b")
	wantIDs(t, fx.ids(t, search.NewQuery(fx.index, "", func(query *search.QueryBuilder) {
		query.WhereNotIn[string]("kind", "x")
	})), "b")
}

// TestFilterValuesCannotInjectConditions checks that quotes in a value stay
// part of the value.
func TestFilterValuesCannotInjectConditions(t *testing.T) {
	t.Parallel()

	fx := newNoteFixture(t, "")

	err := fx.engine.Put(t.Context(), fx.index,
		note{
			ID:    "a",
			Title: "one",
			Kind:  str(`x" OR "kind" != "nothing`),
		},
		note{
			ID:    "b",
			Title: "two",
			Kind:  str("y"),
		})

	if err != nil {
		t.Fatal(err)
	}

	wantIDs(t, fx.ids(t, search.NewQuery(fx.index, "", func(query *search.QueryBuilder) {
		query.Where("kind", `x" OR "kind" != "nothing`)
	})), "a")
	wantIDs(t, fx.ids(t, search.NewQuery(fx.index, "", func(query *search.QueryBuilder) {
		query.Where("kind", `x" OR "kind`)
	})))
}

// TestExclusionsMayComeFirstOrLast checks queries that exclude a word that is
// typed before and after the words that must match.
func TestExclusionsMayComeFirstOrLast(t *testing.T) {
	t.Parallel()

	fx := newNoteFixture(t, "")

	err := fx.engine.Put(t.Context(), fx.index,
		note{
			ID:    "a",
			Title: "red wool socks",
		},
		note{
			ID:    "b",
			Title: "blue wool socks",
		},
		note{
			ID:    "c",
			Title: "red leather boots",
		})

	if err != nil {
		t.Fatal(err)
	}

	wantIDs(t, fx.ids(t, search.NewQuery(fx.index, "-red wool")), "b")
	wantIDs(t, fx.ids(t, search.NewQuery(fx.index, "wool -red")), "b")
	wantIDs(t, fx.ids(t, search.NewQuery(fx.index, `-"red wool" soc`)), "b")
}

// TestLongQueriesAreUnsupported checks that the engine reports a query that
// Meilisearch would truncate.
func TestLongQueriesAreUnsupported(t *testing.T) {
	t.Parallel()

	fx := newNoteFixture(t, "")

	_, err := fx.engine.Search(t.Context(), search.NewQuery(fx.index, "a b c d e f g h i j k"), pagination.PageParams{})

	if !errors.Is(err, search.ErrUnsupported) {
		t.Fatalf("Search of 11 words = %v, want ErrUnsupported", err)
	}
}

// TestSearchRejectsUnalignedWindows checks that a window that is not a whole
// page fails before any request is sent.
func TestSearchRejectsUnalignedWindows(t *testing.T) {
	t.Parallel()

	_, err := offlineDriver(t).Search(t.Context(), search.Schema{
		Name: "docs",
	}, search.Spec{}, 10, 5)

	if !errors.Is(err, search.ErrUnsupported) {
		t.Fatalf("Search error = %v, want ErrUnsupported", err)
	}
}

// TestSearchRejectsLongQueries checks that a query Meilisearch would
// truncate fails before any request is sent.
func TestSearchRejectsLongQueries(t *testing.T) {
	t.Parallel()

	terms := make([]search.Term, 11)

	for i := range terms {
		terms[i] = search.Term{
			Value: "word",
		}
	}

	_, err := offlineDriver(t).Search(t.Context(), search.Schema{
		Name: "docs",
	}, search.Spec{
		Text: terms,
	}, 10, 0)

	if !errors.Is(err, search.ErrUnsupported) {
		t.Fatalf("Search error = %v, want ErrUnsupported", err)
	}
}

// TestSearchRejectsMultiwordNonphraseExclusion protects the core's unordered
// word exclusion from being weakened into the backend's consecutive phrase.
func TestSearchRejectsMultiwordNonphraseExclusion(t *testing.T) {
	t.Parallel()

	_, err := offlineDriver(t).Search(t.Context(), search.Schema{Name: "docs"}, search.Spec{
		Text: []search.Term{{Value: "t-shirt", Exclude: true}},
	}, 10, 0)

	if !errors.Is(err, search.ErrUnsupported) {
		t.Fatalf("Search error=%v; want ErrUnsupported before HTTP", err)
	}
}
