package postgressearch_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/searchtest"
	"arkoren.dev/driver/postgres/postgressearch"
)

// note is the document type of the PostgreSQL-specific tests.
type note struct {
	// ID is the stable document key returned in search matches.
	ID string `json:"id"`

	// Title supplies the first searchable field for word and phrase checks.
	Title string `json:"title"`

	// Body supplies a separate searchable field to test phrase boundaries.
	Body string `json:"body"`

	// Tag holds mixed JSON types to exercise typed filtering and ordering.
	Tag any `json:"tag"`
}

// noteIndex returns an index with a unique name over note.
func noteIndex() search.Index[note] {
	return search.NewIndex(uniqueInstance(), func(note note) string {
		return note.ID
	}, func(index *search.IndexBuilder) {
		index.Searchable("title", "body")
		index.Filterable("tag")
		index.Sortable("tag")
	})
}

// notesEngine returns an engine over a freshly migrated and synced index.
func notesEngine(t *testing.T) (*search.Engine, search.Index[note]) {
	t.Helper()

	index := noteIndex()
	engine := search.New(migratedDriver(t, integration(t)), func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	return engine, index
}

// found returns the sorted IDs that text matches.
func found(t *testing.T, engine *search.Engine, index search.Index[note], text string) []string {
	t.Helper()

	page, err := engine.Search(t.Context(), search.NewQuery(index, text), pagination.PageParams{
		Page:    1,
		PerPage: 100,
	})

	if err != nil {
		t.Fatalf("Search(%q): %v", text, err)
	}

	ids := make([]string, len(page.Items))

	for i, hit := range page.Items {
		ids[i] = hit.ID
	}

	slices.Sort(ids)

	return ids
}

// TestPunctuationSplitsWords checks the contract's letter-and-digit tokenization.
// URL, email, version, and hyphen punctuation must not hide individual words.
func TestPunctuationSplitsWords(t *testing.T) {
	t.Parallel()

	engine, index := notesEngine(t)

	if err := engine.Put(t.Context(), index,
		note{
			ID:    "a",
			Title: "red-shoes at https://example.com/path_name",
			Body:  "v1.5 e-mail a@b.org",
		}); err != nil {
		t.Fatal(err)
	}

	for _, text := range []string{"red", "shoes", "red-shoes", `"red shoes"`, "example", "com", "path", "name", "v1", "5", "e", "mail", "org", "b"} {
		if got := found(t, engine, index, text); !slices.Equal(got, []string{"a"}) {
			t.Errorf("text %q found %v, want [a]", text, got)
		}
	}
}

// TestPhraseDoesNotSpanFields checks the positional gap between indexed fields.
// Separate terms may match across fields, but one phrase must remain within one.
func TestPhraseDoesNotSpanFields(t *testing.T) {
	t.Parallel()

	engine, index := notesEngine(t)

	if err := engine.Put(t.Context(), index,
		note{
			ID:    "a",
			Title: "big red",
			Body:  "shoes sale",
		},
		note{
			ID:    "b",
			Title: "big",
			Body:  "red shoes",
		}); err != nil {
		t.Fatal(err)
	}

	if got := found(t, engine, index, `"red shoes"`); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("phrase found %v, want only b", got)
	}

	if got := found(t, engine, index, "red shoes"); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("words found %v, want a and b", got)
	}
}

// TestHostileQueryTextIsHarmless exercises SQL and tsquery metacharacters as data.
// Such queries must not fail parsing or prevent later ordinary searches.
func TestHostileQueryTextIsHarmless(t *testing.T) {
	t.Parallel()

	engine, index := notesEngine(t)

	if err := engine.Put(t.Context(), index, note{
		ID:    "a",
		Title: "plain words",
		Body:  "o'brien",
	}); err != nil {
		t.Fatal(err)
	}

	for _, text := range []string{
		`a & b | !c`, `'quoted'`, `a:* b:A`, `(a <-> b)`, `\`, `"x' || pg_sleep(10) --"`, `plain'; DROP TABLE x; --`,
		`-"a & b"`, `:*`, `!`, `<->`, `ünïcode ☃ 日本語`, `o'brien`,
	} {
		if _, err := engine.Search(t.Context(), search.NewQuery(index, text), pagination.PageParams{
			Page:    1,
			PerPage: 10,
		}); err != nil {
			t.Errorf("Search(%q) error = %v", text, err)
		}
	}

	if got := found(t, engine, index, "o'brien"); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("o'brien found %v, want [a]", got)
	}
}

