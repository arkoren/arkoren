package memorystorage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"arkoren.dev/core/service/storage"
)

// defaultListLimit is the page size used when neither the request nor
// [Builder.ListLimit] sets one.
const defaultListLimit = 100

// settings configures a [Driver].
type settings struct {
	// ListLimit is the page size [Driver.List] uses when the request sets no
	// limit. Zero or negative uses 100.
	ListLimit int
}

// defaultConfig returns the default in-memory storage configuration.
func defaultConfig() settings {
	return settings{ListLimit: defaultListLimit}
}

// value holds immutable object contents and metadata while stored.
type value struct {
	// contents is an owned copy of the object bytes.
	contents []byte

	// modifiedAt is the time at which the object was last written.
	modifiedAt time.Time
}

// Driver stores owned object snapshots in process memory and is safe for
// concurrent use. Create it with New; process exit loses every object. It needs
// no Start/Stop calls and cannot issue externally fetchable signed URLs.
//
// Example:
//
//	driver := memorystorage.New()
//	info, err := driver.Put(ctx, "report.txt", strings.NewReader("report"))
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Size) // 6
type Driver struct {
	// mutex protects values.
	mutex sync.RWMutex

	// values maps object keys to their stored values.
	values map[string]value

	// listLimit is the page size used when a request sets no limit.
	listLimit int
}

// New creates an empty concurrent [Driver] with the completed construction policies.
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
//
// Example:
//
//	memorystorage.New(func(store *memorystorage.Builder) {
//		store.ListLimit(25)
//	})
func New(configure ...BuilderFunc) *Driver {
	config := newSettings(configure...)

	if config.ListLimit <= 0 {
		config.ListLimit = defaultListLimit
	}

	return &Driver{values: make(map[string]value), listLimit: config.ListLimit}
}

// readSeekCloser adds a no-op Close to a [bytes.Reader]. Unlike
// [io.NopCloser] it keeps Seek visible, so [storage.Object.Contents]
// implements [io.ReadSeeker] and helpers such as [storage.Storage.Download]
// can serve range requests.
type readSeekCloser struct {
	// Reader provides seeking over the caller's independent content snapshot.
	*bytes.Reader
}

// Close does nothing: the snapshot is plain memory owned by the garbage collector.
func (readSeekCloser) Close() error {
	return nil
}

// Open returns an independent snapshot of the object contents, whose Contents
// also implements [io.ReadSeeker]. Missing objects return an error
// wrapping [storage.ErrNotFound].
// Replacing or deleting the stored object never changes an already opened body.
//
// Example:
//
//	object, err := driver.Open(ctx, "report.txt")
//	if err != nil {
//		return err
//	}
//	_, err = io.Copy(destination, object.Contents)
//	return errors.Join(err, object.Contents.Close())
func (driver *Driver) Open(ctx context.Context, key string) (storage.Object, error) {
	if err := ctx.Err(); err != nil {
		return storage.Object{}, err
	}

	if err := storage.ValidateKey(key); err != nil {
		return storage.Object{}, err
	}

	driver.mutex.RLock()

	value, found := driver.values[key]
	contents := append([]byte(nil), value.contents...)
	driver.mutex.RUnlock()

	if !found {
		return storage.Object{}, fmt.Errorf("open %q: %w", key, storage.ErrNotFound)
	}

	return storage.Object{
		Key: key, Size: int64(len(contents)), ModifiedAt: value.modifiedAt,
		Contents: readSeekCloser{bytes.NewReader(contents)},
	}, nil
}

// Put reads contents once and replaces key with an independent copy. It checks
// cancellation between reads and before publication, leaving an existing object
// untouched on failure. It neither closes contents nor interrupts a Read already
// blocked inside the caller's reader.
//
// Example:
//
//	driver := memorystorage.New()
//	info, err := driver.Put(ctx, "reports/a.txt", strings.NewReader("report"))
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Size) // 6
func (driver *Driver) Put(ctx context.Context, key string, contents io.Reader) (storage.ObjectInfo, error) {
	if err := validatePutInput(ctx, key); err != nil {
		return storage.ObjectInfo{}, err
	}

	data, err := io.ReadAll(cancelableReader{ctx: ctx, contents: contents})

	if err != nil {
		return storage.ObjectInfo{}, err
	}

	value := value{contents: append([]byte(nil), data...), modifiedAt: time.Now()}
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}

	driver.values[key] = value

	return storage.ObjectInfo{
		Key:        key,
		Size:       int64(len(data)),
		ModifiedAt: value.modifiedAt,
	}, nil
}

