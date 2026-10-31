package memorystorage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"

	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/memorystorage"
	"arkoren.dev/core/service/storage/storagetest"
)

// TestBuilderZeroListLimitUsesDefault preserves the driver page size after an
// explicit zero setting; an omitted request limit must not mean unlimited.
func TestBuilderZeroListLimitUsesDefault(t *testing.T) {
	t.Parallel()

	driver := memorystorage.New(func(store *memorystorage.Builder) {
		store.ListLimit(0)
	})

	for index := range 101 {
		if _, err := driver.Put(t.Context(), strconv.Itoa(index), bytes.NewReader(nil)); err != nil {
			t.Fatal(err)
		}
	}

	page, err := driver.List(t.Context(), storage.ListOptions{})

	if err != nil {
		t.Fatal(err)
	}

	if len(page.Objects) != 100 || page.NextCursor == "" {
		t.Fatalf("page size = %d, cursor = %q", len(page.Objects), page.NextCursor)
	}
}

// TestBuilderNegativeListLimitUsesDefault preserves nonpositive normalization
// after the final policy rather than changing request pagination semantics.
func TestBuilderNegativeListLimitUsesDefault(t *testing.T) {
	t.Parallel()

	driver := memorystorage.New(func(store *memorystorage.Builder) {
		store.ListLimit(-1)
	})

	for index := range 101 {
		if _, err := driver.Put(t.Context(), strconv.Itoa(index), bytes.NewReader(nil)); err != nil {
			t.Fatal(err)
		}
	}

	page, err := driver.List(t.Context(), storage.ListOptions{})

	if err != nil {
		t.Fatal(err)
	}

	if len(page.Objects) != 100 || page.NextCursor == "" {
		t.Fatalf("page size = %d, cursor = %q", len(page.Objects), page.NextCursor)
	}
}

// TestDriverBinaryRoundTripIsolated verifies that object bytes do not alias caller buffers.
func TestDriverBinaryRoundTripIsolated(t *testing.T) {
	driver := memorystorage.New()
	source := []byte{0x00, 0xff, 'a'}
	putMemoryStorageObject(t, driver, "nested/value", source)

	source[0] = 'x'

	object := openMemoryStorageObject(t, driver, "nested/value")
	t.Cleanup(func() {
		_ = object.Contents.Close()
	})

	data := readMemoryStorageObject(t, object.Contents)
	data[0] = 'x'

	second := openMemoryStorageObject(t, driver, "nested/value")
	t.Cleanup(func() {
		_ = second.Contents.Close()
	})

	assertMemoryStorageObjectContents(t, second.Contents, []byte{0x00, 0xff, 'a'})
}

// TestDriverContentsIsSeekable verifies Contents implements io.ReadSeeker so
// downloads can serve ranges, and that Seek works on the snapshot.
func TestDriverContentsIsSeekable(t *testing.T) {
	driver := memorystorage.New()
	putMemoryStorageObject(t, driver, "a", []byte("0123456789"))

	object := openMemoryStorageObject(t, driver, "a")
	t.Cleanup(func() {
		_ = object.Contents.Close()
	})

	seeker, ok := object.Contents.(io.ReadSeeker)

	if !ok {
		t.Fatalf("Contents is %T, want an io.ReadSeeker", object.Contents)
	}

	if _, err := seeker.Seek(7, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	assertMemoryStorageObjectContents(t, seeker, []byte("789"))
}

// TestDriverMissingAndDelete verifies missing-object errors and idempotent deletion.
func TestDriverMissingAndDelete(t *testing.T) {
	driver := memorystorage.New()

	if _, err := driver.Open(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open error = %v", err)
	}

	if _, err := driver.Stat(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat error = %v", err)
	}

	if err := driver.Delete(context.Background(), "missing"); err != nil {
		t.Fatal(err)
	}
}

// TestStoragePreservesMissingObjectErrors verifies the facade preserves memory
// driver missing-object errors.
func TestStoragePreservesMissingObjectErrors(t *testing.T) {
	store := storage.New(memorystorage.New())

	if _, err := store.Open(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open error = %v", err)
	}

	if _, err := store.Stat(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat error = %v", err)
	}
}

// TestDriverListPagesAndDefaultLimit verifies deterministic pagination and default limits.
func TestDriverListPagesAndDefaultLimit(t *testing.T) {
	driver := memorystorage.New()
	putMemoryStorageListObjects(t, driver)

	first := memoryStorageListPage(t, driver, storage.ListOptions{Prefix: "reports/", Limit: 1}, "first")

	assertMemoryStoragePage(t, first, "reports/a", "reports/a", "first")

	second := memoryStorageListPage(t, driver, storage.ListOptions{
		Prefix: "reports/", Cursor: first.NextCursor,
	}, "second")

	assertMemoryStoragePage(t, second, "reports/b", "", "second")
}

// TestDriverRejectsInvalidDirectInput verifies validation of invalid keys and cursors.
func TestDriverRejectsInvalidDirectInput(t *testing.T) {
	driver := memorystorage.New()

	if _, err := driver.Put(context.Background(), "../escape", bytes.NewReader(nil)); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("Put error = %v", err)
	}

	if _, err := driver.List(context.Background(), storage.ListOptions{Prefix: "../"}); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("List error = %v", err)
	}

	if _, err := driver.List(context.Background(), storage.ListOptions{
		Prefix: "reports/", Cursor: "other/key",
	}); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("List error = %v", err)
	}
}

