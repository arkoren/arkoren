package opensearchsearch_test

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
	"arkoren.dev/driver/opensearch/opensearchsearch"
)

// TestMultiwordTermMatchesAcrossSearchableFields checks native analysis and
// Boolean matching, not only the JSON query shape sent by the adapter.
func TestMultiwordTermMatchesAcrossSearchableFields(t *testing.T) {
	t.Parallel()

	_, original, driver := integrationEngine(t)
	index := search.NewIndex(original.Name(), func(record item) string {
		return record.ID
	}, func(index *search.IndexBuilder) {
		index.Searchable("title", "value")
	})

	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := engine.Put(t.Context(), index, item{ID: "a", Title: "red", Value: "shoes"}); err != nil {
		t.Fatal(err)
	}

	if got := matching(t, engine, search.NewQuery(index, "red-shoes")); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("matching IDs=%v; want [a]", got)
	}
}

// TestMixedNotInIgnoresOperandOrder checks public memory/native parity: NOT IN
// requires every typed inequality to hold. A string cannot satisfy numeric !=,
// so neither operand order matches either string document, including blue.
func TestMixedNotInIgnoresOperandOrder(t *testing.T) {
	t.Parallel()

	engine, index, _ := integrationEngine(t)
	inMemory := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(index)
	})

	for _, engine := range []*search.Engine{inMemory, engine} {
		if err := engine.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}

		if err := engine.Put(t.Context(), index, item{ID: "a", Title: "doc", Value: "blue"}, item{ID: "b", Title: "doc", Value: "red"}); err != nil {
			t.Fatal(err)
		}

		query := search.NewQuery(index, "", func(query *search.QueryBuilder) {
			query.WhereNotIn("value", []any{float64(1), "red"}...)
		})

		if got := matching(t, engine, query); len(got) != 0 {
			t.Fatalf("number-first NOT IN matched %v; want none", got)
		}

		reversed := search.NewQuery(index, "", func(query *search.QueryBuilder) {
			query.WhereNotIn("value", []any{"red", float64(1)}...)
		})

		if got := matching(t, engine, reversed); len(got) != 0 {
			t.Fatalf("string-first NOT IN matched %v; want none", got)
		}
	}
}

// TestMixedBooleanStringNotInMatchesNothing checks two distinct scalar kinds.
// Neither order can satisfy the typed conjunction.
func TestMixedBooleanStringNotInMatchesNothing(t *testing.T) {
	t.Parallel()

	engine, index, _ := integrationEngine(t)

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := engine.Put(t.Context(), index, item{ID: "a", Title: "doc", Value: "blue"}); err != nil {
		t.Fatal(err)
	}

	for _, values := range [][]any{{true, "red"}, {"red", true}} {
		query := search.NewQuery(index, "", func(query *search.QueryBuilder) {
			query.WhereNotIn("value", values...)
		})

		if got := matching(t, engine, query); len(got) != 0 {
			t.Fatalf("NOT IN %v matched %v; want none", values, got)
		}
	}
}

// TestNumericNotInDoesNotMatchStrings checks the homogeneous list's presence
// guard against the actual keyword mapping, not just compiled request text.
func TestNumericNotInDoesNotMatchStrings(t *testing.T) {
	t.Parallel()

	engine, index, _ := integrationEngine(t)

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := engine.Put(t.Context(), index, item{ID: "a", Title: "doc", Value: "blue"}); err != nil {
		t.Fatal(err)
	}

	query := search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.WhereNotIn("value", 1, 2)
	})

	if got := matching(t, engine, query); len(got) != 0 {
		t.Fatalf("numeric NOT IN matched %v; want none", got)
	}
}

// item is the document type of the integration tests. Value holds whatever a
// test wants to store in the field.
type item struct {
	// ID is the stable document key checked in search results.
	ID string `json:"id"`

	// Title supplies text for indexing and query assertions.
	Title string `json:"title"`

	// Value permits mixed JSON types for mapping and comparison checks.
	Value any `json:"value,omitempty"`
}

// itemIndex returns an index called name over items whose value is filterable.
func itemIndex(name string) search.Index[item] {
	return search.NewIndex(name, func(item item) string {
		return item.ID
	}, func(index *search.IndexBuilder) {
		index.Searchable("title")
		index.Filterable("value")
	})
}

