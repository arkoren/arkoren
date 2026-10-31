package search_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
)

// TestEnginePutStopsBetweenKeyCallbacks checks cancellation from application
// code prevents another document callback and leaves the backend untouched.
func TestEnginePutStopsBetweenKeyCallbacks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	calls := 0
	index := search.NewIndex("docs", func(document doc) string {
		calls++
		cancel()

		return document.ID
	}, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	driver := memorysearch.New()
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	err := engine.Put(ctx, index, doc{ID: "a"}, doc{ID: "b"})

	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("Put = %v, key calls = %d, want cancellation after one callback", err, calls)
	}

	matches, err := driver.Search(t.Context(), index.Schema(), search.Spec{}, 10, 0)

	if err != nil || matches.Total != 0 {
		t.Fatalf("canceled encoding changed backend: %+v, %v", matches, err)
	}
}

// cancelingSearchDriver cancels after the provider has prepared its results.
// The facade must still observe cancellation before publishing decoded hits.
type cancelingSearchDriver struct {
	// Driver supplies ordinary memory behavior for setup and searching.
	search.Driver

	// cancel models cancellation at the provider-to-facade boundary.
	cancel context.CancelFunc
}

// Search completes the driver read, then cancels before returning its result.
// No private engine state or timing assumptions are involved.
func (driver cancelingSearchDriver) Search(
	ctx context.Context,
	schema search.Schema,
	spec search.Spec,
	limit, offset int,
) (search.Matches, error) {
	matches, err := driver.Driver.Search(ctx, schema, spec, limit, offset)
	driver.cancel()

	return matches, err
}

