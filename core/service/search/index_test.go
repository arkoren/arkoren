package search_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/service/search"
)

// ExampleIndexer demonstrates complete type-erased definition validation.
// Index already implements both methods required for engine registration.
func ExampleIndexer() {
	var definition search.Indexer = exampleProducts

	if err := definition.Err(); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(definition.Schema().Name)
	// Output: products
}

// ExampleIndex_Schema demonstrates independent field-list storage rather than
// a borrowed slice into the immutable index.
func ExampleIndex_Schema() {
	snapshot := exampleProducts.Schema()
	snapshot.Searchable[0] = "description"
	fmt.Println(exampleProducts.Schema().Searchable)
	// Output: [title]
}

// doc is the document type of the package tests.
type doc struct {
	// ID is the stable document identity used for indexing and result checks.
	ID string `json:"id"`
	// Title is the searchable display text used by query fixtures.
	Title string `json:"title"`
	// Group is the category used by filtering fixtures.
	Group string `json:"group"`
	// Price is the numeric value used by filtering and ordering fixtures.
	Price float64 `json:"price"`
}

// docID returns the stable identity used by package declaration tests.
func docID(document doc) string {
	return document.ID
}

// TestIndexSchemaIsSortedAndUnique checks repeated field declarations normalize at publication.
func TestIndexSchemaIsSortedAndUnique(t *testing.T) {
	t.Parallel()

	index := search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("title", "body")
		index.Filterable("price", "group", "price")
		index.Sortable("price")
		index.Searchable("body")
	})

	want := search.Schema{
		Name:       "docs",
		Searchable: []string{"body", "title"},
		Filterable: []string{"group", "price"},
		Sortable:   []string{"price"},
	}

	if got := index.Schema(); !got.Equal(want) {
		t.Fatalf("Schema() = %+v, want %+v", got, want)
	}

	if err := index.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}

	if !slices.Equal(index.SearchableValue(), want.Searchable) || !slices.Equal(index.FilterableValue(), want.Filterable) ||
		!slices.Equal(index.SortableValue(), want.Sortable) || index.Name() != "docs" {
		t.Fatalf("getters disagree with the schema: %v %v %v", index.SearchableValue(), index.FilterableValue(), index.SortableValue())
	}
}

// TestSchemaIgnoresSetterOrder checks equivalent declaration orders produce equal schemas.
func TestSchemaIgnoresSetterOrder(t *testing.T) {
	t.Parallel()

	firstSchema := search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("b", "a")
	}).Schema()

	secondSchema := search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("a")
		index.Searchable("b")
	}).Schema()

	if !firstSchema.Equal(secondSchema) {
		t.Fatalf("schemas differ: %+v vs %+v", firstSchema, secondSchema)
	}

	if firstSchema.Equal(search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("a")
	}).Schema()) {
		t.Fatal("schemas with different searchable fields are equal")
	}
}

// TestIndexInvalidNames checks an empty identity invalidates the definition.
func TestIndexInvalidNames(t *testing.T) {
	t.Parallel()

	wantInvalidIndex(t, "empty index name", search.NewIndex("", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	}))
}

// wantInvalidIndex checks malformed definitions retain the portable name sentinel.
// name keeps assertion diagnostics specific to the declaration being checked.
func wantInvalidIndex(t *testing.T, name string, index search.Index[doc]) {
	t.Helper()

	if err := index.Err(); !errors.Is(err, search.ErrInvalidName) {
		t.Errorf("%s: Err() = %v, want ErrInvalidName", name, err)
	}
}

// TestIndexRejectsUppercaseName checks identities use the portable lowercase syntax.
func TestIndexRejectsUppercaseName(t *testing.T) {
	t.Parallel()

	wantInvalidIndex(t, "uppercase index name", search.NewIndex("Docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	}))
}

// TestIndexRejectsHyphenatedName checks identities exclude nonportable separators.
func TestIndexRejectsHyphenatedName(t *testing.T) {
	t.Parallel()

	wantInvalidIndex(t, "hyphen in index name", search.NewIndex("my-docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	}))
}

// TestIndexRejectsLongName checks the identity length limit before driver use.
func TestIndexRejectsLongName(t *testing.T) {
	t.Parallel()

	wantInvalidIndex(t, "long index name", search.NewIndex(strings.Repeat("a", 65), docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	}))
}

// TestIndexRejectsInvalidSearchableField checks searchable field names are validated.
func TestIndexRejectsInvalidSearchableField(t *testing.T) {
	t.Parallel()

	wantInvalidIndex(t, "bad searchable", search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("Title")
	}))
}

// TestIndexRejectsInvalidFilterableField checks nested field paths are not portable names.
func TestIndexRejectsInvalidFilterableField(t *testing.T) {
	t.Parallel()

	wantInvalidIndex(t, "bad filterable", search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
		index.Filterable("a.b")
	}))
}

