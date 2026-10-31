package filesystemstorage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"arkoren.dev/core/service/storage"
)

const (
	// directoryMode is the permission mode for storage directories.
	directoryMode fs.FileMode = 0o750

	// fileMode is the permission mode for stored objects.
	fileMode fs.FileMode = 0o640

	// defaultListLimit is the maximum number of objects returned without a limit.
	defaultListLimit = 100

	// temporaryPrefix labels unpublished uploads. Their trailing dot makes the
	// complete filename an invalid portable key, without reserving this prefix
	// for applications. os.Root uses literal filenames, including on Windows.
	temporaryPrefix = ".storage-"
)

// errNotStarted is returned by every object operation called before
// [Driver.Start] or after [Driver.Stop].
var errNotStarted = errors.New("filesystem storage is not started")

// Driver stores objects beneath a local filesystem root. Every file operation
// goes through an [os.Root] opened by [Driver.Start], so a symlink beneath the
// root that points outside it can neither be read, written, nor deleted
// through the driver; such operations fail with an error that is not
// [storage.ErrNotFound]. Symlinks that stay inside the root are followed.
// Operations called before Start or after Stop fail without touching the disk.
// Open and Stat accept only regular files; directories are logical prefixes,
// while other special entries fail with an operational error. Unix FIFO opens
// never wait for a writer. Finish application operations before Stop.
//
// Example:
//
//	driver, err := filesystemstorage.New("./data/uploads")
//	if err != nil {
//		return err
//	}
//	files := storage.New(driver)
//	_ = files.Driver()
type Driver struct {
	// root is the absolute, cleaned filesystem root.
	root string

	// handle is the open root, set by Start and cleared by Stop. It is atomic
	// so publication and removal are race-safe. Operations racing Stop can also
	// observe the native root's closed-handle error after loading this pointer.
	handle atomic.Pointer[os.Root]
}

// New returns a [Driver] rooted at the absolute clean form of root, the
// directory objects are stored beneath. It performs no disk IO;
// [Driver.Start] creates the directory when absent and opens it. New fails
// when root is empty.
//
// Example:
//
//	driver, err := filesystemstorage.New("./data/uploads")
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(root string) (*Driver, error) {
	if root == "" {
		return nil, errors.New("storage root is empty")
	}

	absoluteRoot, err := filepath.Abs(root)

	if err != nil {
		return nil, fmt.Errorf("resolve storage root: %w", err)
	}

	return &Driver{
		root: filepath.Clean(absoluteRoot),
	}, nil
}

// Start creates the storage root when absent and opens it as an [os.Root],
// which fails when the path is not a directory. Starting a started driver is a
// no-op.
// It returns cancellation before filesystem I/O and creates no logical objects.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if driver.handle.Load() != nil {
		return nil
	}

	if err := os.MkdirAll(driver.root, directoryMode); err != nil {
		return fmt.Errorf("create storage root: %w", err)
	}

	handle, err := os.OpenRoot(driver.root)

	if err != nil {
		return fmt.Errorf("open storage root: %w", err)
	}

	if !driver.handle.CompareAndSwap(nil, handle) {
		_ = handle.Close() // A concurrent Start won; keep its root.
	}

	return nil
}

// Stop closes the storage root. It does not block, so ctx is not consulted.
// Objects already returned by [Driver.Open] stay readable; later operations
// fail until the driver is started again.
// It does not wait for readers or writers; end application work first. A call
// racing Stop can report a native closed-root error instead of errNotStarted.
//
// Example:
//
//	if err := driver.Stop(context.Background()); err != nil {
//		return err
//	}
func (driver *Driver) Stop(context.Context) error {
	handle := driver.handle.Swap(nil)

	if handle == nil {
		return nil
	}

	return handle.Close()
}

// Open returns a readable object body. The caller must close the body. Missing objects return
// an error wrapping [storage.ErrNotFound].
// Directories are missing logical objects. Other non-regular entries fail with
// an operational error; Unix FIFOs are opened nonblocking before checking the
// actual handle, so even a concurrently substituted FIFO cannot wait for a writer.
//
// Example:
//
//	object, err := driver.Open(ctx, "reports/a.txt")
//	if err != nil {
//		return err
//	}
//	defer object.Contents.Close()
//	_, err = io.Copy(output, object.Contents)
//	return err
func (driver *Driver) Open(ctx context.Context, key string) (storage.Object, error) {
	root, err := driver.resolve(ctx, key)

	if err != nil {
		return storage.Object{}, err
	}

	file, err := root.OpenFile(key, objectReadFlags, 0)

	if err != nil {
		return storage.Object{}, mapNotFound("open", key, err)
	}

	info, err := file.Stat()

	if err != nil {
		// Preserve the stat failure; closing the rejected read handle is best-effort cleanup.
		_ = file.Close()

		return storage.Object{}, fmt.Errorf("stat opened object %q: %w", key, err)
	}

	if !info.Mode().IsRegular() {
		// No object will be returned; closing the rejected entry is best-effort cleanup.
		_ = file.Close()

		if info.IsDir() {
			return storage.Object{}, fmt.Errorf("open %q: is a directory: %w", key, storage.ErrNotFound)
		}

		return storage.Object{}, fmt.Errorf("open %q: not a regular file", key)
	}

	return storage.Object{
		ObjectInfo: objectInfo(key, info),
		Contents:   file,
	}, nil
}