// TestEngineSearchObservesCancellationAfterDriver checks completed provider
// results do not bypass cancellation during the facade's decoding phase.
func TestEngineSearchObservesCancellationAfterDriver(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver := cancelingSearchDriver{Driver: memorysearch.New(), cancel: cancel}
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(testIndex)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := engine.Put(t.Context(), testIndex, doc{ID: "a"}); err != nil {
		t.Fatal(err)
	}

	_, err := engine.Search(ctx, search.NewQuery(testIndex, ""), pagination.PageParams{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Search = %v, want cancellation after provider read", err)
	}
}

// TestEngineDeleteObservesCancellationBeforeIDs checks canceled calls stop
// before starting per-document ID validation and never delete stored data.
func TestEngineDeleteObservesCancellationBeforeIDs(t *testing.T) {
	t.Parallel()

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(testIndex)
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := engine.Delete(ctx, testIndex, "invalid/id"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete = %v, want context.Canceled", err)
	}
}

// TestSearchCollectionItemsKeepHitMetadata checks local collection operations
// retain typed hit identity, score and order without changing backend counts.
func TestSearchCollectionItemsKeepHitMetadata(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	driver.matches = &search.Matches{
		Total: 30,
		Items: []search.Match{
			{Document: search.Document{ID: "b", Source: []byte(`{"title":"second","price":20}`)}, Score: 2.5},
			{Document: search.Document{ID: "a", Source: []byte(`{"title":"first","price":10}`)}, Score: 1.5},
		},
	}
	engine := syncedEngine(t, driver)

	page, err := engine.Search(t.Context(), search.NewQuery(testIndex, ""), pagination.PageParams{Page: 2, PerPage: 2})

	if err != nil {
		t.Fatal(err)
	}

	before := driver.calls
	ids := page.Items.Map(func(_ int, hit search.Hit[doc]) string {
		return hit.ID
	})

	groups := page.Items.GroupBy(func(_ int, hit search.Hit[doc]) string {
		return hit.Document.Title
	})

	filtered := page.Items.Filter(func(_ int, hit search.Hit[doc]) bool {
		return hit.Document.Price >= 20
	})

	if !slices.Equal(ids, []string{"b", "a"}) || filtered[0].Score != 2.5 {
		t.Fatalf("IDs = %v, filtered = %v", ids, filtered)
	}

	if groups["first"][0].ID != "a" || groups["first"][0].Score != 1.5 {
		t.Fatalf("groups = %v", groups)
	}

	filtered[0].ID = "changed"
	driver.matches.Items[0].ID = "driver-changed"

	if page.Items[0].ID != "b" || driver.calls != before {
		t.Fatalf("local operations changed the source or invoked search: %+v", page)
	}

	if page.Total != 30 || page.LastPage != 15 {
		t.Fatalf("counts = %+v", page)
	}

	if page.CurrentPage != 2 || page.PerPage != 2 {
		t.Fatalf("window = %+v", page)
	}
}

// TestEngineRegistrationRejectsNilKey verifies complete definition validation
// prevents synchronization, even when the index's raw schema is valid.
func TestEngineRegistrationRejectsNilKey(t *testing.T) {
	t.Parallel()

	index := search.NewIndex[doc]("docs", nil, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	driver := memorysearch.New()
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := engine.Sync(t.Context()); !errors.Is(err, search.ErrInvalidName) {
		t.Fatalf("Sync = %v, want ErrInvalidName", err)
	}

	if err := driver.Check(t.Context(), []search.Schema{index.Schema()}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("invalid registration changed backend: %v", err)
	}
}

// TestEngineSearchRejectsZeroQuery distinguishes an invalid declaration from
// an otherwise valid query targeting an unregistered index.
func TestEngineSearchRejectsZeroQuery(t *testing.T) {
	t.Parallel()

	engine := search.New(memorysearch.New())
	var query search.Query[doc]

	_, err := engine.Search(t.Context(), query, pagination.PageParams{})

	if !errors.Is(err, search.ErrInvalidQuery) {
		t.Fatalf("Search = %v, want ErrInvalidQuery", err)
	}
}

// exampleProduct supplies the small application-owned JSON shape used in these
// examples. Its envelope ID is selected explicitly by exampleProducts.
type exampleProduct struct {
	// ID names the document independently of its searchable fields.
	ID string `json:"id"`

	// Title is the text searched by the examples.
	Title string `json:"title"`

	// Category is the string field used for exact filters.
	Category string `json:"category"`

	// Price is the numeric field used for comparisons and ordering.
	Price float64 `json:"price"`
}

// exampleProducts is a shared immutable definition. Every runnable example
// creates its own driver, so the examples share no stored document state.
var exampleProducts = search.NewIndex("products", func(product exampleProduct) string {
	return product.ID
}, func(index *search.IndexBuilder) {
	index.Searchable("title")
	index.Filterable("category", "price")
	index.Sortable("price")
})

// ExampleEngine completes an isolated index, write, and typed search through
// public facades without network access or lifecycle hooks.
func ExampleEngine() {
	ctx := context.Background()
	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(exampleProducts)
	})

	if err := engine.Sync(ctx); err != nil {
		fmt.Println(err)

		return
	}

	if err := engine.Put(ctx, exampleProducts,
		exampleProduct{ID: "p1", Title: "Red shoes", Category: "shoes", Price: 80},
		exampleProduct{ID: "p2", Title: "Red boots", Category: "boots", Price: 150},
	); err != nil {
		fmt.Println(err)

		return
	}

	query := search.NewQuery(exampleProducts, "red", func(query *search.QueryBuilder) {
		query.Where("price", search.OpLessOrEqual, 100)
	})

	page, err := engine.Search(ctx, query, pagination.PageParams{PerPage: 10})

	if err != nil {
		fmt.Println(err)

		return
	}

	for _, hit := range page.Items {
		fmt.Println(hit.ID, hit.Document.Title, hit.Score, page.Total)
	}
	// Output: p1 Red shoes 1 1
}

// schemaEditingDriver simulates a driver that normalizes its input containers
// after synchronization. Its edits must not change engine registrations.
type schemaEditingDriver struct {
	// Driver supplies the full contract beneath the schema-editing boundary.
	search.Driver
}

