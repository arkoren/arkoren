package search_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
)

// TestAdministrativeEventsSnapshotConstruction verifies ordered once-only
// policies and immutable bus selection, including repeated no-op synchronization.
func TestAdministrativeEventsSnapshotConstruction(t *testing.T) {
	t.Parallel()

	events := event.New()
	var observed []search.MutationEvent

	_, err := events.Listen(search.Synced, func(_ context.Context, payload search.MutationEvent) error {
		observed = append(observed, payload)

		return errors.New("listener cannot replace sync result")
	})

	if err != nil {
		t.Fatal(err)
	}

	var retained *search.Builder
	calls := 0
	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		calls++
		retained = engine
		engine.Indexes(testIndex).Events(nil)
	}, func(engine *search.Builder) {
		calls++
		engine.Events(events)
	})
	retained.Events(nil)

	for range 2 {
		if err := engine.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	if calls != 2 || len(observed) != 2 {
		t.Fatalf("callbacks = %d, events = %+v", calls, observed)
	}

	if observed[0].Operation != "sync" || observed[0].ErrorClass != "" {
		t.Fatalf("sync event = %+v", observed[0])
	}
}

// TestDropEventsObserveSuccessEvenWhenAlreadyAbsent checks observed success,
// not an invented claim that an existing index or document was removed.
func TestDropEventsObserveSuccessEvenWhenAlreadyAbsent(t *testing.T) {
	t.Parallel()

	events := event.New()
	var observed []search.MutationEvent

	_, err := events.Listen(search.Dropped, func(_ context.Context, payload search.MutationEvent) error {
		observed = append(observed, payload)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(testIndex).Events(events)
	})

	for range 2 {
		if err := engine.Drop(t.Context(), testIndex); err != nil {
			t.Fatal(err)
		}
	}

	if len(observed) != 2 {
		t.Fatalf("drop events = %+v", observed)
	}

	if observed[0].Operation != "drop" || observed[0].ErrorClass != "" {
		t.Fatalf("drop event = %+v", observed[0])
	}
}

// partialSyncDriver deliberately changes memory before returning a native
// error, modeling an administrative boundary without cross-index atomicity.
type partialSyncDriver struct {
	// Driver supplies unchanged document/query behavior and persisted test state.
	*memorysearch.Driver
	// cause is preserved after the synchronization effect.
	cause error
}

// Sync changes only the first registered schema before failing. This fixture
// requires a nonempty registration batch and models a partial multi-index sync.
func (driver partialSyncDriver) Sync(ctx context.Context, schemas []search.Schema) error {
	if err := driver.Driver.Sync(ctx, schemas[:1]); err != nil {
		return err
	}

	return driver.cause
}

// TestSyncFailureRedactsAndPreservesPartialEffect verifies native data is not
// published and a failed observation does not imply that no indexes changed.
func TestSyncFailureRedactsAndPreservesPartialEffect(t *testing.T) {
	t.Parallel()

	events := event.New()
	var observed []search.MutationEvent

	_, err := events.Listen(search.Failed, func(_ context.Context, payload search.MutationEvent) error {
		observed = append(observed, payload)
		panic("listener cannot replace native error")
	})

	if err != nil {
		t.Fatal(err)
	}

	cause := errors.New("private-query private-document private-id")
	driver := partialSyncDriver{Driver: memorysearch.New(), cause: cause}
	other := search.NewIndex("zdocs", docID, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(testIndex, other).Events(events)
	})

	if err := engine.Sync(t.Context()); !errors.Is(err, cause) {
		t.Fatalf("sync error = %v", err)
	}

	if err := driver.Check(t.Context(), []search.Schema{testIndex.Schema()}); err != nil {
		t.Fatalf("partial effect lost: %v", err)
	}

	if err := driver.Check(t.Context(), []search.Schema{other.Schema()}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("later index should remain absent: %v", err)
	}

	if len(observed) != 1 {
		t.Fatalf("failure events = %+v", observed)
	}

	if observed[0].Operation != "sync" || observed[0].ErrorClass != "error" {
		t.Fatalf("failure event = %+v", observed[0])
	}

	if strings.Contains(fmt.Sprint(observed), "private-") {
		t.Fatalf("sensitive event = %+v", observed)
	}
}

