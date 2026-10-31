package engine_test

import (
	"slices"
	"testing"
	"time"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
)

// TestMixedTypesNeverMatchOrPanic checks incompatible filter values fail safely.
func TestMixedTypesNeverMatchOrPanic(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	docs := []search.Document{
		{ID: "num", Source: []byte(`{"title":"x","n":1}`)},
		{ID: "str", Source: []byte(`{"title":"x","n":"1"}`)},
		{ID: "nil", Source: []byte(`{"title":"x","n":null}`)},
		{ID: "none", Source: []byte(`{"title":"x"}`)},
	}

	if err := driver.Put(t.Context(), schema, docs); err != nil {
		t.Fatal(err)
	}

	for _, operator := range []search.Operator{search.OpEqual, search.OpNotEqual, search.OpLess, search.OpGreaterOrEqual} {
		spec := search.Spec{
			Where:  []search.Condition{{Field: "n", Operator: operator, Value: float64(1)}},
			Orders: []search.Order{{Field: "n"}},
		}

		matches, err := driver.Search(t.Context(), schema, spec, 10, 0)

		if err != nil {
			t.Fatal(err)
		}

		for _, item := range matches.Items {
			if item.ID != "num" {
				t.Errorf("operator %s matched %s, want only num", operator, item.ID)
			}
		}
	}
}

// TestNotInNumbersRequiresCompatibleNonmatch excludes equal numbers and all
// wrong-kind, absent, null, or nonscalar fields instead of negating equality.
func TestNotInNumbersRequiresCompatibleNonmatch(t *testing.T) {
	t.Parallel()

	driver := membershipDriver(t)
	ids := membershipIDs(t, driver, []search.Condition{{
		Field: "n", Operator: search.OpNotIn, Values: []any{float64(3), float64(1)},
	}})

	if !slices.Equal(ids, []string{"number-other"}) {
		t.Fatalf("numeric NOT IN matched %v, want [number-other]", ids)
	}
}

// TestNotInStringsRequiresCompatibleNonmatch preserves case-insensitive
// equality while rejecting values that cannot be compared with strings.
func TestNotInStringsRequiresCompatibleNonmatch(t *testing.T) {
	t.Parallel()

	driver := membershipDriver(t)
	ids := membershipIDs(t, driver, []search.Condition{{
		Field: "n", Operator: search.OpNotIn, Values: []any{"absent", "one"},
	}})

	if !slices.Equal(ids, []string{"string-other"}) {
		t.Fatalf("string NOT IN matched %v, want [string-other]", ids)
	}
}

// TestNotInBooleansRequiresCompatibleNonmatch keeps false as a comparable
// scalar, without admitting strings, numbers, or fields with no scalar value.
func TestNotInBooleansRequiresCompatibleNonmatch(t *testing.T) {
	t.Parallel()

	driver := membershipDriver(t)
	ids := membershipIDs(t, driver, []search.Condition{{
		Field: "n", Operator: search.OpNotIn, Values: []any{true},
	}})

	if !slices.Equal(ids, []string{"boolean-other"}) {
		t.Fatalf("boolean NOT IN matched %v, want [boolean-other]", ids)
	}
}

// TestNotInMixedKindsMatchesNothingInAnyOrder requires a typed inequality
// against every operand, as PostgreSQL does. Having one compatible operand
// is insufficient, even when the stored value equals none of the operands.
func TestNotInMixedKindsMatchesNothingInAnyOrder(t *testing.T) {
	t.Parallel()

	driver := membershipDriver(t)

	for _, values := range [][]any{
		{float64(3), "absent", true},
		{"absent", true, float64(3)},
		{true, float64(3), "absent"},
	} {
		ids := membershipIDs(t, driver, []search.Condition{{
			Field: "n", Operator: search.OpNotIn, Values: values,
		}})

		if len(ids) != 0 {
			t.Fatalf("NOT IN %v matched %v, want no documents", values, ids)
		}
	}
}

