package storage_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/memorystorage"
	"arkoren.dev/core/service/telemetry"
)

// walkDriver substitutes only listing; other operations are deliberately absent
// so these tests isolate facade pagination rather than a provider's cursor rules.
type walkDriver struct {
	storage.Driver

	// requests retains every page request in order, including repeated traversal.
	requests []storage.ListOptions

	// list supplies pages or failures and can cancel a request deterministically.
	list func(context.Context, storage.ListOptions) (storage.Page, error)
}

// List records options before invoking the scripted response. The supplied
// callback owns response storage; the facade must not mutate its page slice.
func (driver *walkDriver) List(ctx context.Context, options storage.ListOptions) (storage.Page, error) {
	driver.requests = append(driver.requests, options)

	return driver.list(ctx, options)
}

// TestWalkDefersListingAndRepeatsFreshIO checks construction and Take(0) do not
// list, and a repeated traversal observes newly supplied metadata.
func TestWalkDefersListingAndRepeatsFreshIO(t *testing.T) {
	t.Parallel()

	driver := &walkDriver{}
	driver.list = func(context.Context, storage.ListOptions) (storage.Page, error) {
		return storage.Page{Objects: []storage.ObjectInfo{{Key: fmt.Sprintf("report-%d", len(driver.requests))}}}, nil
	}

	walk := storage.New(driver).Walk(t.Context(), storage.ListOptions{})

	if _, err := walk.Take(0).Items(); err != nil {
		t.Fatal(err)
	}

	if len(driver.requests) != 0 {
		t.Fatal("construction or Take(0) requested a page")
	}

	first, err := walk.Items()

	if err != nil {
		t.Fatal(err)
	}

	second, err := walk.Items()

	if err != nil {
		t.Fatal(err)
	}

	if first[0].Key != "report-1" || second[0].Key != "report-2" {
		t.Fatalf("traversals = %v, %v", first, second)
	}
}

// TestWalkFollowsOpaqueCursorsAcrossEmptyAndShortPages checks continuation is
// based only on the cursor, with unchanged prefix and page limit on every call.
func TestWalkFollowsOpaqueCursorsAcrossEmptyAndShortPages(t *testing.T) {
	t.Parallel()

	pages := map[string]storage.Page{
		"initial+/=": {NextCursor: "empty+/="},
		"empty+/=":   {Objects: []storage.ObjectInfo{{Key: "reports/a"}}, NextCursor: "short+/="},
		"short+/=":   {Objects: []storage.ObjectInfo{{Key: "reports/b"}, {Key: "reports/c"}}},
	}
	driver := &walkDriver{list: func(_ context.Context, options storage.ListOptions) (storage.Page, error) {
		return pages[options.Cursor], nil
	}}

	options := storage.ListOptions{Prefix: "reports/", Cursor: "initial+/=", Limit: 2}
	walk := storage.New(driver).Walk(t.Context(), options)

	items, err := walk.Items()

	if err != nil {
		t.Fatal(err)
	}

	want := []storage.ObjectInfo{{Key: "reports/a"}, {Key: "reports/b"}, {Key: "reports/c"}}

	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items = %v, want %v", items, want)
	}

	wantRequests := []storage.ListOptions{
		{Prefix: "reports/", Cursor: "initial+/=", Limit: 2},
		{Prefix: "reports/", Cursor: "empty+/=", Limit: 2},
		{Prefix: "reports/", Cursor: "short+/=", Limit: 2},
	}

	if !reflect.DeepEqual(driver.requests, wantRequests) {
		t.Fatalf("requests = %v, want %v", driver.requests, wantRequests)
	}

	if _, err := walk.Items(); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(driver.requests[3:], wantRequests) {
		t.Fatalf("repeated traversal requests = %v", driver.requests[3:])
	}
}

// TestWalkBreakStopsBeforeNextPage checks even a page-boundary break prevents
// the advertised continuation from being fetched.
func TestWalkBreakStopsBeforeNextPage(t *testing.T) {
	t.Parallel()

	driver := &walkDriver{list: func(context.Context, storage.ListOptions) (storage.Page, error) {
		return storage.Page{Objects: []storage.ObjectInfo{{Key: "a"}}, NextCursor: "next"}, nil
	}}

	for _, err := range storage.New(driver).Walk(t.Context(), storage.ListOptions{}) {
		if err != nil {
			t.Fatal(err)
		}

		break
	}

	if len(driver.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(driver.requests))
	}
}

// TestWalkTakeStopsWithinBufferedPage verifies Take neither drains buffered
// objects nor requests the next page after its item limit has been reached.
func TestWalkTakeStopsWithinBufferedPage(t *testing.T) {
	t.Parallel()

	driver := &walkDriver{list: func(context.Context, storage.ListOptions) (storage.Page, error) {
		return storage.Page{Objects: []storage.ObjectInfo{{Key: "a"}, {Key: "b"}}, NextCursor: "next"}, nil
	}}

	items, err := storage.New(driver).Walk(t.Context(), storage.ListOptions{}).Take(1).Items()

	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 1 || len(driver.requests) != 1 {
		t.Fatalf("items = %v, requests = %d", items, len(driver.requests))
	}
}

