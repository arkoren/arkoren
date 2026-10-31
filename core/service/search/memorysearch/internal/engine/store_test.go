package engine_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
)

// boundaryContext deterministically cancels after an error-status snapshot.
// It models cancellation immediately after a successful entry check, without
// sleeps, lock access, polling counts, or assertions about driver internals.
type boundaryContext struct {
	// Context supplies the underlying cancellation state and standard values.
	context.Context

	// cancel requests cancellation after observing the current status.
	cancel context.CancelFunc
}

// Err observes the current status before requesting cancellation. A later
// observation sees the canceled context, as it would across a cancellation race.
func (pending boundaryContext) Err() error {
	err := pending.Context.Err()
	pending.cancel()

	return err
}

// canceledAtBoundary returns a context that cancels just after an initial poll.
// Cleanup owns the release even when a test stops before making a driver call.
func canceledAtBoundary(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	return boundaryContext{Context: ctx, cancel: cancel}
}

// TestDropObservesCancellationAfterEntry verifies cancellation at the lock
// boundary cannot remove an index that was present before the call.
func TestDropObservesCancellationAfterEntry(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Drop(canceledAtBoundary(t), schema); !errors.Is(err, context.Canceled) {
		t.Fatalf("Drop = %v, want context.Canceled", err)
	}

	if err := driver.Check(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatalf("canceled Drop removed index: %v", err)
	}
}

// TestSyncObservesCancellationAfterEntry verifies cancellation before applying
// a schema leaves the backend without that new index.
func TestSyncObservesCancellationAfterEntry(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(canceledAtBoundary(t), []search.Schema{schema}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync = %v, want context.Canceled", err)
	}

	if err := driver.Check(t.Context(), []search.Schema{schema}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("canceled Sync created index: %v", err)
	}
}

// TestCheckObservesCancellationAfterEntry checks a read that becomes canceled
// does not report a successful schema comparison from its entry snapshot.
func TestCheckObservesCancellationAfterEntry(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Check(canceledAtBoundary(t), []search.Schema{schema}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Check = %v, want context.Canceled", err)
	}
}