// TestInMixedKindsFindsCompatibleMatchesInAnyOrder keeps IN as typed
// equalities joined by OR. An incompatible operand must not hide a later
// match, and absent, null, and nonscalar fields must never match.
func TestInMixedKindsFindsCompatibleMatchesInAnyOrder(t *testing.T) {
	t.Parallel()

	driver := membershipDriver(t)

	for _, values := range [][]any{
		{float64(1), "one", true},
		{"one", true, float64(1)},
		{true, float64(1), "one"},
	} {
		ids := membershipIDs(t, driver, []search.Condition{{
			Field: "n", Operator: search.OpIn, Values: values,
		}})

		if !slices.Equal(ids, []string{"boolean-match", "number-match", "string-match"}) {
			t.Fatalf("IN %v matched %v, want [boolean-match number-match string-match]", values, ids)
		}
	}
}

// TestNotInPreservesAndBeforeOr checks a later AND binds only to its OR run.
// A compatible NOT IN result must survive even when that later run is false.
func TestNotInPreservesAndBeforeOr(t *testing.T) {
	t.Parallel()

	driver := membershipDriver(t)
	ids := membershipIDs(t, driver, []search.Condition{
		{Field: "n", Operator: search.OpNotIn, Values: []any{float64(1)}},
		{Or: true, Field: "n", Operator: search.OpEqual, Value: "one"},
		{Field: "n", Operator: search.OpEqual, Value: false},
	})

	if !slices.Equal(ids, []string{"number-other"}) {
		t.Fatalf("a OR b AND c matched %v, want [number-other]", ids)
	}
}

// TestNotInPreservesGroupBoundaries checks a grouped OR remains subject to
// its enclosing AND, including when NOT IN sees a wrong-kind stored value.
func TestNotInPreservesGroupBoundaries(t *testing.T) {
	t.Parallel()

	driver := membershipDriver(t)
	ids := membershipIDs(t, driver, []search.Condition{
		{Group: []search.Condition{
			{Field: "n", Operator: search.OpNotIn, Values: []any{float64(1)}},
			{Or: true, Field: "n", Operator: search.OpEqual, Value: "one"},
		}},
		{Field: "n", Operator: search.OpEqual, Value: "ONE"},
	})

	if !slices.Equal(ids, []string{"string-match"}) {
		t.Fatalf("(a OR b) AND c matched %v, want [string-match]", ids)
	}
}

// TestNotInNormalizesNumbersAndTimestamps checks typed queries still widen
// numeric operands and convert time operands to UTC before driver comparisons.
func TestNotInNormalizesNumbersAndTimestamps(t *testing.T) {
	t.Parallel()

	index := search.NewIndex("docs", func(document map[string]any) string {
		return document["id"].(string)
	}, func(index *search.IndexBuilder) {
		index.Searchable("title")
		index.Filterable("n", "at")
	})

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := engine.Put(t.Context(), index,
		map[string]any{"id": "number-match", "title": "x", "n": 1, "at": "2026-03-05T10:00:00Z"},
		map[string]any{"id": "time-match", "title": "x", "n": 2, "at": "2026-03-04T10:00:00Z"},
		map[string]any{"id": "other", "title": "x", "n": 2, "at": "2026-03-05T10:00:00Z"},
	); err != nil {
		t.Fatal(err)
	}

	moment := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.FixedZone("plus-two", 2*60*60))
	query := search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.WhereNotIn("n", int8(1))
		query.WhereNotIn("at", moment)
	})

	page, err := engine.Search(t.Context(), query, pagination.PageParams{Page: 1, PerPage: 10})

	if err != nil {
		t.Fatal(err)
	}

	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("Search returned %+v, want one match", page)
	}

	if page.Items[0].ID != "other" {
		t.Fatalf("Search matched %s, want other", page.Items[0].ID)
	}
}