// Sync consumes schemas and then edits their field storage. Tests observe
// registration integrity through later public engine operations only.
func (driver schemaEditingDriver) Sync(ctx context.Context, schemas []search.Schema) error {
	if err := driver.Driver.Sync(ctx, schemas); err != nil {
		return err
	}

	for _, schema := range schemas {
		schema.Searchable[0] = "changed"
		schema.Filterable[0] = "changed"
		schema.Sortable[0] = "changed"
	}

	return nil
}

// TestEngineSyncDetachesDriverSchemaFields verifies passing schemas across
// the driver boundary cannot rewrite the engine's published registrations.
func TestEngineSyncDetachesDriverSchemaFields(t *testing.T) {
	t.Parallel()

	driver := schemaEditingDriver{Driver: memorysearch.New()}
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(testIndex)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := engine.Put(t.Context(), testIndex, doc{ID: "a", Title: "alpha"}); err != nil {
		t.Fatalf("driver edits changed registration: %v", err)
	}
}

// sharedSchema deliberately returns shared field storage to characterize the
// engine's registration snapshot independently of immutable Index behavior.
type sharedSchema struct {
	// schema is application-owned storage that may change after registration.
	schema search.Schema
}

// Schema returns the original storage, not a copy. Engine construction must
// detach the field lists before publishing its registered schema.
//
//	index := sharedSchema{schema: search.Schema{Name: "products", Searchable: []string{"title"}}}
//	definition := index.Schema()
//	_ = definition
func (index sharedSchema) Schema() search.Schema {
	return index.schema
}

// Err validates the fixture's application-owned definition. These tests supply
// known valid schemas and exercise container ownership, not custom validation.
//
//	index := sharedSchema{schema: search.Schema{Name: "products", Searchable: []string{"title"}}}
//	if err := index.Err(); err != nil {
//		return err
//	}
func (index sharedSchema) Err() error {
	return nil
}

// TestEngineConstructionSnapshotsSchemaFields verifies that registration
// detaches owned field storage without recursively cloning Indexer objects.
func TestEngineConstructionSnapshotsSchemaFields(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	index := sharedSchema{schema: search.Schema{
		Name: "products", Searchable: []string{"title"},
		Filterable: []string{"category"}, Sortable: []string{"price"},
	}}
	want := search.Schema{
		Name: "products", Searchable: []string{"title"},
		Filterable: []string{"category"}, Sortable: []string{"price"},
	}
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(index)
	})

	index.schema.Searchable[0] = "changed"
	index.schema.Filterable[0] = "changed"
	index.schema.Sortable[0] = "changed"

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !driver.synced[0][0].Equal(want) {
		t.Fatalf("published schema changed: %+v", driver.synced)
	}
}