// Put atomically replaces key after the complete reader has been written to a
// temporary file beside it. The temporary file is removed when the write fails.
// Staging filenames end in a dot, outside the portable key grammar, so List
// excludes unpublished bytes without reserving any valid .storage- object keys.
// The caller owns contents; Put consumes but does not close it. Cancellation is
// checked before I/O and after copying; a reader blocked in Read must cooperate.
//
// Example:
//
//	info, err := driver.Put(ctx, "reports/a.txt", strings.NewReader("ready"))
//	if err != nil {
//		return err
//	}
//	_ = info.Size
func (driver *Driver) Put(ctx context.Context, key string, contents io.Reader) (storage.ObjectInfo, error) {
	root, err := driver.resolve(ctx, key)

	if err != nil {
		return storage.ObjectInfo{}, err
	}

	directory := path.Dir(key)

	if err := root.MkdirAll(directory, directoryMode); err != nil {
		return storage.ObjectInfo{}, fmt.Errorf("create object directory: %w", err)
	}

	temporaryName := path.Join(directory, temporaryPrefix+rand.Text()+".")
	temporary, err := root.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)

	if err != nil {
		return storage.ObjectInfo{}, fmt.Errorf("create temporary object: %w", err)
	}

	if err := writeTemporaryObject(ctx, key, temporary, contents); err != nil {
		// Preserve the write failure; removing the unpublished file is best-effort cleanup.
		_ = root.Remove(temporaryName)

		return storage.ObjectInfo{}, err
	}

	if err := root.Rename(temporaryName, key); err != nil {
		// Preserve the rename failure; removing the unpublished file is best-effort cleanup.
		_ = root.Remove(temporaryName)

		return storage.ObjectInfo{}, fmt.Errorf("replace object %q: %w", key, err)
	}

	info, err := root.Stat(key)

	if err != nil {
		return storage.ObjectInfo{}, fmt.Errorf("stat object %q: %w", key, err)
	}

	return objectInfo(key, info), nil
}

// Stat returns metadata for key without opening its contents. Missing objects return an error
// wrapping [storage.ErrNotFound].
// A directory is only a prefix and reports the same sentinel. Other special
// entries return an operational error instead of metadata for a regular object.
//
// Example:
//
//	info, err := driver.Stat(ctx, "reports/a.txt")
//	if err != nil {
//		return err
//	}
//	_ = info.ModifiedAt
func (driver *Driver) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	root, err := driver.resolve(ctx, key)

	if err != nil {
		return storage.ObjectInfo{}, err
	}

	info, err := root.Stat(key)

	if err != nil {
		return storage.ObjectInfo{}, mapNotFound("stat", key, err)
	}

	if info.IsDir() {
		return storage.ObjectInfo{}, fmt.Errorf("stat %q: is a directory: %w", key, storage.ErrNotFound)
	}

	if !info.Mode().IsRegular() {
		return storage.ObjectInfo{}, fmt.Errorf("stat %q: not a regular file", key)
	}

	return objectInfo(key, info), nil
}

// SignURL validates the request in the order documented on [storage.Driver.SignURL] and then
// returns an error wrapping [storage.ErrUnsupported]: files on disk have no URL a client could
// fetch.
// It performs no filesystem I/O and does not require Start.
//
// Example:
//
//	_, err := driver.SignURL(ctx, "reports/a.txt", storage.SignOptions{TTL: time.Minute})
//	if !errors.Is(err, storage.ErrUnsupported) {
//		return err
//	}
func (driver *Driver) SignURL(ctx context.Context, key string, options storage.SignOptions) (*url.URL, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}

	if options.TTL <= 0 {
		return nil, fmt.Errorf("sign %q: ttl %s must be positive: %w", key, options.TTL, storage.ErrInvalidTTL)
	}

	if options.Method != "" && options.Method != http.MethodGet && options.Method != http.MethodPut {
		return nil, fmt.Errorf("sign %q: method %q: %w", key, options.Method, storage.ErrUnsupported)
	}

	if err := options.ValidateBinding(); err != nil {
		return nil, fmt.Errorf("sign %q: %w", key, err)
	}

	return nil, fmt.Errorf("sign %q: filesystem storage has no URLs: %w", key, storage.ErrUnsupported)
}