// TestWalkTakeStopsAtPageBoundary verifies collection Take propagates false on
// its final item instead of pulling another page to discover that it is done.
func TestWalkTakeStopsAtPageBoundary(t *testing.T) {
	t.Parallel()

	driver := &walkDriver{list: func(context.Context, storage.ListOptions) (storage.Page, error) {
		return storage.Page{Objects: []storage.ObjectInfo{{Key: "a"}}, NextCursor: "next"}, nil
	}}

	items, err := storage.New(driver).Walk(t.Context(), storage.ListOptions{}).Take(1).Items()

	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 1 || len(driver.requests) != 1 {
		t.Fatalf("items = %v, requests = %d", items, len(driver.requests))
	}
}

// TestWalkCancellationBeforeFirstFetch checks the captured context takes
// precedence over prefix validation and prevents all driver calls on reuse.
func TestWalkCancellationBeforeFirstFetch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver := &walkDriver{}
	walk := storage.New(driver).Walk(ctx, storage.ListOptions{Prefix: "../invalid"})
	cancel()

	items, err := walk.Items()

	if items != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("items = %v, err = %v", items, err)
	}

	if _, err := walk.Items(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if len(driver.requests) != 0 {
		t.Fatal("canceled traversal called driver")
	}
}

// TestWalkCancellationBetweenBufferedObjects checks cancellation in the
// consumer discards buffered metadata and emits exactly one terminal error.
func TestWalkCancellationBetweenBufferedObjects(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver := &walkDriver{list: func(context.Context, storage.ListOptions) (storage.Page, error) {
		return storage.Page{Objects: []storage.ObjectInfo{{Key: "a"}, {Key: "b"}}, NextCursor: "next"}, nil
	}}

	values, failures := 0, 0

	for info, err := range storage.New(driver).Walk(ctx, storage.ListOptions{}) {
		if err == nil {
			values++
			cancel()
			continue
		}

		failures++

		if !errors.Is(err, context.Canceled) || info != (storage.ObjectInfo{}) {
			t.Fatalf("info = %v, err = %v", info, err)
		}
	}

	if values != 1 || failures != 1 || len(driver.requests) != 1 {
		t.Fatalf("values = %d, errors = %d, requests = %d", values, failures, len(driver.requests))
	}
}

// TestWalkCancellationBetweenPages checks the next facade request rejects a
// canceled context without making another driver request.
func TestWalkCancellationBetweenPages(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver := &walkDriver{list: func(context.Context, storage.ListOptions) (storage.Page, error) {
		return storage.Page{Objects: []storage.ObjectInfo{{Key: "a"}}, NextCursor: "next"}, nil
	}}

	walk := storage.New(driver).Walk(ctx, storage.ListOptions{}).TapEach(func(_ int, _ storage.ObjectInfo) error {
		cancel()

		return nil
	})

	items, err := walk.Items()

	if len(items) != 1 || !errors.Is(err, context.Canceled) || len(driver.requests) != 1 {
		t.Fatalf("items = %v, err = %v, requests = %d", items, err, len(driver.requests))
	}
}

// TestWalkCancellationDuringFetch checks a driver that returns a prepared page
// after cancellation cannot expose its buffered objects to the consumer.
func TestWalkCancellationDuringFetch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver := &walkDriver{list: func(context.Context, storage.ListOptions) (storage.Page, error) {
		cancel()

		return storage.Page{Objects: []storage.ObjectInfo{{Key: "a"}}, NextCursor: "next"}, nil
	}}

	items, err := storage.New(driver).Walk(ctx, storage.ListOptions{}).Items()

	if items != nil || !errors.Is(err, context.Canceled) || len(driver.requests) != 1 {
		t.Fatalf("items = %v, err = %v, requests = %d", items, err, len(driver.requests))
	}
}

// TestWalkLaterPageErrorReturnsPrefixWithoutRetry checks Items preserves both
// the successful prefix and the exact driver error, discarding a failed page.
func TestWalkLaterPageErrorReturnsPrefixWithoutRetry(t *testing.T) {
	t.Parallel()

	failure := errors.New("later page unavailable")
	driver := &walkDriver{list: func(_ context.Context, options storage.ListOptions) (storage.Page, error) {
		if options.Cursor == "next" {
			return storage.Page{Objects: []storage.ObjectInfo{{Key: "discard"}}, NextCursor: "retry"}, failure
		}

		return storage.Page{Objects: []storage.ObjectInfo{{Key: "a"}}, NextCursor: "next"}, nil
	}}

	walk := storage.New(driver).Walk(t.Context(), storage.ListOptions{})

	items, err := walk.Items()

	if !reflect.DeepEqual(items, []storage.ObjectInfo{{Key: "a"}}) || !errors.Is(err, failure) {
		t.Fatalf("items = %v, err = %v", items, err)
	}

	if len(driver.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(driver.requests))
	}

	failures := 0

	for _, err := range walk {
		if err != nil {
			failures++
		}
	}

	if failures != 1 || len(driver.requests) != 4 {
		t.Fatalf("errors = %d, requests = %d", failures, len(driver.requests))
	}
}