// TestEngineConstructionCopiesAppendedIndexes proves capture and publication
// isolation, append order, callback cardinality and lazy driver invocation.
func TestEngineConstructionCopiesAppendedIndexes(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	other := search.NewIndex("other", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	input := []search.Indexer{testIndex}
	var retained *search.Builder
	calls := 0
	engine := search.New(driver, func(engine *search.Builder) {
		retained = engine
		calls++

		if engine.Indexes(input...) != engine {
			t.Fatal("Indexes changed receiver")
		}

		input[0] = nil
	}, func(engine *search.Builder) {
		calls++

		if engine != retained {
			t.Fatal("callbacks received different drafts")
		}

		engine.Indexes(other)
		engine.Indexes()
		engine.Indexes([]search.Indexer(nil)...)
	})
	retained.Indexes(nil)

	if calls != 2 || driver.calls != 0 {
		t.Fatalf("callbacks=%d driver calls=%d", calls, driver.calls)
	}

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := []search.Schema{other.Schema(), testIndex.Schema()}
	slices.SortFunc(want, func(left, right search.Schema) int {
		return strings.Compare(left.Name, right.Name)
	})

	if !slices.EqualFunc(driver.synced[0], want, search.Schema.Equal) {
		t.Fatalf("registrations=%+v", driver.synced)
	}
}

// TestEngineConstructionEmptyPoliciesAcceptNoIndexes preserves the empty
// registry and fresh-draft behavior without special nil filtering.
func TestEngineConstructionEmptyPoliciesAcceptNoIndexes(t *testing.T) {
	t.Parallel()

	var policies []func(*search.Builder)

	if err := search.New(newRecorder(), policies...).Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := search.New(newRecorder(), []func(*search.Builder){}...).Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	var first *search.Builder
	search.New(nil, func(engine *search.Builder) {
		first = engine
	})
	search.New(nil, func(engine *search.Builder) {
		if engine == first {
			t.Fatal("construction reused a draft")
		}
	})
}

// TestEngineConstructionNilPolicyPanics ensures later callbacks do not run
// after a nil entry, including when required driver input is nil.
func TestEngineConstructionNilPolicyPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil policy did not interrupt in order: calls=%d", calls)
		}
	}()
	search.New(nil, func(engine *search.Builder) {
		calls++
	}, nil, func(engine *search.Builder) {
		calls++
	})
}

// TestEngineConstructionApplicationPanicPropagates preserves the application
// panic value rather than converting it into a registered-index error.
func TestEngineConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	failure := "construction policy"
	defer func() {
		if recover() != failure {
			t.Fatal("application panic was replaced")
		}
	}()
	search.New(nil, func(engine *search.Builder) {
		panic(failure)
	})
}

// TestEngineConstructionKeepsFirstRegistrationError verifies final validation
// follows every callback and records the first error in registration order.
func TestEngineConstructionKeepsFirstRegistrationError(t *testing.T) {
	t.Parallel()

	calls := 0
	engine := search.New(newRecorder(), func(engine *search.Builder) {
		calls++
		engine.Indexes(nil)
	}, func(engine *search.Builder) {
		calls++
		engine.Indexes(testIndex, testIndex)
	})

	err := engine.Sync(t.Context())

	if calls != 2 || !errors.Is(err, search.ErrInvalidName) {
		t.Fatalf("callbacks=%d error=%v", calls, err)
	}

	if !strings.Contains(err.Error(), "nil index") {
		t.Fatalf("first registration error replaced: %v", err)
	}
}

// recorder wraps a memory driver and records what the engine sends it.
type recorder struct {
	// Driver provides isolated memory behavior beneath the recording overrides.
	*memorysearch.Driver

	// synced records schema batches sent to the driver.
	synced [][]search.Schema
	// puts records document batches sent to the driver.
	puts [][]search.Document
	// calls counts operations observed by this fixture.
	calls int

	// matches, when set, is what Search returns.
	matches *search.Matches
}

// Sync records the schemas and delegates registration to the memory driver.
func (driver *recorder) Sync(ctx context.Context, schemas []search.Schema) error {
	driver.calls++
	driver.synced = append(driver.synced, schemas)

	return driver.Driver.Sync(ctx, schemas)
}

// Put records the documents and delegates indexing to the memory driver.
func (driver *recorder) Put(ctx context.Context, schema search.Schema, docs []search.Document) error {
	driver.calls++
	driver.puts = append(driver.puts, docs)

	return driver.Driver.Put(ctx, schema, docs)
}

// Search counts requests and returns configured matches or delegates to memory search.
func (driver *recorder) Search(ctx context.Context, schema search.Schema, spec search.Spec, limit, offset int) (search.Matches, error) {
	driver.calls++

	if driver.matches != nil {
		return *driver.matches, nil
	}

	return driver.Driver.Search(ctx, schema, spec, limit, offset)
}

