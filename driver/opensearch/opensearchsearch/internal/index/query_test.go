package index_test

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"arkoren.dev/core/service/search"
	opensearchsearch "arkoren.dev/driver/opensearch/opensearchsearch/internal/index"
)

// testSchema combines text, keyword, and numeric roles for compiler assertions.
// Tests read it without mutation so parallel compilation checks share one schema.
var testSchema = search.Schema{
	Name:       "products",
	Searchable: []string{"description", "title"},
	Filterable: []string{"category", "price", "title"},
	Sortable:   []string{"price"},
}

// compact encodes value with sorted keys, so tests compare it as text.
func compact(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value, json.Deterministic(true))

	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	return string(encoded)
}

// specOf builds the spec of a query over testSchema's fields.
func specOf(text string, configure func(*search.QueryBuilder)) search.Spec {
	index := search.NewIndex("products", func(document map[string]any) string {
		return ""
	}, func(index *search.IndexBuilder) {
		index.Searchable("description", "title")
		index.Filterable("category", "price", "title")
		index.Sortable("price")
	})

	return search.NewQuery(index, text, configure).Inspect()
}

// TestSearchBodyTextAndOrder verifies the clauses of text terms, the sort of an
// ordered query with relevance and ID tie-breaks, and the window.
func TestSearchBodyTextAndOrder(t *testing.T) {
	t.Parallel()

	spec := specOf(`red "running shoes" -boots soc`, func(query *search.QueryBuilder) {
		query.OrderByDesc("price")
	})

	got := compact(t, opensearchsearch.SearchBody(testSchema, spec, 20, 40))

	for _, want := range []string{
		`{"match":{"strings.description":{"operator":"and","query":"red"}}}`,
		`{"match_phrase":{"strings.title":"running shoes"}}`,
		`{"match_bool_prefix":{"strings.description":{"operator":"and","query":"soc"}}}`,
		`"must_not":[{"bool":{"minimum_should_match":1,"should":[{"match":{"strings.description":{"operator":"and","query":"boots"}}}`,
		`"sort":[{"numbers.price":{"missing":"_last","order":"desc","unmapped_type":"double"}},{"strings.price":{"missing":"_last","order":"desc","unmapped_type":"keyword"}},{"booleans.price":{"missing":"_last","order":"desc","unmapped_type":"boolean"}},{"_score":{"order":"desc"}},{"_id":{"order":"asc"}}]`,
		`"from":40`,
		`"size":20`,
		`"track_total_hits":1000`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("body lacks %s\n%s", want, got)
		}
	}
}

// TestSearchBodyWithoutPositiveTermIsFilterOnly verifies that empty text and an
// exclusion alone run in filter context and do not sort by score.
func TestSearchBodyWithoutPositiveTermIsFilterOnly(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"", "-red"} {
		spec := specOf(text, func(query *search.QueryBuilder) {
		})

		got := compact(t, opensearchsearch.SearchBody(testSchema, spec, 10, 0))

		if !strings.Contains(got, `"filter":[{"match_all":{}}]`) || strings.Contains(got, `"_score":`) || strings.Contains(got, `"must":`) {
			t.Errorf("body of %q is not filter-only\n%s", text, got)
		}
	}
}

// TestSearchBodyConditions verifies operators, the operand type's field, and
// AND binding tighter than OR.
func TestSearchBodyConditions(t *testing.T) {
	t.Parallel()

	spec := specOf("", func(query *search.QueryBuilder) {
		query.Where("category", "Shoes")
		query.Where("price", "<=", 100)
		query.Where("title", "!=", "x")
		query.OrWhere("category", true)
		query.WhereIn("price", 1, 2)
	})

	got := compact(t, opensearchsearch.SearchBody(testSchema, spec, 10, 0))

	for _, want := range []string{
		`{"term":{"strings.category":"Shoes"}}`,
		`{"range":{"numbers.price":{"lte":100}}}`,
		`"bool":{"filter":[{"exists":{"field":"strings.title.keyword"}}],"must_not":[{"term":{"strings.title.keyword":"x"}}]}`,
		`{"term":{"booleans.category":true}}`,
		`{"terms":{"numbers.price":[1,2]}}`,
		`"minimum_should_match":1,"should":[{"bool":{"filter":[{"term":{"strings.category":"Shoes"}}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("body lacks %s\n%s", want, got)
		}
	}
}

// TestMixedNotInCannotMatchAnyScalar checks every pair of normalized scalar
// kinds in both operand orders. No stored scalar can satisfy typed inequalities
// against two different kinds, even when it differs from every listed value.
func TestMixedNotInCannotMatchAnyScalar(t *testing.T) {
	t.Parallel()

	for _, values := range [][]any{
		{float64(1), "red"}, {"red", float64(1)},
		{true, "red"}, {"red", true},
		{float64(1), true}, {true, float64(1)},
	} {
		spec := specOf("", func(query *search.QueryBuilder) {
			query.WhereNotIn("category", values...)
		})

		body := compact(t, opensearchsearch.SearchBody(testSchema, spec, 10, 0))

		if !strings.Contains(body, `"match_none":{}`) {
			t.Fatalf("NOT IN %v compiled as %s; want match_none", values, body)
		}
	}
}

// TestNumericNotInRequiresNumericPresence ensures a homogeneous numeric list
// does not admit string documents merely because their keyword field exists.
func TestNumericNotInRequiresNumericPresence(t *testing.T) {
	t.Parallel()

	spec := specOf("", func(query *search.QueryBuilder) {
		query.WhereNotIn("price", 1, 2)
	})

	body := compact(t, opensearchsearch.SearchBody(testSchema, spec, 10, 0))

	if !strings.Contains(body, `"exists":{"field":"numbers.price"}`) {
		t.Fatalf("numeric NOT IN compiled as %s; want numeric presence", body)
	}
}
