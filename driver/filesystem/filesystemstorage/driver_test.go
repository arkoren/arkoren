package filesystemstorage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/storagetest"
	"arkoren.dev/driver/filesystem/filesystemstorage"
)

// pausedUpload keeps a public Put operation inside its reader until the test
// releases it. No filename or driver state is needed to observe an active upload.
type pausedUpload struct {
	// started closes once the driver has requested the upload's first bytes.
	started chan struct{}

	// release lets that first read finish with an empty complete object.
	release <-chan struct{}
}

// Read signals that Put is consuming the reader and waits for test-controlled
// completion. Returning EOF ends the copy without another Read call.
func (upload pausedUpload) Read([]byte) (int, error) {
	close(upload.started)
	<-upload.release

	return 0, io.EOF
}

// TestListIncludesStoragePrefixedObject keeps the upload filename convention
// from reserving otherwise valid application keys in the logical object space.
func TestListIncludesStoragePrefixedObject(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)

	if _, err := driver.Put(t.Context(), ".storage-report", bytes.NewBufferString("complete")); err != nil {
		t.Fatal(err)
	}

	page, err := driver.List(t.Context(), storage.ListOptions{Prefix: ".storage-"})

	if err != nil {
		t.Fatal(err)
	}

	if len(page.Objects) != 1 {
		t.Fatalf("List = %+v, want the published .storage-report object", page)
	}

	if page.Objects[0].Key != ".storage-report" {
		t.Fatalf("listed key = %q, want .storage-report", page.Objects[0].Key)
	}
}

// TestConcurrentDeleteAbsentObjectSucceeds releases competing deleters together
// against one published object. Every caller must succeed whether it observes
// the object before or after another caller removes it.
func TestConcurrentDeleteAbsentObjectSucceeds(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)

	if _, err := driver.Put(t.Context(), "shared", bytes.NewBufferString("value")); err != nil {
		t.Fatal(err)
	}

	const callers = 256
	start := make(chan struct{})
	results := make(chan error, callers)
	var workers sync.WaitGroup

	for range callers {
		workers.Go(func() {
			<-start
			results <- driver.Delete(t.Context(), "shared")
		})
	}

	close(start)
	workers.Wait()
	close(results)

	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Delete: %v", err)
		}
	}
}

// TestRegularObjectDownloadRetainsRanges checks the public HTTP seam after
// changing native open flags. A regular object's seekable body must still serve
// the requested byte window, rather than silently falling back to a full body.
func TestRegularObjectDownloadRetainsRanges(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)
	files := storage.New(driver)

	if _, err := files.Put(t.Context(), "report.txt", bytes.NewBufferString("0123456789")); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/report", nil)
	request.Header.Set("Range", "bytes=2-4")
	response := httptest.NewRecorder()

	if err := files.Download(response, request, "report.txt", "report.txt"); err != nil {
		t.Fatal(err)
	}

	if response.Code != http.StatusPartialContent || response.Body.String() != "234" {
		t.Fatalf("download = %d, %q, want 206, 234", response.Code, response.Body.String())
	}
}

// failingReader returns one partial read before reporting a failure.
type failingReader struct {
	// read records whether the one successful partial read has been returned.
	read bool
}

// Read returns partial data once and then returns a read error.
func (reader *failingReader) Read(data []byte) (int, error) {
	if reader.read {
		return 0, errors.New("read failure")
	}

	reader.read = true
	copy(data, "partial")

	return len("partial"), nil
}

// TestDriverWritesNestedBinaryObject verifies nested binary object persistence and metadata.
func TestDriverWritesNestedBinaryObject(t *testing.T) {
	driver := newFilesystemDriver(t)

	info := putFilesystemObjectInfo(t, driver, "nested/value", []byte{0x00, 0xff, 'a'})

	assertFilesystemObjectInfo(t, info, "nested/value", 3)

	object := openFilesystemObject(t, driver, "nested/value")
	t.Cleanup(func() {
		// The body is no longer read; closing only releases the test file handle.
		_ = object.Contents.Close()
	})

	assertFilesystemObjectContents(t, object.Contents, []byte{0x00, 0xff, 'a'})
	assertFilesystemObjectSize(t, driver, "nested/value", 3)
}

// TestDriverReplaceDoesNotLeavePartialObject verifies failed replacements preserve the existing object.
func TestDriverReplaceDoesNotLeavePartialObject(t *testing.T) {
	driver := newFilesystemDriver(t)
	putFilesystemObject(t, driver, "value", []byte("old"))

	assertFilesystemPutFails(t, driver, "value")

	object := openFilesystemObject(t, driver, "value")
	t.Cleanup(func() {
		// The body is no longer read; closing only releases the test file handle.
		_ = object.Contents.Close()
	})

	assertFilesystemObjectText(t, object.Contents, "old")
}