// integrationEngine returns a started driver with the prefix of these tests and
// a synced engine over a uniquely named index that is dropped afterwards.
func integrationEngine(t *testing.T) (*search.Engine, search.Index[item], *opensearchsearch.Driver) {
	t.Helper()

	if testing.Short() {
		t.Skip("needs the OpenSearch service of compose.yaml")
	}

	driver := newTestDriver(t, func(engine *opensearchsearch.Builder) {
		engine.IndexPrefix("arkoren_extra_")
	})

	index := itemIndex("it_" + strings.ToLower(rand.Text())[:16])
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(index)
	})
	t.Cleanup(func() {
		if err := engine.Drop(context.WithoutCancel(t.Context()), index); err != nil {
			t.Errorf("Drop: %v", err)
		}
	})

	return engine, index, driver
}

// matching returns the IDs of the first page of query, sorted by the contract.
func matching(t *testing.T, engine *search.Engine, query search.Query[item]) []string {
	t.Helper()

	page, err := engine.Search(t.Context(), query, pagination.PageParams{
		Page:    1,
		PerPage: 50,
	})

	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	ids := make([]string, len(page.Items))

	for i, hit := range page.Items {
		ids[i] = hit.ID
	}

	return ids
}

// TestMistypedConditionsMatchNothing verifies that a number condition does not
// match string values or documents without the field, "!=" included. The scalar
// parity suite also exercises the reverse direction and boolean comparisons.
func TestMistypedConditionsMatchNothing(t *testing.T) {
	t.Parallel()

	engine, index, _ := integrationEngine(t)

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if err := engine.Put(t.Context(), index,
		item{
			ID:    "num",
			Title: "doc",
			Value: 15,
		},
		item{
			ID:    "str",
			Title: "doc",
			Value: "abc",
		},
		item{
			ID:    "none",
			Title: "doc",
		}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	number := search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.Where("value", 5)
		query.Where("value", "!=", 9)
		query.Where("value", ">", 1)
	})

	if got := matching(t, engine, number); len(got) != 0 {
		t.Errorf("number conditions matched %v, want nothing", got)
	}

	if got := matching(t, engine, search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.Where("value", "!=", 99)
	})); len(got) != 1 || got[0] != "num" {
		t.Errorf("number != matched %v, want only num", got)
	}
}

// TestMixedNotInCompilesToMatchNone observes the public request protocol for an
// impossible typed conjunction. Even a scalar unequal to both operands cannot
// satisfy inequalities with two different kinds.
func TestMixedNotInCompilesToMatchNone(t *testing.T) {
	t.Parallel()

	bodies := make(chan string, 1)
	driver := protocolDriver(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			return // Ignore SDK seed readiness probes, not application requests.
		}

		body, err := io.ReadAll(request.Body)

		if err != nil {
			t.Error(err)
		}

		bodies <- string(body)

		if _, err := writer.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`)); err != nil {
			t.Error(err)
		}
	})

	_, err := driver.Search(t.Context(), search.Schema{Name: "docs", Searchable: []string{"title"}, Filterable: []string{"category"}}, search.Spec{
		Where: []search.Condition{{Field: "category", Operator: search.OpNotIn, Values: []any{float64(1), "red"}}},
	}, 10, 0)

	if err != nil {
		t.Fatal(err)
	}

	if body := <-bodies; !strings.Contains(body, `"match_none":{}`) {
		t.Fatalf("native filter=%s; want an impossible mixed-kind NOT IN", body)
	}
}

// TestSearchRejectsTimedOutResponse checks HTTP success does not disguise an
// incomplete search window or total as a complete contract result.
func TestSearchRejectsTimedOutResponse(t *testing.T) {
	t.Parallel()

	driver := protocolDriver(t, func(writer http.ResponseWriter, _ *http.Request) {
		if _, err := writer.Write([]byte(`{"timed_out":true,"_shards":{"failed":0},"hits":{"total":{"value":0},"hits":[]}}`)); err != nil {
			t.Error(err)
		}
	})

	_, err := driver.Search(t.Context(), search.Schema{Name: "docs", Searchable: []string{"title"}}, search.Spec{}, 10, 0)

	if err == nil {
		t.Fatal("Search accepted an incomplete native response")
	}
}

// TestSearchRejectsFailedShardResponse prevents a successful HTTP status from
// concealing missing shard results in the reported window or exact hit count.
func TestSearchRejectsFailedShardResponse(t *testing.T) {
	t.Parallel()

	driver := protocolDriver(t, func(writer http.ResponseWriter, _ *http.Request) {
		if _, err := writer.Write([]byte(`{"timed_out":false,"_shards":{"failed":1},"hits":{"total":{"value":0},"hits":[]}}`)); err != nil {
			t.Error(err)
		}
	})

	_, err := driver.Search(t.Context(), search.Schema{Name: "docs", Searchable: []string{"title"}}, search.Spec{}, 10, 0)

	if err == nil {
		t.Fatal("Search accepted missing shard results")
	}
}

// TestSearchConjoinsWordsAcrossFieldAlternatives verifies the exported native
// query protocol represents two word requirements, rather than one field-bound
// multiword match. Backend integration separately checks resulting document IDs.
func TestSearchConjoinsWordsAcrossFieldAlternatives(t *testing.T) {
	t.Parallel()

	bodies := make(chan map[string]any, 1)
	driver := protocolDriver(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			return // Native SDK seed health checks are not search requests.
		}

		var body map[string]any

		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
		}

		bodies <- body

		if _, err := writer.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`)); err != nil {
			t.Error(err)
		}
	})

	_, err := driver.Search(t.Context(), search.Schema{Name: "docs", Searchable: []string{"title", "category"}}, search.Spec{
		Text: []search.Term{{Value: "red-shoes", Prefix: true}},
	}, 10, 0)

	if err != nil {
		t.Fatal(err)
	}

	body := <-bodies
	query := body["query"].(map[string]any)["bool"].(map[string]any)
	term := query["must"].([]any)[0].(map[string]any)["bool"].(map[string]any)

	if words, ok := term["must"].([]any); !ok || len(words) != 2 {
		t.Fatalf("native term=%v; want two independently required word clauses", term)
	}
}