// TestDriverReplacesObject verifies that a later write replaces the stored object.
func TestDriverReplacesObject(t *testing.T) {
	driver := memorystorage.New()
	putMemoryStorageObject(t, driver, "value", []byte("first"))
	putMemoryStorageObject(t, driver, "value", []byte("second"))

	object := openMemoryStorageObject(t, driver, "value")
	t.Cleanup(func() {
		_ = object.Contents.Close()
	})

	assertMemoryStorageObjectText(t, object.Contents, "second")
}

// TestDriverConcurrentAccess verifies that concurrent storage operations are safe.
func TestDriverConcurrentAccess(t *testing.T) {
	driver := memorystorage.New()
	var group sync.WaitGroup

	for range 32 {
		group.Go(func() {
			key := "objects/value"
			_, _ = driver.Put(context.Background(), key, bytes.NewReader([]byte("data")))
			_, _ = driver.Stat(context.Background(), key)
			_, _ = driver.List(context.Background(), storage.ListOptions{Prefix: "objects/"})
		})
	}

	group.Wait()
}

// putMemoryStorageObject stores contents under key and fails the test if the write fails.
func putMemoryStorageObject(t *testing.T, driver *memorystorage.Driver, key string, contents []byte) {
	t.Helper()

	if _, err := driver.Put(context.Background(), key, bytes.NewReader(contents)); err != nil {
		t.Fatal(err)
	}
}

// openMemoryStorageObject opens key and fails the test if it cannot be read.
func openMemoryStorageObject(t *testing.T, driver *memorystorage.Driver, key string) storage.Object {
	t.Helper()

	object, err := driver.Open(context.Background(), key)

	if err != nil {
		t.Fatal(err)
	}

	return object
}

// readMemoryStorageObject reads an object body and fails the test if the read fails.
func readMemoryStorageObject(t *testing.T, contents io.Reader) []byte {
	t.Helper()

	data, err := io.ReadAll(contents)

	if err != nil {
		t.Fatal(err)
	}

	return data
}

// assertMemoryStorageObjectContents verifies a body contains want.
func assertMemoryStorageObjectContents(t *testing.T, contents io.Reader, want []byte) {
	t.Helper()

	stored, err := io.ReadAll(contents)

	if err != nil || !bytes.Equal(stored, want) {
		t.Fatalf("stored = %x, err = %v", stored, err)
	}
}

// putMemoryStorageListObjects stores the objects used by the list-page test.
func putMemoryStorageListObjects(t *testing.T, driver *memorystorage.Driver) {
	t.Helper()

	for _, key := range []string{"reports/a", "reports/b", "other/c"} {
		putMemoryStorageObject(t, driver, key, []byte(key))
	}
}

// memoryStorageListPage returns one expected storage list page.
func memoryStorageListPage(t *testing.T, driver *memorystorage.Driver, options storage.ListOptions, name string) storage.Page {
	t.Helper()

	page, err := driver.List(context.Background(), options)

	if err != nil {
		t.Fatalf("%s = %#v, err = %v", name, page, err)
	}

	return page
}

// assertMemoryStoragePage verifies one page contains the expected object and cursor.
func assertMemoryStoragePage(t *testing.T, page storage.Page, key string, cursor string, name string) {
	t.Helper()

	if len(page.Objects) != 1 {
		t.Fatalf("%s = %#v, err = %v", name, page, nil)
	}

	if page.Objects[0].Key != key {
		t.Fatalf("%s = %#v, err = %v", name, page, nil)
	}

	if page.NextCursor != cursor {
		t.Fatalf("%s = %#v, err = %v", name, page, nil)
	}
}

// assertMemoryStorageObjectText verifies a body contains want.
func assertMemoryStorageObjectText(t *testing.T, contents io.Reader, want string) {
	t.Helper()

	data := readMemoryStorageObject(t, contents)

	if string(data) != want {
		t.Fatalf("data = %q", data)
	}
}