// cancelableReader checks a request's lifetime between reads without taking
// ownership of its contents. It cannot interrupt an underlying blocked Read.
type cancelableReader struct {
	// ctx controls whether another read may begin, retaining the caller's
	// cancellation and deadline rather than detaching the upload lifetime.
	ctx context.Context

	// contents remains caller-owned and is consumed only by the active Put.
	// The wrapper never closes it, even when it also implements io.Closer.
	contents io.Reader
}

// Read refuses to consume another chunk after cancellation. Successful reads
// preserve the underlying reader's byte count and error unchanged.
func (reader cancelableReader) Read(destination []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}

	return reader.contents.Read(destination)
}

// SignURL validates the request in the order documented on [storage.Driver.SignURL] and then
// returns an error wrapping [storage.ErrUnsupported]: objects in process memory have no URL a
// client could fetch.
//
// Example:
//
//	driver := memorystorage.New()
//	_, err := driver.SignURL(context.Background(), "report.pdf", storage.SignOptions{TTL: time.Minute})
//	fmt.Println(errors.Is(err, storage.ErrUnsupported)) // true
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

	return nil, fmt.Errorf("sign %q: memory storage has no URLs: %w", key, storage.ErrUnsupported)
}

// validatePutInput checks cancellation before key syntax, matching the Driver
// contract without consuming a reader for a canceled or malformed request.
func validatePutInput(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return storage.ValidateKey(key)
}

// Stat returns object metadata without opening its contents. Missing objects return an error
// wrapping [storage.ErrNotFound].
// Metadata is returned by value and does not expose the stored byte slice.
//
// Example:
//
//	info, err := driver.Stat(ctx, "report.txt")
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Size)
func (driver *Driver) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}

	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}

	driver.mutex.RLock()

	value, found := driver.values[key]
	driver.mutex.RUnlock()

	if !found {
		return storage.ObjectInfo{}, fmt.Errorf("stat %q: %w", key, storage.ErrNotFound)
	}

	return storage.ObjectInfo{
		Key:        key,
		Size:       int64(len(value.contents)),
		ModifiedAt: value.modifiedAt,
	}, nil
}

// Delete removes key and succeeds when it is absent. Cancellation precedes key
// validation; open snapshots remain readable after a successful deletion.
//
// Example:
//
//	driver := memorystorage.New()
//	if err := driver.Delete(ctx, "obsolete.txt"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	driver.mutex.Lock()
	delete(driver.values, key)
	driver.mutex.Unlock()

	return nil
}

// List returns an owned, lexically sorted page and a cursor only when more keys
// match. One read lock covers selection, metadata, and lookahead. Scanning and
// metadata copying check cancellation between entries; sorting and mutex waits
// are synchronous and are checked after completion, not forcibly interrupted.
// A canceled operation returns no partial page. Concurrent changes between calls
// do not provide a multi-page snapshot.
//
// Example:
//
//	page, err := driver.List(ctx, storage.ListOptions{Prefix: "reports/", Limit: 20})
//	if err != nil {
//		return err
//	}
//	for _, info := range page.Objects {
//		fmt.Println(info.Key)
//	}
func (driver *Driver) List(ctx context.Context, options storage.ListOptions) (storage.Page, error) {
	if err := validateStorageListPrefixAndContext(ctx, options.Prefix); err != nil {
		return storage.Page{}, err
	}

	if err := validateStorageListCursor(options.Cursor); err != nil {
		return storage.Page{}, err
	}

	if err := validateStorageCursorProgress(options.Cursor, options.Prefix); err != nil {
		return storage.Page{}, err
	}

	return driver.listObjects(ctx, options)
}