// scalarEngines puts the same JSON documents in isolated memory and native
// indexes. All assertions use public queries, not the native compiler's shape.
func scalarEngines(t *testing.T, docs ...map[string]any) ([]*search.Engine, search.Index[map[string]any]) {
	t.Helper()

	if testing.Short() {
		t.Skip("needs the OpenSearch service of compose.yaml")
	}

	driver := newTestDriver(t, func(engine *opensearchsearch.Builder) {
		engine.IndexPrefix("arkoren_scalar_")
	})

	index := search.NewIndex("it_"+strings.ToLower(rand.Text())[:16], func(document map[string]any) string {
		return document["id"].(string)
	}, func(index *search.IndexBuilder) {
		index.Searchable("title")
		index.Filterable("value")
		index.Sortable("value")
	})

	register := func(engine *search.Builder) {
		engine.Indexes(index)
	}
	engines := []*search.Engine{search.New(memorysearch.New(), register), search.New(driver, register)}

	for _, engine := range engines {
		t.Cleanup(func() {
			if err := engine.Drop(context.Background(), index); err != nil {
				t.Error(err)
			}
		})

		if err := engine.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}

		if err := engine.Put(t.Context(), index, docs...); err != nil {
			t.Fatal(err)
		}
	}

	return engines, index
}

// scalarQuery exercises every public comparison entry point with normalized
// facade operands. Membership must not bypass the same kind checks as Where.
func scalarQuery(index search.Index[map[string]any], operator search.Operator, value any) search.Query[map[string]any] {
	return search.NewQuery(index, "", func(query *search.QueryBuilder) {
		switch operator {
		case search.OpIn:
			query.WhereIn("value", value)
		case search.OpNotIn:
			query.WhereNotIn("value", value)
		default:
			query.Where("value", operator, value)
		}
	})
}

// requireScalarMatches checks exact ordered IDs in both backends, including
// explicitly empty expectations. Neither backend can silently define the answer.
func requireScalarMatches(t *testing.T, engines []*search.Engine, query search.Query[map[string]any], want []string) {
	t.Helper()

	for backend, engine := range engines {
		page, err := engine.Search(t.Context(), query, pagination.PageParams{Page: 1, PerPage: 100})

		if err != nil {
			t.Fatal(err)
		}

		ids := page.Items.Map(func(_ int, hit search.Hit[map[string]any]) string {
			return hit.ID
		})

		if !slices.Equal(ids, want) {
			t.Errorf("backend %d: query %+v matched %v; want %v", backend, query.Inspect().Where, ids, want)
		}
	}
}