// TestDriverListsPrefixPages verifies deterministic pagination within a prefix.
func TestDriverListsPrefixPages(t *testing.T) {
	driver := newFilesystemDriver(t)
	putFilesystemListObjects(t, driver)

	first := filesystemListPage(t, driver, storage.ListOptions{
		Prefix: "reports/",
		Limit:  1,
	}, "first")

	assertFilesystemPage(t, first, "reports/a", "reports/a", "first")

	second := filesystemListPage(t, driver, storage.ListOptions{
		Prefix: "reports/",
		Cursor: first.NextCursor,
	}, "second")

	assertFilesystemPage(t, second, "reports/b", "", "second")
}

// TestDriverRejectsEmptyRootAndTraversal verifies root and object-key validation.
func TestDriverRejectsEmptyRootAndTraversal(t *testing.T) {
	assertFilesystemEmptyRootRejected(t)

	root := t.TempDir()
	driver := newFilesystemDriver(t)

	assertFilesystemTraversalRejected(t, driver)
	assertFilesystemEscapeAbsent(t, root)
}

// TestDriverMapsMissingAndDeleteIsIdempotent verifies the missing-object sentinel and deletion behavior.
func TestDriverMapsMissingAndDeleteIsIdempotent(t *testing.T) {
	driver, err := filesystemstorage.New(t.TempDir())

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

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

// TestStoragePreservesMissingObjectErrors verifies the facade preserves
// filesystem missing-object errors.
func TestStoragePreservesMissingObjectErrors(t *testing.T) {
	driver := newFilesystemDriver(t)
	store := storage.New(driver)

	if _, err := store.Open(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open error = %v", err)
	}

	if _, err := store.Stat(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat error = %v", err)
	}
}

// newFilesystemDriver creates a driver rooted in the test temporary directory.
func newFilesystemDriver(t *testing.T) *filesystemstorage.Driver {
	t.Helper()

	driver, err := filesystemstorage.New(t.TempDir())

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		// Assertions are complete; Stop is used only to release the root handle.
		_ = driver.Stop(context.Background())
	})

	return driver
}

// putFilesystemObjectInfo stores contents and returns the resulting metadata.
func putFilesystemObjectInfo(t *testing.T, driver *filesystemstorage.Driver, key string, contents []byte) storage.ObjectInfo {
	t.Helper()

	info, err := driver.Put(context.Background(), key, bytes.NewReader(contents))

	if err != nil {
		t.Fatalf("info = %#v, err = %v", info, err)
	}

	return info
}

// assertFilesystemObjectInfo verifies object metadata matches key and size.
func assertFilesystemObjectInfo(t *testing.T, info storage.ObjectInfo, key string, size int64) {
	t.Helper()

	if info.Key != key || info.Size != size {
		t.Fatalf("info = %#v, err = %v", info, nil)
	}
}

// openFilesystemObject opens key and fails the test if it cannot be read.
func openFilesystemObject(t *testing.T, driver *filesystemstorage.Driver, key string) storage.Object {
	t.Helper()

	object, err := driver.Open(context.Background(), key)

	if err != nil {
		t.Fatal(err)
	}

	return object
}

// assertFilesystemObjectContents verifies a body contains want.
func assertFilesystemObjectContents(t *testing.T, contents io.Reader, want []byte) {
	t.Helper()

	data, err := io.ReadAll(contents)

	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("data = %x, err = %v", data, err)
	}
}

// assertFilesystemObjectSize verifies stored metadata has size.
func assertFilesystemObjectSize(t *testing.T, driver *filesystemstorage.Driver, key string, size int64) {
	t.Helper()

	stat, err := driver.Stat(context.Background(), key)

	if err != nil || stat.Size != size {
		t.Fatalf("stat = %#v, err = %v", stat, err)
	}
}

// putFilesystemObject stores contents and fails the test when writing fails.
func putFilesystemObject(t *testing.T, driver *filesystemstorage.Driver, key string, contents []byte) {
	t.Helper()

	if _, err := driver.Put(context.Background(), key, bytes.NewReader(contents)); err != nil {
		t.Fatal(err)
	}
}