// Delete removes a regular file and succeeds when it does not exist. A
// directory is only a prefix of other objects and is left alone.
// Another caller removing the same file between lookup and removal is also a
// successful absence. Errors other than absence remain visible.
//
// Example:
//
//	if err := driver.Delete(ctx, "reports/a.txt"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, key string) error {
	root, err := driver.resolve(ctx, key)

	if err != nil {
		return err
	}

	info, err := root.Stat(key)

	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("stat object %q: %w", key, err)
	}

	if info.IsDir() {
		return nil
	}

	if err := root.Remove(key); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete object %q: %w", key, err)
	}

	return nil
}

// List returns object metadata sorted by key for keys matching options.Prefix.
// Only regular files with valid portable keys are listed. Unpublished staging
// files have invalid portable names; valid .storage- keys are ordinary objects.
// The walk starts at the deepest
// directory the prefix implies, so listing "a/b/" or "a/b/c" never reads
// siblings of "a/b".
// Nonpositive limits default to 100; NextCursor is empty at the final page.
//
// Example:
//
//	page, err := driver.List(ctx, storage.ListOptions{Prefix: "reports/", Limit: 20})
//	if err != nil {
//		return err
//	}
//	_ = page.NextCursor
func (driver *Driver) List(ctx context.Context, options storage.ListOptions) (storage.Page, error) {
	if err := validateListing(ctx, options); err != nil {
		return storage.Page{}, err
	}

	root := driver.handle.Load()

	if root == nil {
		return storage.Page{}, fmt.Errorf("list objects: %w", errNotStarted)
	}

	objects, err := listingObjects(ctx, root.FS(), options)

	if err != nil {
		return storage.Page{}, fmt.Errorf("list objects: %w", err)
	}

	// WalkDir orders each directory's entries, not full keys ("a/b" sorts
	// after "a.txt" by key but is visited first), so sort globally.
	slices.SortFunc(objects, func(left storage.ObjectInfo, right storage.ObjectInfo) int {
		return strings.Compare(left.Key, right.Key)
	})

	return filesystemStoragePage(objects, options.Limit), nil
}

// resolve checks ctx, validates key, and returns the open root. A validated key
// is already a slash-separated path relative to the root, so callers pass it to
// the root unchanged. It fails with [errNotStarted] before Start and after
// Stop. Containment is not decided here: every operation on the returned root
// is confined to it by the operating system, including through symlinks.
func (driver *Driver) resolve(ctx context.Context, key string) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}

	root := driver.handle.Load()

	if root == nil {
		return nil, errNotStarted
	}

	return root, nil
}

// writeTemporaryObject copies contents into temporary, syncs, sets the object
// mode, and closes it. It closes temporary on every path.
func writeTemporaryObject(ctx context.Context, key string, temporary *os.File, contents io.Reader) error {
	if _, err := io.Copy(temporary, contents); err != nil {
		// Preserve the copy failure; the caller will remove this unpublished file.
		_ = temporary.Close()

		return fmt.Errorf("write object %q: %w", key, err)
	}

	if err := ctx.Err(); err != nil {
		// Preserve cancellation; the caller will remove this unpublished file.
		_ = temporary.Close()

		return err
	}

	// Sync before the rename, so a crash cannot leave a renamed but empty file.
	if err := temporary.Sync(); err != nil {
		// Preserve the sync failure; the caller will remove this unpublished file.
		_ = temporary.Close()

		return fmt.Errorf("sync object: %w", err)
	}

	// Chmod because the creation mode is subject to the process umask.
	if err := temporary.Chmod(fileMode); err != nil {
		// Preserve the mode failure; the caller will remove this unpublished file.
		_ = temporary.Close()

		return fmt.Errorf("set object mode: %w", err)
	}

	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary object: %w", err)
	}

	return nil
}

// filesystemStoragePage returns the configured page of sorted objects.
func filesystemStoragePage(objects []storage.ObjectInfo, limit int) storage.Page {
	if limit <= 0 {
		limit = defaultListLimit
	}

	page := storage.Page{
		Objects: objects,
	}

	if len(objects) <= limit {
		return page
	}

	page.Objects = objects[:limit]
	page.NextCursor = page.Objects[len(page.Objects)-1].Key

	return page
}

// objectInfo creates portable metadata from filesystem information.
func objectInfo(key string, info fs.FileInfo) storage.ObjectInfo {
	return storage.ObjectInfo{
		Key:        key,
		Size:       info.Size(),
		ModifiedAt: info.ModTime(),
	}
}

// mapNotFound maps a missing filesystem object error to the storage sentinel.
func mapNotFound(operation string, key string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s %q: %w", operation, key, storage.ErrNotFound)
	}

	return fmt.Errorf("%s %q: %w", operation, key, err)
}

var _ storage.Driver = (*Driver)(nil)