// rejectScalarOperands covers equality, inequality, IN, and homogeneous NOT IN
// for one stored kind and incompatible operands, including the original seven
// failing public probes. Missing and null are checked separately below.
func rejectScalarOperands(t *testing.T, stored any, operands ...any) {
	t.Helper()

	engines, index := scalarEngines(t, map[string]any{"id": "a", "title": "doc", "value": stored})

	for _, value := range operands {
		for _, operator := range []search.Operator{search.OpEqual, search.OpNotEqual, search.OpIn, search.OpNotIn} {
			requireScalarMatches(t, engines, scalarQuery(index, operator, value), nil)
		}
	}
}

// TestScalarNumbersRejectStrings retains the numeric 15 versus string "15"
// equality probe and the !=/NOT IN "absent" probes that previously matched it.
func TestScalarNumbersRejectStrings(t *testing.T) {
	t.Parallel()

	rejectScalarOperands(t, 15, "15", "absent")
}

// TestScalarBooleansRejectStrings retains true versus "true" and also checks
// negation and membership cannot admit a boolean through a keyword spelling.
func TestScalarBooleansRejectStrings(t *testing.T) {
	t.Parallel()

	rejectScalarOperands(t, true, "true", "absent")
}

// TestScalarStringsRejectBooleans retains the "true" versus true equality
// probe and the !=/NOT IN false probes, plus their adjacent IN cases.
func TestScalarStringsRejectBooleans(t *testing.T) {
	t.Parallel()

	rejectScalarOperands(t, "true", true, false)
}

// TestScalarStringsRejectNumbers covers the reverse string/number direction,
// including numeric-looking strings that must not acquire a numeric index entry.
func TestScalarStringsRejectNumbers(t *testing.T) {
	t.Parallel()

	rejectScalarOperands(t, "15", 15, 16)
}

// TestScalarNumbersRejectBooleans prevents native truth-value or keyword
// conversions from making a numeric document comparable with a boolean.
func TestScalarNumbersRejectBooleans(t *testing.T) {
	t.Parallel()

	rejectScalarOperands(t, 1, true, false)
}

// TestScalarBooleansRejectNumbers guards the reverse boolean/number direction,
// even for numeric operands that resemble common truth-value encodings.
func TestScalarBooleansRejectNumbers(t *testing.T) {
	t.Parallel()

	rejectScalarOperands(t, true, 1, 0)
}

// TestScalarAbsentAndNonscalarValuesNeverCompare requires the same empty
// result for missing, null, arrays and objects, including negated comparisons.
func TestScalarAbsentAndNonscalarValuesNeverCompare(t *testing.T) {
	t.Parallel()

	engines, index := scalarEngines(t,
		map[string]any{"id": "missing", "title": "doc"},
		map[string]any{"id": "null", "title": "doc", "value": nil},
		map[string]any{"id": "array", "title": "doc", "value": []any{15, "true", true}},
		map[string]any{"id": "object", "title": "doc", "value": map[string]any{"n": 15}},
	)

	for _, value := range []any{"true", 15, true} {
		for _, operator := range []search.Operator{search.OpEqual, search.OpNotEqual, search.OpIn, search.OpNotIn} {
			requireScalarMatches(t, engines, scalarQuery(index, operator, value), nil)
		}
	}
}

// TestScalarCompatibleComparisonsAndRanges preserves positive matching and
// case folding as well as numeric and string ranges; fidelity must not be fixed
// by making every comparison return an empty result.
func TestScalarCompatibleComparisonsAndRanges(t *testing.T) {
	t.Parallel()

	engines, index := scalarEngines(t,
		map[string]any{"id": "number", "title": "doc", "value": 15},
		map[string]any{"id": "string", "title": "doc", "value": "Blue"},
		map[string]any{"id": "boolean", "title": "doc", "value": true},
	)

	for _, operator := range []search.Operator{search.OpEqual, search.OpIn} {
		requireScalarMatches(t, engines, scalarQuery(index, operator, 15), []string{"number"})
		requireScalarMatches(t, engines, scalarQuery(index, operator, "BLUE"), []string{"string"})
		requireScalarMatches(t, engines, scalarQuery(index, operator, true), []string{"boolean"})
	}

	for _, operator := range []search.Operator{search.OpNotEqual, search.OpNotIn} {
		requireScalarMatches(t, engines, scalarQuery(index, operator, 16), []string{"number"})
		requireScalarMatches(t, engines, scalarQuery(index, operator, "red"), []string{"string"})
		requireScalarMatches(t, engines, scalarQuery(index, operator, false), []string{"boolean"})
	}

	requireScalarMatches(t, engines, scalarQuery(index, search.OpGreater, 14), []string{"number"})
	requireScalarMatches(t, engines, scalarQuery(index, search.OpLess, "red"), []string{"string"})
}