// TestIndexRejectsEmptySortableField checks sortable declarations cannot name an empty field.
func TestIndexRejectsEmptySortableField(t *testing.T) {
	t.Parallel()

	wantInvalidIndex(t, "bad sortable", search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
		index.Sortable("")
	}))
}

// TestIndexRequiresSearchableField checks filter-only definitions are incomplete.
func TestIndexRequiresSearchableField(t *testing.T) {
	t.Parallel()

	wantInvalidIndex(t, "no searchable field", search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		index.Filterable("price")
	}))
}

// TestIndexRequiresKeyFunction checks document identities require a supplied extractor.
func TestIndexRequiresKeyFunction(t *testing.T) {
	t.Parallel()

	wantInvalidIndex(t, "nil key function", search.NewIndex[doc]("docs", nil, func(index *search.IndexBuilder) {
		index.Searchable("title")
	}))
}

// TestIndexValidNames checks portable names at the accepted syntax boundaries.
func TestIndexValidNames(t *testing.T) {
	t.Parallel()

	index := search.NewIndex(strings.Repeat("a", 64), docID, func(index *search.IndexBuilder) {
		index.Searchable("title_2", "0", "_")
	})

	if err := index.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
}

// TestIndexCallbacksShareOneDraft verifies ordered edits and identity, including
// an initially incomplete declaration completed by the last callback.
func TestIndexCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	var retained *search.IndexBuilder
	var calls []int
	index := search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		retained = index
		calls = append(calls, 1)

		if index.Filterable("group") != index {
			t.Fatal("Filterable returned another draft")
		}
	}, func(index *search.IndexBuilder) {
		calls = append(calls, 2)

		if index != retained || index.Searchable("title") != index || index.Sortable("price") != index {
			t.Fatal("callbacks or setters received another draft")
		}
	})

	if index.Err() != nil || !slices.Equal(calls, []int{1, 2}) {
		t.Fatalf("callbacks = %v, Err = %v", calls, index.Err())
	}
}

// TestIndexPublicationDetachesFields verifies input, retained draft, getter,
// and schema ownership independently of the index's published definition.
func TestIndexPublicationDetachesFields(t *testing.T) {
	t.Parallel()

	fields := []string{"title", "body"}
	var retained *search.IndexBuilder
	index := search.NewIndex("docs", docID, func(index *search.IndexBuilder) {
		retained = index
		index.Searchable(fields...)
		index.Filterable(fields...)
		index.Sortable(fields...)

		fields[0] = "changed"
	})

	want := index.Schema()
	retained.Searchable("later")
	retained.Filterable("later")
	retained.Sortable("later")

	index.SearchableValue()[0] = "changed"
	index.FilterableValue()[0] = "changed"
	index.SortableValue()[0] = "changed"
	schema := index.Schema()
	schema.Searchable[0] = "changed"
	schema.Filterable[0] = "changed"
	schema.Sortable[0] = "changed"

	if !index.Schema().Equal(want) || !slices.Equal(want.Searchable, []string{"body", "title"}) {
		t.Fatalf("publication changed: %+v; initial %+v", index.Schema(), want)
	}
}

// TestIndexOmittedCallbacksRemainIncomplete preserves normal domain validation
// for omission and expanded nil/empty callback slices, rather than panicking.
func TestIndexOmittedCallbacksRemainIncomplete(t *testing.T) {
	t.Parallel()

	var omitted []func(*search.IndexBuilder)

	for _, index := range []search.Index[doc]{
		search.NewIndex("docs", docID),
		search.NewIndex("docs", docID, omitted...),
		search.NewIndex("docs", docID, []func(*search.IndexBuilder){}...),
	} {
		if !errors.Is(index.Err(), search.ErrInvalidName) {
			t.Fatalf("Err = %v, want missing searchable field", index.Err())
		}
	}
}

// TestIndexNilCallbackPanicsEvenWhenInvalid distinguishes callback misuse
// from invalid domain input, which remains an Index.Err result.
func TestIndexNilCallbackPanicsEvenWhenInvalid(t *testing.T) {
	t.Parallel()

	requirePanic(t, nil, func() {
		search.NewIndex[doc]("Bad", nil, nil)
	})
}