// Delete counts requests and delegates deletion to the memory driver.
func (driver *recorder) Delete(ctx context.Context, schema search.Schema, ids []string) error {
	driver.calls++

	return driver.Driver.Delete(ctx, schema, ids)
}

// newRecorder creates a recording wrapper backed by an isolated memory index.
func newRecorder() *recorder {
	return &recorder{Driver: memorysearch.New()}
}

// syncedEngine registers testIndex and synchronizes it, failing t on an error.
func syncedEngine(t *testing.T, driver search.Driver) *search.Engine {
	t.Helper()

	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(testIndex)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	return engine
}

// TestEngineReportsInvalidIndexesFromEveryCall checks an invalid registered
// identity remains visible through every operation on that engine.
func TestEngineReportsInvalidIndexesFromEveryCall(t *testing.T) {
	t.Parallel()

	good := search.NewIndex("good", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	bad := search.NewIndex("Bad", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(good, bad)
	})

	wantInvalidEngine(t, "invalid index", engine, good)
}

// wantInvalidEngine checks all operations on one invalid engine share the
// registration error, using good as an otherwise valid call-site index.
func wantInvalidEngine(t *testing.T, name string, engine *search.Engine, good search.Index[doc]) {
	t.Helper()

	ctx := t.Context()
	_, searchErr := engine.Search(ctx, search.NewQuery(good, ""), pagination.PageParams{})

	for call, err := range map[string]error{
		"Sync":   engine.Sync(ctx),
		"Check":  engine.Check(ctx),
		"Put":    engine.Put(ctx, good, doc{ID: "a"}),
		"Delete": engine.Delete(ctx, good, "a"),
		"Drop":   engine.Drop(ctx, good),
		"Search": searchErr,
	} {
		if !errors.Is(err, search.ErrInvalidName) {
			t.Errorf("%s: %s error = %v, want ErrInvalidName", name, call, err)
		}
	}
}

// TestEngineRejectsDuplicateIndexNames checks incompatible registrations with
// the same identity invalidate every operation on the published engine.
func TestEngineRejectsDuplicateIndexNames(t *testing.T) {
	t.Parallel()

	good := search.NewIndex("good", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	duplicate := search.NewIndex("good", docID, func(index *search.IndexBuilder) {
		index.Searchable("body")
	})

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(good, duplicate)
	})

	wantInvalidEngine(t, "duplicate name", engine, good)
}

// TestEngineRejectsRepeatedIndex checks repeated policies cannot register the
// same definition twice, even when its schema is identical.
func TestEngineRejectsRepeatedIndex(t *testing.T) {
	t.Parallel()

	good := search.NewIndex("good", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(good)
	}, func(engine *search.Builder) {
		engine.Indexes(good)
	})

	wantInvalidEngine(t, "same index twice", engine, good)
}

// TestEngineRejectsNilIndex checks absent registrations invalidate every
// operation rather than permitting a partially configured engine.
func TestEngineRejectsNilIndex(t *testing.T) {
	t.Parallel()

	good := search.NewIndex("good", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(good, nil)
	})

	wantInvalidEngine(t, "nil index", engine, good)
}

// TestEngineRejectsAnInvalidIndexOnPutAndSearch checks call-site definitions are validated.
func TestEngineRejectsAnInvalidIndexOnPutAndSearch(t *testing.T) {
	t.Parallel()

	bad := search.NewIndex[doc]("good", nil, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	driver := newRecorder()
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(search.NewIndex("good", docID, func(index *search.IndexBuilder) {
			index.Searchable("title")
		}))
	})

	if err := engine.Put(t.Context(), bad, doc{ID: "a"}); !errors.Is(err, search.ErrInvalidName) {
		t.Fatalf("Put error = %v, want ErrInvalidName", err)
	}

	if _, err := engine.Search(t.Context(), search.NewQuery(bad, ""), pagination.PageParams{}); !errors.Is(err, search.ErrInvalidQuery) {
		t.Fatalf("Search error = %v, want ErrInvalidQuery", err)
	}

	if driver.calls != 0 {
		t.Fatalf("driver received %d calls", driver.calls)
	}
}