// TestWalkValidationIsDeferred checks invalid prefixes fail through List at
// traversal time without reaching the driver.
func TestWalkValidationIsDeferred(t *testing.T) {
	t.Parallel()

	driver := &walkDriver{}

	items, err := storage.New(driver).Walk(t.Context(), storage.ListOptions{Prefix: "../invalid"}).Items()

	if items != nil || !errors.Is(err, storage.ErrInvalidKey) || len(driver.requests) != 0 {
		t.Fatalf("items = %v, err = %v, requests = %d", items, err, len(driver.requests))
	}
}

// TestWalkNonpositiveLimitsPreserveDriverDefaults checks zero and negative
// limits pass through unchanged, rather than turning into total-item limits.
func TestWalkNonpositiveLimitsPreserveDriverDefaults(t *testing.T) {
	t.Parallel()

	driver := &walkDriver{list: func(context.Context, storage.ListOptions) (storage.Page, error) {
		return storage.Page{Objects: []storage.ObjectInfo{{Key: "a"}, {Key: "b"}}}, nil
	}}

	files := storage.New(driver)

	if _, err := files.Walk(t.Context(), storage.ListOptions{}).Items(); err != nil {
		t.Fatal(err)
	}

	items, err := files.Walk(t.Context(), storage.ListOptions{Limit: -1}).Items()

	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 2 || driver.requests[0].Limit != 0 || driver.requests[1].Limit != -1 {
		t.Fatalf("items = %v, requests = %v", items, driver.requests)
	}
}

// TestWalkRecordsOneListOperationPerPage checks Walk adds no traversal-level
// telemetry and defers all page spans and metrics until consumption.
func TestWalkRecordsOneListOperationPerPage(t *testing.T) {
	t.Parallel()

	driver := &walkDriver{list: func(_ context.Context, options storage.ListOptions) (storage.Page, error) {
		if options.Cursor == "" {
			return storage.Page{NextCursor: "next"}, nil
		}

		return storage.Page{}, nil
	}}

	files, fake := newTraced(t, driver)
	walk := files.Walk(t.Context(), storage.ListOptions{})

	if len(fake.Spans()) != 0 {
		t.Fatal("construction emitted telemetry")
	}

	if _, err := walk.Items(); err != nil {
		t.Fatal(err)
	}

	if len(fake.Spans()) != 2 {
		t.Fatalf("spans = %v", fake.Spans())
	}

	fake.AssertCounter(t, "arkoren.storage.operations", 2, telemetry.String("arkoren.storage.operation", "list"))
}

// TestWalkMemoryIntegration preserves the driver's lexical order across real
// pages, filtering by the literal prefix and starting after the supplied cursor.
func TestWalkMemoryIntegration(t *testing.T) {
	t.Parallel()

	files := storage.New(memorystorage.New())

	for _, key := range []string{"reports/c", "other/a", "reports/a", "reports/b"} {
		if _, err := files.PutBytes(t.Context(), key, nil); err != nil {
			t.Fatal(err)
		}
	}

	page, err := files.List(t.Context(), storage.ListOptions{Prefix: "reports/", Limit: 1})

	if err != nil {
		t.Fatal(err)
	}

	items, err := files.Walk(t.Context(), storage.ListOptions{
		Prefix: "reports/", Cursor: page.NextCursor, Limit: 1,
	}).Items()

	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 2 || items[0].Key != "reports/b" || items[1].Key != "reports/c" {
		t.Fatalf("items = %v", items)
	}
}

// ExampleStorage_Walk demonstrates a collection item limit independent of the
// driver's page size. It uses memory storage and requires no external service.
func ExampleStorage_Walk() {
	ctx := context.Background()
	files := storage.New(memorystorage.New())

	for _, key := range []string{"reports/c", "reports/a", "reports/b"} {
		if _, err := files.PutBytes(ctx, key, nil); err != nil {
			fmt.Println(err)

			return
		}
	}

	objects, err := files.Walk(ctx, storage.ListOptions{Prefix: "reports/", Limit: 1}).Take(2).Items()

	if err != nil {
		fmt.Println(err)

		return
	}

	for _, info := range objects {
		fmt.Println(info.Key)
	}
	// Output:
	// reports/a
	// reports/b
}
