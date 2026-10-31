package index

import (
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/search"
)

// TestQueryTextPutsPrefixWordLast checks that exclusions and phrases come
// before the plain words and the prefix word ends the query, whatever order
// the user typed them in.
func TestQueryTextPutsPrefixWordLast(t *testing.T) {
	t.Parallel()

	terms := []search.Term{
		{
			Value: "red",
		},
		{
			Value:   "boots",
			Exclude: true,
		},
		{
			Value:  "running shoes",
			Phrase: true,
		},
		{
			Value:  "wint",
			Prefix: true,
		},
		{
			Value:   "old socks",
			Exclude: true,
			Phrase:  true,
		},
	}

	got, err := queryText(terms)

	if err != nil {
		t.Fatal(err)
	}

	want := `-boots -"old socks" "running shoes" red wint`

	if got != want {
		t.Fatalf("queryText = %q, want %q", got, want)
	}
}

// TestQueryTextSplitsWordsLikeTheContract checks that terms are cut into
// letter and digit runs, so no punctuation of user input reaches the query.
func TestQueryTextSplitsWordsLikeTheContract(t *testing.T) {
	t.Parallel()

	terms := []search.Term{
		{
			Value: "red-shoes",
		},
		{
			Value: `it's`,
		},
		{
			Value:  "x-ra",
			Prefix: true,
		},
	}

	got, err := queryText(terms)

	if err != nil {
		t.Fatal(err)
	}

	if want := "red shoes it s x ra"; got != want {
		t.Fatalf("queryText = %q, want %q", got, want)
	}
}

// TestQueryTextRejectsMoreThanTenTerms checks that a query Meilisearch would
// silently truncate fails instead.
func TestQueryTextRejectsMoreThanTenTerms(t *testing.T) {
	t.Parallel()

	terms := make([]search.Term, 11)

	for i := range terms {
		terms[i] = search.Term{
			Value: "word",
		}
	}

	if _, err := queryText(terms); !errors.Is(err, search.ErrUnsupported) {
		t.Fatalf("queryText of 11 words error = %v, want ErrUnsupported", err)
	}

	if _, err := queryText(terms[:10]); err != nil {
		t.Fatalf("queryText of 10 words = %v, want no error", err)
	}
}

// TestQueryTextOfNothingIsEmpty checks that no terms make an empty query,
// which Meilisearch answers with every document.
func TestQueryTextOfNothingIsEmpty(t *testing.T) {
	t.Parallel()

	got, err := queryText(nil)

	if err != nil || got != "" {
		t.Fatalf("queryText(nil) = %q, %v, want an empty query", got, err)
	}
}

// TestFilterEscapesValues checks that quotes and backslashes in a value stay
// inside its string, so a value cannot add conditions.
func TestFilterEscapesValues(t *testing.T) {
	t.Parallel()

	got := filterExpression([]search.Condition{{
		Field:    "category",
		Operator: search.OpEqual,
		Value:    `x" OR "y" = "\`,
	}})

	want := `("category" = "x\" OR \"y\" = \"\\")`

	if got != want {
		t.Fatalf("filter = %s, want %s", got, want)
	}
}

// TestFilterJoinsAndBeforeOr checks that AND runs are grouped before the OR
// that follows them.
func TestFilterJoinsAndBeforeOr(t *testing.T) {
	t.Parallel()

	got := filterExpression([]search.Condition{
		{
			Field:    "a",
			Operator: search.OpEqual,
			Value:    "x",
		},
		{
			Field:    "b",
			Operator: search.OpEqual,
			Value:    true,
		},
		{
			Or:       true,
			Field:    "c",
			Operator: search.OpLess,
			Value:    1.5,
		},
	})

	want := `("a" = "x" AND "b" = true) OR ("c" < 1.5)`

	if got != want {
		t.Fatalf("filter = %s, want %s", got, want)
	}
}

// TestFilterNestsGroups checks that a group becomes a parenthesized
// sequence of its own.
func TestFilterNestsGroups(t *testing.T) {
	t.Parallel()

	got := filterExpression([]search.Condition{
		{
			Field:    "a",
			Operator: search.OpGreaterOrEqual,
			Value:    10.0,
		},
		{
			Group: []search.Condition{
				{
					Field:    "b",
					Operator: search.OpEqual,
					Value:    "x",
				},
				{
					Or:       true,
					Field:    "c",
					Operator: search.OpEqual,
					Value:    "y",
				},
			},
		},
	})

	want := `("a" >= 10 AND (("b" = "x") OR ("c" = "y")))`

	if got != want {
		t.Fatalf("filter = %s, want %s", got, want)
	}
}

// TestFilterNegationRequiresAValue checks that != and NOT IN also demand a
// non-null value, because Meilisearch would otherwise match documents
// without the field.
func TestFilterNegationRequiresAValue(t *testing.T) {
	t.Parallel()

	notEqual := filterExpression([]search.Condition{{
		Field:    "f",
		Operator: search.OpNotEqual,
		Value:    "x",
	}})

	if want := `(("f" EXISTS AND "f" IS NOT NULL AND "f" != "x"))`; notEqual != want {
		t.Fatalf("!= filter = %s, want %s", notEqual, want)
	}

	notIn := filterExpression([]search.Condition{{
		Field:    "f",
		Operator: search.OpNotIn,
		Values:   []any{"x", 2.0},
	}})

	if want := `(("f" EXISTS AND "f" IS NOT NULL AND "f" NOT IN ["x", 2]))`; notIn != want {
		t.Fatalf("NOT IN filter = %s, want %s", notIn, want)
	}
}

// TestFilterInListsValues checks the IN operator's value list.
func TestFilterInListsValues(t *testing.T) {
	t.Parallel()

	got := filterExpression([]search.Condition{{
		Field:    "f",
		Operator: search.OpIn,
		Values:   []any{"a", "b"},
	}})

	if want := `("f" IN ["a", "b"])`; got != want {
		t.Fatalf("filter = %s, want %s", got, want)
	}
}

// TestFilterOfNoConditionsIsEmpty checks that no conditions leave the filter
// out of the request.
func TestFilterOfNoConditionsIsEmpty(t *testing.T) {
	t.Parallel()

	if got := filterExpression(nil); got != "" {
		t.Fatalf("filter = %q, want empty", got)
	}
}

// TestSortRulesPutDirectionAfterField checks the sort rule syntax.
func TestSortRulesPutDirectionAfterField(t *testing.T) {
	t.Parallel()

	got := strings.Join(sortRules([]search.Order{
		{
			Field: "price",
		},
		{
			Field: "title",
			Desc:  true,
		},
	}), ",")

	if want := "price:asc,title:desc"; got != want {
		t.Fatalf("sortRules = %s, want %s", got, want)
	}
}