// assertFilesystemPutFails verifies the failing reader does not write an object.
func assertFilesystemPutFails(t *testing.T, driver *filesystemstorage.Driver, key string) {
	t.Helper()

	if _, err := driver.Put(context.Background(), key, &failingReader{}); err == nil {
		t.Fatal("Put succeeded for a failing reader")
	}
}

// assertFilesystemObjectText verifies a body contains want.
func assertFilesystemObjectText(t *testing.T, contents io.Reader, want string) {
	t.Helper()

	data, _ := io.ReadAll(contents)

	if string(data) != want {
		t.Fatalf("data = %q", data)
	}
}

// putFilesystemListObjects stores the objects used by the list-page test.
func putFilesystemListObjects(t *testing.T, driver *filesystemstorage.Driver) {
	t.Helper()

	for _, key := range []string{"reports/a", "reports/b", "other/c"} {
		putFilesystemObject(t, driver, key, []byte(key))
	}
}

// filesystemListPage returns one expected filesystem list page.
func filesystemListPage(
	t *testing.T,
	driver *filesystemstorage.Driver,
	options storage.ListOptions,
	name string,
) storage.Page {
	t.Helper()

	page, err := driver.List(context.Background(), options)

	if err != nil {
		t.Fatalf("%s = %#v, err = %v", name, page, err)
	}

	return page
}

// assertFilesystemPage verifies a page contains the expected object and cursor.
func assertFilesystemPage(t *testing.T, page storage.Page, key string, cursor string, name string) {
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

// assertFilesystemEmptyRootRejected verifies an empty root is invalid.
func assertFilesystemEmptyRootRejected(t *testing.T) {
	t.Helper()

	if _, err := filesystemstorage.New(""); err == nil {
		t.Fatal("New accepted an empty root")
	}
}

// assertFilesystemTraversalRejected verifies traversal keys are rejected.
func assertFilesystemTraversalRejected(t *testing.T, driver *filesystemstorage.Driver) {
	t.Helper()

	if _, err := driver.Put(context.Background(), "../escape", bytes.NewReader([]byte("data"))); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("Put error = %v", err)
	}
}

// assertFilesystemEscapeAbsent verifies no object was written outside root.
func assertFilesystemEscapeAbsent(t *testing.T, root string) {
	t.Helper()

	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escaped object stat error = %v", err)
	}
}

// TestListSkipsTemporaryUploads ensures a public Put operation does not expose
// an object until its reader finishes. It observes no staging filename or state.
func TestListSkipsTemporaryUploads(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := driver.Put(context.WithoutCancel(t.Context()), "published", pausedUpload{started: started, release: release})

		done <- err
	}()
	t.Cleanup(func() {
		close(release)

		if err := <-done; err != nil {
			t.Errorf("complete upload: %v", err)
		}
	})

	<-started

	page, err := driver.List(t.Context(), storage.ListOptions{})

	if err != nil {
		t.Fatal(err)
	}

	if len(page.Objects) != 0 {
		t.Fatalf("objects = %+v, want none", page.Objects)
	}
}

// TestStartCreatesRoot verifies Start creates a missing root and New does not.
func TestStartCreatesRoot(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "nested", "root")
	driver, err := filesystemstorage.New(root)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root stat after New = %v, want not exist", err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("root stat after Start = %v, %v", info, err)
	}
}

// TestConformance runs the storage conformance suite against the filesystem driver.
func TestConformance(t *testing.T) {
	t.Parallel()

	storagetest.Run(t, func(t *testing.T) storage.Driver {
		return newFilesystemDriver(t)
	}, storagetest.WithoutSignURL())
}

// newDriverWithEscapeLink returns a started driver whose root holds "link", a
// symlink to a directory outside the root, and the outside directory.
func newDriverWithEscapeLink(t *testing.T) (*filesystemstorage.Driver, string) {
	t.Helper()

	root, outside := t.TempDir(), t.TempDir()

	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	driver, err := filesystemstorage.New(root)

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		// Assertions are complete; Stop is used only to release the root handle.
		_ = driver.Stop(context.Background())
	})

	return driver, outside
}

// TestSymlinkOutOfRootCannotBeRead verifies Open and Stat refuse to follow a symlink out of the root.
func TestSymlinkOutOfRootCannotBeRead(t *testing.T) {
	t.Parallel()

	driver, outside := newDriverWithEscapeLink(t)

	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	object, err := driver.Open(t.Context(), "link/secret")

	if err == nil {
		_ = object.Contents.Close()
		t.Fatal("Open followed a symlink out of the root")
	}

	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open error = %v, want an error other than not found", err)
	}

	if _, err := driver.Stat(t.Context(), "link/secret"); err == nil {
		t.Fatal("Stat followed a symlink out of the root")
	}
}