// TestSyncSendsEveryRegisteredSchemaByName checks registration order cannot change synchronization order.
func TestSyncSendsEveryRegisteredSchemaByName(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	zeta := search.NewIndex("zeta", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	alpha := search.NewIndex("alpha", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(zeta, alpha)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(driver.synced) != 1 || len(driver.synced[0]) != 2 || driver.synced[0][0].Name != "alpha" || driver.synced[0][1].Name != "zeta" {
		t.Fatalf("driver received %+v, want alpha then zeta", driver.synced)
	}
}

// TestSyncDoesNotPruneUnregisteredIndexes guards against destructive synchronization of shared backends.
func TestSyncDoesNotPruneUnregisteredIndexes(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()
	old := search.NewIndex("old", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	if err := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(old)
	}).Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	current := syncedEngine(t, driver)

	if err := current.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(old)
	}).Check(t.Context()); err != nil {
		t.Fatalf("old index was pruned: %v", err)
	}
}

// TestPutEncodesDocumentsAndKeepsTheLastDuplicate checks duplicate identities use the last input document.
func TestPutEncodesDocumentsAndKeepsTheLastDuplicate(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	engine := syncedEngine(t, driver)

	err := engine.Put(t.Context(), testIndex,
		doc{ID: "a", Title: "first"}, doc{ID: "b", Title: "other"}, doc{ID: "a", Title: "second"})

	if err != nil {
		t.Fatal(err)
	}

	if len(driver.puts) != 1 || len(driver.puts[0]) != 2 {
		t.Fatalf("driver received %+v, want one call with two documents", driver.puts)
	}

	first := driver.puts[0][0]

	if first.ID != "a" || !strings.Contains(string(first.Source), `"title":"second"`) || driver.puts[0][1].ID != "b" {
		t.Fatalf("documents = %q %s, %q", first.ID, first.Source, driver.puts[0][1].ID)
	}
}

// TestPutValidatesEveryIDBeforeWriting checks invalid identities prevent any batch write.
func TestPutValidatesEveryIDBeforeWriting(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	engine := syncedEngine(t, driver)
	long := strings.Repeat("a", 256)

	for _, id := range []string{"", "has space", "slash/id", "café", long} {
		err := engine.Put(t.Context(), testIndex, doc{ID: "good"}, doc{ID: id})

		if !errors.Is(err, search.ErrInvalidID) {
			t.Errorf("Put with ID %q: error = %v, want ErrInvalidID", id, err)
		}
	}

	if len(driver.puts) != 0 {
		t.Fatalf("driver received %d Put calls, want 0", len(driver.puts))
	}

	for _, id := range []string{"A-b_9", strings.Repeat("a", 255)} {
		if err := engine.Put(t.Context(), testIndex, doc{ID: id}); err != nil {
			t.Errorf("Put with ID %.12q: %v", id, err)
		}
	}
}

// unencodable fails to encode as JSON.
type unencodable struct {
	// ID is the stable document identity used for indexing and result checks.
	ID string `json:"id"`
	// Bad deliberately cannot be encoded as JSON, exercising pre-write validation.
	Bad chan int `json:"bad"`
}

// TestPutEncodesBeforeWriting checks encoding errors cannot partially publish a batch.
func TestPutEncodesBeforeWriting(t *testing.T) {
	t.Parallel()

	index := search.NewIndex("bad", func(document unencodable) string {
		return document.ID
	}, func(index *search.IndexBuilder) {
		index.Searchable("id")
	})

	driver := newRecorder()
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := engine.Put(t.Context(), index, unencodable{ID: "a"}); err == nil {
		t.Fatal("Put of an unencodable document succeeded")
	}

	if len(driver.puts) != 0 {
		t.Fatalf("driver received %d Put calls, want 0", len(driver.puts))
	}
}