// TestScalarMixedMembershipKeepsTypedConjunctionAndDisjunction checks both
// operand orders over every scalar kind, rather than only over string documents.
func TestScalarMixedMembershipKeepsTypedConjunctionAndDisjunction(t *testing.T) {
	t.Parallel()

	engines, index := scalarEngines(t,
		map[string]any{"id": "number", "title": "doc", "value": 15},
		map[string]any{"id": "string", "title": "doc", "value": "blue"},
		map[string]any{"id": "boolean", "title": "doc", "value": true},
	)

	for _, values := range [][]any{{15, "blue", true}, {true, "blue", 15}} {
		query := search.NewQuery(index, "", func(query *search.QueryBuilder) {
			query.WhereNotIn("value", values...)
		})

		requireScalarMatches(t, engines, query, nil)

		query = search.NewQuery(index, "", func(query *search.QueryBuilder) {
			query.WhereIn("value", values...)
		})

		requireScalarMatches(t, engines, query, []string{"boolean", "number", "string"})
	}
}

// TestScalarBooleanSortKeepsFalseAndMissingDistinct verifies the boolean
// comparison path also supplies sort keys, with missing values last both ways.
func TestScalarBooleanSortKeepsFalseAndMissingDistinct(t *testing.T) {
	t.Parallel()

	engines, index := scalarEngines(t,
		map[string]any{"id": "yes", "title": "doc", "value": true},
		map[string]any{"id": "no", "title": "doc", "value": false},
		map[string]any{"id": "absent", "title": "doc"},
	)

	query := search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.OrderBy("value")
	})

	requireScalarMatches(t, engines, query, []string{"no", "yes", "absent"})

	query = search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.OrderByDesc("value")
	})

	requireScalarMatches(t, engines, query, []string{"yes", "no", "absent"})
}

// TestRawSourceRoundTripsWithoutReservedNames checks raw driver results retain
// original bytes, IDs, and fields whose names resemble the physical wrapper.
// The native _source may differ; that difference must not escape Driver.Search.
func TestRawSourceRoundTripsWithoutReservedNames(t *testing.T) {
	t.Parallel()

	_, index, driver := integrationEngine(t)

	if err := driver.Sync(t.Context(), []search.Schema{index.Schema()}); err != nil {
		t.Fatal(err)
	}

	source := []byte(` {
 "id":"body-id", "title":"red shoes", "value":15,
 "source":{"strings":true}, "strings":false, "numbers":"15",
 "booleans":1, "arkoren_id":"user", "_id":"user", "_source":"user",
 "value.n":"user", "title.keyword":"user", "strings.value":"user"
 } `)

	if err := driver.Put(t.Context(), index.Schema(), []search.Document{{ID: "native-id", Source: source}}); err != nil {
		t.Fatal(err)
	}

	query := search.NewQuery(index, "red shoes")
	matches, err := driver.Search(t.Context(), index.Schema(), query.Inspect(), 10, 0)

	if err != nil {
		t.Fatal(err)
	}

	if len(matches.Items) != 1 {
		t.Fatalf("raw matches=%+v; want one", matches)
	}

	if matches.Items[0].ID != "native-id" || string(matches.Items[0].Source) != string(source) {
		t.Fatalf("raw document=%+v; original source or external ID changed", matches.Items[0])
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/arkoren_extra_"+index.Name()+"/_doc/native-id", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := driver.Client().Client.Request(request)

	if err != nil {
		t.Fatal(err)
	}

	_ = response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("native document ID status=%d; want 200", response.StatusCode)
	}
}