// TestSymlinkOutOfRootCannotBeWritten verifies Put refuses to write through a symlink out of the root.
func TestSymlinkOutOfRootCannotBeWritten(t *testing.T) {
	t.Parallel()

	driver, outside := newDriverWithEscapeLink(t)

	if _, err := driver.Put(t.Context(), "link/new", bytes.NewReader([]byte("data"))); err == nil {
		t.Fatal("Put wrote through a symlink out of the root")
	}

	entries, err := os.ReadDir(outside)

	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory = %v, %v; want empty", entries, err)
	}
}

// TestSymlinkOutOfRootCannotBeDeleted verifies Delete refuses to remove a file behind a symlink out of the root.
func TestSymlinkOutOfRootCannotBeDeleted(t *testing.T) {
	t.Parallel()

	driver, outside := newDriverWithEscapeLink(t)
	target := filepath.Join(outside, "keep")

	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := driver.Delete(t.Context(), "link/keep"); err == nil {
		t.Fatal("Delete followed a symlink out of the root")
	}

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("outside file stat = %v", err)
	}
}

// TestSymlinkOutOfRootIsNotListed verifies List neither lists nor descends into a symlink out of the root.
func TestSymlinkOutOfRootIsNotListed(t *testing.T) {
	t.Parallel()

	driver, outside := newDriverWithEscapeLink(t)

	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	putFilesystemObject(t, driver, "own", []byte("own"))

	page, err := driver.List(t.Context(), storage.ListOptions{})

	if err != nil {
		t.Fatal(err)
	}

	if keys := listedKeys(page); !slices.Equal(keys, []string{"own"}) {
		t.Fatalf("keys = %v, want [own]", keys)
	}

	if _, err := driver.List(t.Context(), storage.ListOptions{
		Prefix: "link/",
	}); err == nil {
		t.Fatal("List walked a symlink out of the root")
	}
}

// TestListNestedPrefixes checks that an empty prefix includes every nested
// object in lexical key order alongside objects at the storage root.
func TestListNestedPrefixes(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)

	for _, key := range []string{"a.txt", "a/b/c1", "a/b/c2", "a/b/d", "a/b/c/x", "a/e", "z"} {
		putFilesystemObject(t, driver, key, []byte(key))
	}

	prefix := ""
	want := []string{"a.txt", "a/b/c/x", "a/b/c1", "a/b/c2", "a/b/d", "a/e", "z"}

	page, err := driver.List(t.Context(), storage.ListOptions{
		Prefix: prefix,
	})

	if err != nil {
		t.Fatalf("List(%q) = %v", prefix, err)
	}

	if keys := listedKeys(page); !slices.Equal(keys, want) {
		t.Fatalf("List(%q) = %v, want %v", prefix, keys, want)
	}
}

// TestListNestedDirectoryPrefix checks a directory prefix includes its direct
// objects and deeper descendants, but excludes siblings and root objects.
func TestListNestedDirectoryPrefix(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)

	for _, key := range []string{"a.txt", "a/b/c1", "a/b/c2", "a/b/d", "a/b/c/x", "a/e", "z"} {
		putFilesystemObject(t, driver, key, []byte(key))
	}

	prefix := "a/b/"
	want := []string{"a/b/c/x", "a/b/c1", "a/b/c2", "a/b/d"}

	page, err := driver.List(t.Context(), storage.ListOptions{
		Prefix: prefix,
	})

	if err != nil {
		t.Fatalf("List(%q) = %v", prefix, err)
	}

	if keys := listedKeys(page); !slices.Equal(keys, want) {
		t.Fatalf("List(%q) = %v, want %v", prefix, keys, want)
	}
}

// TestListNestedPartialNamePrefix checks matching uses the whole key prefix,
// including both partial filenames and a directory with the same leading name.
func TestListNestedPartialNamePrefix(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)

	for _, key := range []string{"a.txt", "a/b/c1", "a/b/c2", "a/b/d", "a/b/c/x", "a/e", "z"} {
		putFilesystemObject(t, driver, key, []byte(key))
	}

	prefix := "a/b/c"
	want := []string{"a/b/c/x", "a/b/c1", "a/b/c2"}

	page, err := driver.List(t.Context(), storage.ListOptions{
		Prefix: prefix,
	})

	if err != nil {
		t.Fatalf("List(%q) = %v", prefix, err)
	}

	if keys := listedKeys(page); !slices.Equal(keys, want) {
		t.Fatalf("List(%q) = %v, want %v", prefix, keys, want)
	}
}