// TestPutRejectsDocumentsThatAreNotObjects checks JSON scalar documents fail before driver access.
func TestPutRejectsDocumentsThatAreNotObjects(t *testing.T) {
	t.Parallel()

	index := search.NewIndex("strings", func(value string) string {
		return value
	}, func(index *search.IndexBuilder) {
		index.Searchable("id")
	})

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := engine.Put(t.Context(), index, "plain"); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("Put error = %v, want a JSON object error", err)
	}
}

// TestEmptyPutAndDeleteDoNotReachTheDriver checks empty batches are side-effect-free.
func TestEmptyPutAndDeleteDoNotReachTheDriver(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	engine := syncedEngine(t, driver)
	before := driver.calls

	if err := engine.Put(t.Context(), testIndex); err != nil {
		t.Fatal(err)
	}

	if err := engine.Delete(t.Context(), testIndex); err != nil {
		t.Fatal(err)
	}

	if driver.calls != before {
		t.Fatalf("driver received %d calls", driver.calls-before)
	}
}

// TestDeleteValidatesIDsBeforeDeleting checks invalid identities prevent any batch deletion.
func TestDeleteValidatesIDsBeforeDeleting(t *testing.T) {
	t.Parallel()

	engine := syncedEngine(t, memorysearch.New())

	if err := engine.Put(t.Context(), testIndex, doc{ID: "a", Title: "x"}); err != nil {
		t.Fatal(err)
	}

	if err := engine.Delete(t.Context(), testIndex, "a", "bad id"); !errors.Is(err, search.ErrInvalidID) {
		t.Fatalf("Delete error = %v, want ErrInvalidID", err)
	}

	page, err := engine.Search(t.Context(), search.NewQuery(testIndex, ""), pagination.PageParams{})

	if err != nil || page.Total != 1 {
		t.Fatalf("Search = %+v, %v; want the document kept", page, err)
	}
}

// TestSearchDecodesHitsAndKeepsScores checks typed decoding preserves ranking metadata.
func TestSearchDecodesHitsAndKeepsScores(t *testing.T) {
	t.Parallel()

	engine := syncedEngine(t, memorysearch.New())

	if err := engine.Put(t.Context(), testIndex, doc{ID: "a", Title: "red shoes", Price: 9.5}); err != nil {
		t.Fatal(err)
	}

	page, err := engine.Search(t.Context(), search.NewQuery(testIndex, "red"), pagination.PageParams{
		Page: 1, PerPage: 20,
	})

	if err != nil {
		t.Fatal(err)
	}

	if len(page.Items) != 1 || page.Items[0].ID != "a" || page.Items[0].Document.Price != 9.5 || page.Items[0].Score <= 0 {
		t.Fatalf("page = %+v", page)
	}

	if page.Total != 1 || page.PerPage != 20 || page.CurrentPage != 1 || page.LastPage != 1 {
		t.Fatalf("page metadata = %+v", page)
	}
}

// TestSearchCapsTheTotalAtMaxHits checks pagination totals respect the portable result window.
func TestSearchCapsTheTotalAtMaxHits(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	driver.matches = &search.Matches{Total: 5000}
	engine := syncedEngine(t, driver)

	page, err := engine.Search(t.Context(), search.NewQuery(testIndex, ""), pagination.PageParams{
		Page: 1, PerPage: 10,
	})

	if err != nil {
		t.Fatal(err)
	}

	if page.Total != search.MaxHits || page.LastPage != 100 {
		t.Fatalf("total = %d last page = %d, want %d and 100", page.Total, page.LastPage, search.MaxHits)
	}
}