// TestMistypedAndMissingFilterValuesMatchNothing checks typed JSON comparisons.
// Negation must not admit null or mismatched types, and ordering remains stable.
func TestMistypedAndMissingFilterValuesMatchNothing(t *testing.T) {
	t.Parallel()

	engine, index := notesEngine(t)

	if err := engine.Put(t.Context(), index,
		note{
			ID:    "str",
			Title: "n",
			Tag:   "Hello",
		},
		note{
			ID:    "num",
			Title: "n",
			Tag:   5,
		},
		note{
			ID:    "flag",
			Title: "n",
			Tag:   true,
		},
		note{
			ID:    "null",
			Title: "n",
		},
	); err != nil {
		t.Fatal(err)
	}

	idsOf := func(query search.Query[note]) []string {
		t.Helper()

		page, err := engine.Search(t.Context(), query, pagination.PageParams{
			Page:    1,
			PerPage: 10,
		})

		if err != nil {
			t.Fatal(err)
		}

		ids := make([]string, len(page.Items))

		for i, hit := range page.Items {
			ids[i] = hit.ID
		}

		return ids
	}

	if got := idsOf(search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.Where("tag", "!=", "hello")
	})); len(got) != 0 {
		t.Errorf("tag != hello matched %v, want no document: the others hold other types or no value", got)
	}

	if got := idsOf(search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.Where("tag", 5)
	})); !slices.Equal(got, []string{"num"}) {
		t.Errorf("tag = 5 matched %v, want [num]", got)
	}

	if got := idsOf(search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.WhereNotIn[float64]("tag", 1, 2)
	})); !slices.Equal(got, []string{"num"}) {
		t.Errorf("tag not in numbers matched %v, want [num]", got)
	}

	// Numbers sort first, then strings, then booleans, then documents without a value.
	if got := idsOf(search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.OrderBy("tag")
	})); !slices.Equal(got, []string{"num", "str", "flag", "null"}) {
		t.Errorf("order by tag = %v, want [num str flag null]", got)
	}
}

// TestPutSplitsLargeBatches sends more documents than one INSERT chunk accepts.
// All chunks must be searchable, with totals capped at the contract limit.
func TestPutSplitsLargeBatches(t *testing.T) {
	t.Parallel()

	engine, index := notesEngine(t)

	docs := make([]note, 2500)

	for i := range docs {
		docs[i] = note{
			ID:    fmt.Sprintf("n%04d", i),
			Title: "bulk " + strings.Repeat("x", i%7),
			Body:  "chunked",
		}
	}

	if err := engine.Put(t.Context(), index, docs...); err != nil {
		t.Fatalf("Put: %v", err)
	}

	page, err := engine.Search(t.Context(), search.NewQuery(index, "chunked"), pagination.PageParams{
		Page:    1,
		PerPage: 10,
	})

	if err != nil {
		t.Fatal(err)
	}

	if page.Total != search.MaxHits {
		t.Fatalf("total = %d, want it capped at %d", page.Total, search.MaxHits)
	}
}

// TestSyncIsSafeInParallel exercises concurrent synchronization of one index.
// Drivers sharing its schema must serialize updates without reporting errors.
func TestSyncIsSafeInParallel(t *testing.T) {
	t.Parallel()

	engine, index := notesEngine(t)
	driver := engine.Driver().(*postgressearch.Driver)
	other := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(index)
	})

	errs := make(chan error, 8)

	for range 8 {
		go func() {
			errs <- other.Sync(t.Context())
		}()
	}

	for range 8 {
		if err := <-errs; err != nil {
			t.Fatalf("parallel Sync: %v", err)
		}
	}
}

// TestConformance runs the search conformance suite against a live server,
// over the tables of one migrated instance that every subtest shares; the
// suite isolates its subtests with unique index names.
func TestConformance(t *testing.T) {
	driver := migratedDriver(t, integration(t))

	searchtest.Run(t, func(*testing.T) search.Driver {
		return driver
	})
}