// TestListDeepDirectoryPrefix checks a trailing slash excludes similarly
// named sibling files and keeps only descendants of that exact directory.
func TestListDeepDirectoryPrefix(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)

	for _, key := range []string{"a.txt", "a/b/c1", "a/b/c2", "a/b/d", "a/b/c/x", "a/e", "z"} {
		putFilesystemObject(t, driver, key, []byte(key))
	}

	prefix := "a/b/c/"
	want := []string{"a/b/c/x"}

	page, err := driver.List(t.Context(), storage.ListOptions{
		Prefix: prefix,
	})

	if err != nil {
		t.Fatalf("List(%q) = %v", prefix, err)
	}

	if keys := listedKeys(page); !slices.Equal(keys, want) {
		t.Fatalf("List(%q) = %v, want %v", prefix, keys, want)
	}
}

// TestListRootDirectoryPrefix checks a top-level directory includes all of
// its descendants without including a similarly named file at the root.
func TestListRootDirectoryPrefix(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)

	for _, key := range []string{"a.txt", "a/b/c1", "a/b/c2", "a/b/d", "a/b/c/x", "a/e", "z"} {
		putFilesystemObject(t, driver, key, []byte(key))
	}

	prefix := "a/"
	want := []string{"a/b/c/x", "a/b/c1", "a/b/c2", "a/b/d", "a/e"}

	page, err := driver.List(t.Context(), storage.ListOptions{
		Prefix: prefix,
	})

	if err != nil {
		t.Fatalf("List(%q) = %v", prefix, err)
	}

	if keys := listedKeys(page); !slices.Equal(keys, want) {
		t.Fatalf("List(%q) = %v, want %v", prefix, keys, want)
	}
}

// TestListRootPartialNamePrefix checks a prefix without a trailing slash
// includes both matching root filenames and matching nested directory keys.
func TestListRootPartialNamePrefix(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)

	for _, key := range []string{"a.txt", "a/b/c1", "a/b/c2", "a/b/d", "a/b/c/x", "a/e", "z"} {
		putFilesystemObject(t, driver, key, []byte(key))
	}

	prefix := "a"
	want := []string{"a.txt", "a/b/c/x", "a/b/c1", "a/b/c2", "a/b/d", "a/e"}

	page, err := driver.List(t.Context(), storage.ListOptions{
		Prefix: prefix,
	})

	if err != nil {
		t.Fatalf("List(%q) = %v", prefix, err)
	}

	if keys := listedKeys(page); !slices.Equal(keys, want) {
		t.Fatalf("List(%q) = %v, want %v", prefix, keys, want)
	}
}

// TestListMissingNestedPrefixes checks absent directories and partial paths
// both produce empty results rather than errors from the filesystem walk.
func TestListMissingNestedPrefixes(t *testing.T) {
	t.Parallel()

	driver := newFilesystemDriver(t)

	for _, key := range []string{"a.txt", "a/b/c1", "a/b/c2", "a/b/d", "a/b/c/x", "a/e", "z"} {
		putFilesystemObject(t, driver, key, []byte(key))
	}

	var want []string

	for _, prefix := range []string{"a/missing/", "nope/x"} {
		page, err := driver.List(t.Context(), storage.ListOptions{
			Prefix: prefix,
		})

		if err != nil {
			t.Fatalf("List(%q) = %v", prefix, err)
		}

		if keys := listedKeys(page); !slices.Equal(keys, want) {
			t.Fatalf("List(%q) = %v, want %v", prefix, keys, want)
		}
	}
}

// TestOperationsFailBeforeStartAndAfterStop verifies the driver does not touch the disk outside Start and Stop.
func TestOperationsFailBeforeStartAndAfterStop(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "root")
	driver, err := filesystemstorage.New(root)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Put(t.Context(), "key", bytes.NewReader(nil)); err == nil {
		t.Fatal("Put succeeded before Start")
	}

	if _, err := driver.List(t.Context(), storage.ListOptions{}); err == nil {
		t.Fatal("List succeeded before Start")
	}

	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root stat = %v, want not exist", err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Stat(t.Context(), "key"); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat after Stop = %v, want a not-started error", err)
	}
}

// listedKeys returns the keys of page in order.
func listedKeys(page storage.Page) []string {
	var keys []string

	for _, object := range page.Objects {
		keys = append(keys, object.Key)
	}

	return keys
}