// TestSearchRefusesPagesBeyondMaxHitsBeforeTheDriver checks out-of-window pages avoid backend requests.
func TestSearchRefusesPagesBeyondMaxHitsBeforeTheDriver(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	engine := syncedEngine(t, driver)
	before := driver.calls

	for _, params := range []pagination.PageParams{{Page: 11, PerPage: 100}, {Page: 101, PerPage: 10}} {
		if _, err := engine.Search(t.Context(), search.NewQuery(testIndex, ""), params); !errors.Is(err, search.ErrInvalidQuery) {
			t.Errorf("Search(%+v) error = %v, want ErrInvalidQuery", params, err)
		}
	}

	if _, err := engine.Search(t.Context(), search.NewQuery(testIndex, ""), pagination.PageParams{
		Page: 10, PerPage: 100,
	}); err != nil {
		t.Fatalf("Search at the cap: %v", err)
	}

	if driver.calls != before+1 {
		t.Fatalf("driver received %d calls, want 1", driver.calls-before)
	}
}

// TestSearchPassesLimitAndOffset checks page parameters translate to driver bounds.
func TestSearchPassesLimitAndOffset(t *testing.T) {
	t.Parallel()

	engine := syncedEngine(t, memorysearch.New())
	docs := make([]doc, 7)

	for i := range docs {
		docs[i] = doc{ID: "d" + string(rune('a'+i)), Title: "x"}
	}

	if err := engine.Put(t.Context(), testIndex, docs...); err != nil {
		t.Fatal(err)
	}

	page, err := engine.Search(t.Context(), search.NewQuery(testIndex, ""), pagination.PageParams{
		Page: 2, PerPage: 3,
	})

	if err != nil {
		t.Fatal(err)
	}

	var ids []string

	for _, hit := range page.Items {
		ids = append(ids, hit.ID)
	}

	if !slices.Equal(ids, []string{"dd", "de", "df"}) || page.Total != 7 || page.LastPage != 3 {
		t.Fatalf("page 2 = %v total %d last %d", ids, page.Total, page.LastPage)
	}
}

// TestSearchReportsDocumentsThatDoNotDecode checks malformed driver results surface decoding errors.
// Even a successfully decoded earlier hit must not escape as a partial page.
func TestSearchReportsDocumentsThatDoNotDecode(t *testing.T) {
	t.Parallel()

	driver := newRecorder()
	driver.matches = &search.Matches{
		Total: 2,
		Items: []search.Match{
			{Document: search.Document{ID: "valid", Source: []byte(`{"price":10}`)}},
			{Document: search.Document{
				ID: "invalid", Source: []byte(`{"price":"cheap"}`),
			}},
		},
	}
	engine := syncedEngine(t, driver)

	page, err := engine.Search(t.Context(), search.NewQuery(testIndex, ""), pagination.PageParams{})

	if err == nil {
		t.Fatal("Search of an undecodable document succeeded")
	}

	if page.Items != nil || page.Total != 0 {
		t.Fatalf("decode failure returned a partial page: %+v", page)
	}
}

// TestDriverErrorsKeepTheirSentinel checks facade error wrapping preserves errors.Is matching.
func TestDriverErrorsKeepTheirSentinel(t *testing.T) {
	t.Parallel()

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(testIndex)
	})

	if err := engine.Put(t.Context(), testIndex, doc{ID: "a"}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Put before Sync error = %v, want ErrNotFound", err)
	}

	if err := engine.Check(t.Context()); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Check before Sync error = %v, want ErrNotFound", err)
	}
}

// TestDriverAccessors checks direct and typed access recover the original driver.
func TestDriverAccessors(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()
	engine := search.New(driver)

	if engine.Driver() != search.Driver(driver) {
		t.Fatal("Driver() is not the configured driver")
	}

	if got, ok := engine.DriverAs[*memorysearch.Driver](); !ok || got != driver {
		t.Fatalf("DriverAs = %v, %v", got, ok)
	}

	if _, ok := engine.DriverAs[*recorder](); ok {
		t.Fatal("DriverAs found a driver that is not there")
	}
}