// TestDropFailurePreservesContextClass proves failed administrative calls emit
// once, preserve cancellation identity, and leave the driver behavior intact.
func TestDropFailurePreservesContextClass(t *testing.T) {
	t.Parallel()

	events := event.New()
	var observed []search.MutationEvent

	_, err := events.Listen(search.Failed, func(_ context.Context, payload search.MutationEvent) error {
		observed = append(observed, payload)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(testIndex).Events(events)
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := engine.Drop(ctx, testIndex); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if len(observed) != 1 {
		t.Fatalf("failure events = %+v", observed)
	}

	if observed[0].Operation != "drop" || observed[0].ErrorClass != "canceled" {
		t.Fatalf("failure event = %+v", observed[0])
	}
}

// TestSearchDocumentOperationsEmitNothing excludes queries and per-document
// hot paths from the administrative stream, including rejected input.
func TestSearchDocumentOperationsEmitNothing(t *testing.T) {
	t.Parallel()

	driver := memorysearch.New()

	if err := driver.Sync(t.Context(), []search.Schema{testIndex.Schema()}); err != nil {
		t.Fatal(err)
	}

	events := event.New()
	count := 0

	_, err := events.ListenRegex(regexp.MustCompile(`^search\.`), func(context.Context, string, any) error {
		count++

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(testIndex).Events(events)
	})

	if err := engine.Put(t.Context(), testIndex, doc{ID: "id", Title: "private-document"}); err != nil {
		t.Fatal(err)
	}

	if err := engine.Delete(t.Context(), testIndex, "id"); err != nil {
		t.Fatal(err)
	}

	if _, err := engine.Search(t.Context(), search.NewQuery(testIndex, "private-query"), pagination.PageParams{}); err != nil {
		t.Fatal(err)
	}

	if err := engine.Delete(t.Context(), testIndex, ""); !errors.Is(err, search.ErrInvalidID) {
		t.Fatal(err)
	}

	if count != 0 {
		t.Fatalf("hot-path events = %d", count)
	}
}

// TestAdministrativeNilDisablingAndInvalidRegistration verifies both explicit
// opt-out and registration failures never publish mutation observations.
func TestAdministrativeNilDisablingAndInvalidRegistration(t *testing.T) {
	t.Parallel()

	events := event.New()
	count := 0

	_, err := events.ListenRegex(regexp.MustCompile(`^search\.`), func(context.Context, string, any) error {
		count++

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(testIndex).Events(events).Events(nil)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	engine = search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(testIndex, testIndex).Events(events)
	})

	if err := engine.Sync(t.Context()); !errors.Is(err, search.ErrInvalidName) {
		t.Fatal(err)
	}

	if err := engine.Drop(t.Context(), testIndex); !errors.Is(err, search.ErrInvalidName) {
		t.Fatal(err)
	}

	if count != 0 {
		t.Fatalf("unexpected events = %d", count)
	}
}

// panickingAdministration preserves normal document behavior but panics at
// administrative driver boundaries, which are not returned operation outcomes.
type panickingAdministration struct {
	// Driver supplies the unchanged remaining driver contract for these tests.
	*memorysearch.Driver
}

// Sync panics before returning an administrative batch outcome.
func (panickingAdministration) Sync(context.Context, []search.Schema) error {
	panic("sync driver panic")
}

// Drop panics before returning an administrative index outcome.
func (panickingAdministration) Drop(context.Context, search.Schema) error {
	panic("drop driver panic")
}

// TestSyncDriverPanicEmitsNothing guards against publishing Synced or Failed
// when an administrative batch never returned an observed driver result.
func TestSyncDriverPanicEmitsNothing(t *testing.T) {
	t.Parallel()

	events := event.New()
	count := 0

	_, err := events.ListenRegex(regexp.MustCompile(`^search\.`), func(context.Context, string, any) error {
		count++

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	engine := search.New(panickingAdministration{Driver: memorysearch.New()}, func(engine *search.Builder) {
		engine.Indexes(testIndex).Events(events)
	})
	defer func() {
		if recovered := recover(); recovered != "sync driver panic" {
			t.Fatalf("panic = %v", recovered)
		}

		if count != 0 {
			t.Fatalf("events after sync panic = %d", count)
		}
	}()
	_ = engine.Sync(t.Context())
}

// TestDropDriverPanicEmitsNothing guards against publishing Dropped or Failed
// from an initial nil named return error while the provider panic unwinds.
func TestDropDriverPanicEmitsNothing(t *testing.T) {
	t.Parallel()

	events := event.New()
	count := 0

	_, err := events.ListenRegex(regexp.MustCompile(`^search\.`), func(context.Context, string, any) error {
		count++

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	engine := search.New(panickingAdministration{Driver: memorysearch.New()}, func(engine *search.Builder) {
		engine.Indexes(testIndex).Events(events)
	})
	defer func() {
		if recovered := recover(); recovered != "drop driver panic" {
			t.Fatalf("panic = %v", recovered)
		}

		if count != 0 {
			t.Fatalf("events after drop panic = %d", count)
		}
	}()
	_ = engine.Drop(t.Context(), testIndex)
}