// TestPutObservesCancellationAfterEntry checks cancellation during preparation
// never publishes the caller's batch to the in-memory index.
func TestPutObservesCancellationAfterEntry(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	err := driver.Put(canceledAtBoundary(t), schema, []search.Document{
		{ID: "a", Source: []byte(`{"title":"alpha"}`)},
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v, want context.Canceled", err)
	}

	matches, err := driver.Search(t.Context(), schema, search.Spec{}, 10, 0)

	if err != nil || matches.Total != 0 {
		t.Fatalf("canceled Put stored documents: %+v, %v", matches, err)
	}
}

// TestDeleteObservesCancellationAfterEntry checks a canceled delete preserves
// documents when cancellation happens before any mutation is applied.
func TestDeleteObservesCancellationAfterEntry(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Put(t.Context(), schema, []search.Document{
		{ID: "a", Source: []byte(`{"title":"alpha"}`)},
	}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Delete(canceledAtBoundary(t), schema, []string{"a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete = %v, want context.Canceled", err)
	}

	matches, err := driver.Search(t.Context(), schema, search.Spec{}, 10, 0)

	if err != nil || matches.Total != 1 {
		t.Fatalf("canceled Delete removed documents: %+v, %v", matches, err)
	}
}

// schema declares the searchable and filterable fields shared by memory index tests.
var schema = search.Schema{Name: "docs", Searchable: []string{"title"}, Filterable: []string{"n"}}

// TestSyncCopiesSchemaFields checks the stored definition survives caller
// edits to every field list after synchronization has returned.
func TestSyncCopiesSchemaFields(t *testing.T) {
	t.Parallel()

	definition := search.Schema{
		Name:       "docs",
		Searchable: []string{"title"},
		Filterable: []string{"n"},
		Sortable:   []string{"n"},
	}
	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{definition}); err != nil {
		t.Fatal(err)
	}

	definition.Searchable[0] = "changed"
	definition.Filterable[0] = "changed"
	definition.Sortable[0] = "changed"
	want := search.Schema{
		Name:       "docs",
		Searchable: []string{"title"},
		Filterable: []string{"n"},
		Sortable:   []string{"n"},
	}

	if err := driver.Check(t.Context(), []search.Schema{want}); err != nil {
		t.Fatalf("caller edits changed stored schema: %v", err)
	}
}

// TestPutAndSearchCopyDocuments verifies neither input nor returned bytes alias stored documents.
func TestPutAndSearchCopyDocuments(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	source := []byte(`{"title":"alpha"}`)

	if err := driver.Put(t.Context(), schema, []search.Document{{ID: "a", Source: source}}); err != nil {
		t.Fatal(err)
	}

	source[10] = 'X'

	matches, err := driver.Search(t.Context(), schema, search.Spec{}, 10, 0)

	if err != nil || len(matches.Items) != 1 {
		t.Fatalf("Search = %+v, %v", matches, err)
	}

	if got := string(matches.Items[0].Source); got != `{"title":"alpha"}` {
		t.Fatalf("stored source = %s, want the original bytes", got)
	}

	matches.Items[0].Source[10] = 'Y'
	again, _ := driver.Search(t.Context(), schema, search.Spec{}, 10, 0)

	if got := string(again.Items[0].Source); got != `{"title":"alpha"}` {
		t.Fatalf("stored source = %s, want a result copy", got)
	}
}

// TestMalformedDocumentStoresNothing checks decoding failures cannot partially publish a batch.
func TestMalformedDocumentStoresNothing(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	err := driver.Put(t.Context(), schema, []search.Document{
		{ID: "a", Source: []byte(`{"title":"ok"}`)},
		{ID: "b", Source: []byte(`{`)},
	})

	if err == nil {
		t.Fatal("Put of malformed JSON succeeded")
	}

	matches, _ := driver.Search(t.Context(), schema, search.Spec{}, 10, 0)

	if matches.Total != 0 {
		t.Fatalf("total = %d after a failed Put, want 0", matches.Total)
	}
}

// TestCheckDistinguishesMissingFromChanged checks absent and incompatible schemas have distinct errors.
func TestCheckDistinguishesMissingFromChanged(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Check(t.Context(), []search.Schema{schema}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Check before Sync error = %v, want ErrNotFound", err)
	}

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Check(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatalf("Check after Sync = %v", err)
	}

	changed := schema
	changed.Filterable = []string{"n", "m"}

	if err := driver.Check(t.Context(), []search.Schema{changed}); !errors.Is(err, search.ErrSchemaChanged) {
		t.Fatalf("Check of a changed schema error = %v, want ErrSchemaChanged", err)
	}
}

// TestCheckIgnoresIndexesItIsNotAskedAbout checks readiness only validates requested schemas.
func TestCheckIgnoresIndexesItIsNotAskedAbout(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()
	other := search.Schema{Name: "other", Searchable: []string{"title"}}

	if err := driver.Sync(t.Context(), []search.Schema{schema, other}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Check(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatalf("Check = %v", err)
	}
}

// membershipDriver stores matching and nonmatching values of each scalar
// kind, plus fields with no comparable value. Tests use only search.Driver
// operations so membership checks exercise the public query protocol.
func membershipDriver(t *testing.T) search.Driver {
	t.Helper()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Put(t.Context(), schema, []search.Document{
		{ID: "number-match", Source: []byte(`{"title":"x","n":1}`)},
		{ID: "number-other", Source: []byte(`{"title":"x","n":2}`)},
		{ID: "string-match", Source: []byte(`{"title":"x","n":"ONE"}`)},
		{ID: "string-other", Source: []byte(`{"title":"x","n":"other"}`)},
		{ID: "boolean-match", Source: []byte(`{"title":"x","n":true}`)},
		{ID: "boolean-other", Source: []byte(`{"title":"x","n":false}`)},
		{ID: "missing", Source: []byte(`{"title":"x"}`)},
		{ID: "null", Source: []byte(`{"title":"x","n":null}`)},
		{ID: "array", Source: []byte(`{"title":"x","n":[1]}`)},
		{ID: "object", Source: []byte(`{"title":"x","n":{"value":1}}`)},
	}); err != nil {
		t.Fatal(err)
	}

	return driver
}

// membershipIDs executes conditions against the complete membership fixture
// and checks that its total agrees with the returned window. IDs remain in
// driver order so assertions also detect unstable result ordering.
func membershipIDs(t *testing.T, driver search.Driver, conditions []search.Condition) []string {
	t.Helper()

	matches, err := driver.Search(t.Context(), schema, search.Spec{Where: conditions}, 10, 0)

	if err != nil {
		t.Fatal(err)
	}

	if matches.Total != int64(len(matches.Items)) {
		t.Fatalf("total = %d, want %d returned items", matches.Total, len(matches.Items))
	}

	ids := make([]string, len(matches.Items))

	for i, item := range matches.Items {
		ids[i] = item.ID
	}

	return ids
}