// TestBuilderListLimitSetsDetachedDefaultPageSize verifies request defaults and
// retained-root publication without changing explicit per-list options.
func TestBuilderListLimitSetsDetachedDefaultPageSize(t *testing.T) {
	t.Parallel()

	var retained *memorystorage.Builder
	driver := memorystorage.New(func(store *memorystorage.Builder) {
		retained = store
		store.ListLimit(1)
	})
	retained.ListLimit(2)

	for _, key := range []string{"a", "b"} {
		if _, err := driver.Put(context.Background(), key, bytes.NewReader(nil)); err != nil {
			t.Fatal(err)
		}
	}

	page, err := driver.List(context.Background(), storage.ListOptions{})

	if err != nil {
		t.Fatal(err)
	}

	if len(page.Objects) != 1 || page.NextCursor != "a" {
		t.Fatalf("page = %#v", page)
	}
}

// TestMemoryStoragePoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestMemoryStoragePoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *memorystorage.Builder
	calls := 0
	component := memorystorage.New(func(store *memorystorage.Builder) {
		calls++
		retained = store
	}, func(store *memorystorage.Builder) {
		if calls != 1 || retained != store {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	memorystorage.New(func(store *memorystorage.Builder) {
		if retained == store {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestMemoryStorageNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestMemoryStorageNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*memorystorage.Builder)
	empty := []func(*memorystorage.Builder){}

	if memorystorage.New(absent...) == nil || memorystorage.New(empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestMemoryStorageNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestMemoryStorageNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		memorystorage.New(nil, func(store *memorystorage.Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestMemoryStoragePolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestMemoryStoragePolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		memorystorage.New(func(store *memorystorage.Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestMemoryStorageListLimitReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMemoryStorageListLimitReturnsDraft(t *testing.T) {
	t.Parallel()

	store := &memorystorage.Builder{}

	if store.ListLimit(4) != store {
		t.Fatal("setting returned a different draft")
	}
}

// endingContext ends immediately after its first successful error observation.
// It models cancellation racing operation admission without sleeps or a private
// hook, and otherwise retains the wrapped context's normal lifetime behavior.
type endingContext struct {
	// Context supplies the cancellation signal, deadline, and values.
	context.Context

	// cancel ends the context after observing its current error.
	cancel context.CancelFunc
}

// Err returns the error observed before cancellation, like an observation racing
// a concurrent cancel. Later observations always see the underlying canceled state.
func (ctx endingContext) Err() error {
	err := ctx.Context.Err()
	ctx.cancel()

	return err
}

// TestDriverListCancellationAfterAdmission rejects an incomplete listing when
// cancellation arrives after the caller's initial context observation.
func TestDriverListCancellationAfterAdmission(t *testing.T) {
	t.Parallel()

	driver := memorystorage.New()

	if _, err := driver.Put(t.Context(), "reports/a", bytes.NewReader([]byte("a"))); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	page, err := driver.List(endingContext{Context: ctx, cancel: cancel}, storage.ListOptions{Prefix: "reports/"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("List = %#v, %v; want context.Canceled", page, err)
	}

	if len(page.Objects) != 0 || page.NextCursor != "" {
		t.Fatalf("canceled listing exposed a partial page: %#v", page)
	}
}

// cancelingReader ends the caller's context after delivering one chunk. A later
// read fails distinctly, making continued consumption after cancellation visible.
type cancelingReader struct {
	// cancel ends the upload context after the first chunk.
	cancel context.CancelFunc

	// delivered records whether the first chunk has been returned.
	delivered bool
}

// Read supplies one byte, then refuses further reads. It never blocks and does
// not own or close any external resource.
func (reader *cancelingReader) Read(destination []byte) (int, error) {
	if reader.delivered {
		return 0, errors.New("body read continued after cancellation")
	}

	reader.delivered = true
	destination[0] = 'x'
	reader.cancel()

	return 1, nil
}

// TestDriverPutStopsReadingAfterCancellation preserves the previous object
// instead of consuming another chunk or publishing the interrupted upload.
func TestDriverPutStopsReadingAfterCancellation(t *testing.T) {
	t.Parallel()

	driver := memorystorage.New()

	if _, err := driver.Put(t.Context(), "object", bytes.NewReader([]byte("old"))); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	_, err := driver.Put(ctx, "object", &cancelingReader{cancel: cancel})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v, want context.Canceled", err)
	}

	object, err := driver.Open(t.Context(), "object")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := object.Contents.Close(); err != nil {
			t.Error(err)
		}
	})

	data, err := io.ReadAll(object.Contents)

	if err != nil || string(data) != "old" {
		t.Fatalf("stored body = %q, %v; want old", data, err)
	}
}

// TestConformance runs the storage conformance suite against the memory driver.
func TestConformance(t *testing.T) {
	t.Parallel()

	storagetest.Run(t, func(*testing.T) storage.Driver {
		return memorystorage.New()
	}, storagetest.WithoutSignURL())
}