// validateStorageListPrefixAndContext checks cancellation before prefix syntax.
// Neither a canceled call nor an invalid prefix begins a map scan.
func validateStorageListPrefixAndContext(ctx context.Context, prefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return storage.ValidatePrefix(prefix)
}

// validateStorageListCursor validates the optional list cursor syntax.
func validateStorageListCursor(cursor string) error {
	if cursor == "" {
		return nil
	}

	return storage.ValidateKey(cursor)
}

// validateStorageCursorProgress verifies that a cursor belongs to its prefix.
func validateStorageCursorProgress(cursor string, prefix string) error {
	if cursor != "" && !strings.HasPrefix(cursor, prefix) {
		return fmt.Errorf("invalid storage cursor %q: %w", cursor, storage.ErrInvalidKey)
	}

	return nil
}

// listObjects builds one owned page under the same read lock as its lookahead.
// A canceled scan or metadata copy returns no partial page. Sorting is bounded
// synchronous work; cancellation is checked immediately after it completes.
func (driver *Driver) listObjects(ctx context.Context, options storage.ListOptions) (storage.Page, error) {
	driver.mutex.RLock()
	defer driver.mutex.RUnlock()

	keys, err := matchingStorageKeys(ctx, driver.values, options)

	if err != nil {
		return storage.Page{}, err
	}

	limit := options.Limit

	if limit <= 0 {
		limit = driver.listLimit
	}

	page := storage.Page{}

	if len(keys) > limit {
		page.NextCursor = keys[limit-1]
		keys = keys[:limit]
	}

	page.Objects, err = storageObjectInfos(ctx, driver.values, keys)

	if err != nil {
		return storage.Page{}, err
	}

	return page, nil
}

// matchingStorageKeys selects keys after the cursor using literal prefix
// matching. It checks cancellation during the scan and after sorting, so callers
// never publish a successful page from an interrupted listing.
func matchingStorageKeys(ctx context.Context, values map[string]value, options storage.ListOptions) ([]string, error) {
	keys := make([]string, 0, len(values))

	for key := range values {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if key > options.Cursor && strings.HasPrefix(key, options.Prefix) {
			keys = append(keys, key)
		}
	}

	sort.Strings(keys)

	return keys, ctx.Err()
}

// storageObjectInfos copies metadata in the supplied key order while the caller
// holds the map's read lock. Cancellation discards the entire partial result.
func storageObjectInfos(ctx context.Context, values map[string]value, keys []string) ([]storage.ObjectInfo, error) {
	objects := make([]storage.ObjectInfo, 0, len(keys))

	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		value := values[key]
		objects = append(objects, storage.ObjectInfo{
			Key:        key,
			Size:       int64(len(value.contents)),
			ModifiedAt: value.modifiedAt,
		})
	}

	return objects, ctx.Err()
}

var _ storage.Driver = (*Driver)(nil)

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	memorystorage.New(func(store *memorystorage.Builder) {
//		store.ListLimit(25)
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...BuilderFunc) settings {
	storage := &Builder{settings: defaultConfig()}

	for _, configure := range configure {
		configure(storage)
	}

	return storage.settings
}

// ListLimit sets the page size used when ListOptions has no positive limit.
// Zero or negative values resolve to 100 after all callbacks. Explicit positive
// request limits retain precedence over this construction default.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	memorystorage.New(func(store *memorystorage.Builder) {
//		store.ListLimit(25)
//	})
func (store *Builder) ListLimit(limit int) *Builder {
	store.settings.ListLimit = limit

	return store
}

// BuilderFunc is a construction policy for New. Policies run directly once in
// order on one draft; retained drafts cannot change an already published driver.
// Nil entries panic, while an expanded nil or empty policy slice makes no edits.
//
// Example:
//
//	policies := []memorystorage.BuilderFunc{
//		func(store *memorystorage.Builder) {
//			store.ListLimit(25)
//		},
//	}
//	driver := memorystorage.New(policies...)
//	page, err := driver.List(ctx, storage.ListOptions{})
//	_ = page
//	return err
type BuilderFunc = func(*Builder)
