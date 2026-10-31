package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

// ErrNotFound indicates that Open or Stat found no logical object. Drivers wrap
// this identity; the facade preserves it rather than parsing provider messages.
//
// Example:
//
//	_, err := files.Stat(ctx, "reports/missing.pdf")
//	if errors.Is(err, storage.ErrNotFound) {
//		return nil // An absent optional report needs no recovery.
//	}
//	return err
var ErrNotFound = errors.New("storage object not found")

// ErrInvalidKey indicates that a key or prefix fails the literal portable
// syntax. Correct or migrate the name; retries never repair invalid input.
//
// Example:
//
//	err := storage.ValidateKey("../private")
//	fmt.Println(errors.Is(err, storage.ErrInvalidKey)) // true
var ErrInvalidKey = errors.New("storage invalid key")

// ErrUnsupported indicates that a driver cannot perform the requested operation, such as
// signing a URL for a backend without presigned access.
// Invalid signing input keeps its own error before capability rejection.
//
// Example:
//
//	_, err := files.SignURL(ctx, "report.pdf", storage.SignOptions{TTL: time.Minute})
//	if errors.Is(err, storage.ErrUnsupported) {
//		http.Redirect(writer, request, "/reports/download", http.StatusSeeOther)
//		return nil // The application route streams the authorized object.
//	}
//	return err
var ErrUnsupported = errors.New("storage unsupported operation")

// ErrInvalidTTL indicates a signing TTL that is not positive or exceeds the driver's maximum.
// It does not mean a signed URL has merely expired after successful creation.
//
// Example:
//
//	_, err := files.SignURL(ctx, "report.pdf", storage.SignOptions{})
//	fmt.Println(errors.Is(err, storage.ErrInvalidTTL)) // true
var ErrInvalidTTL = errors.New("storage invalid ttl")

// ErrInvalidSignOptions indicates [SignOptions] fields that cannot be honored, such as a
// ContentType or ContentLength on a GET URL or a negative ContentLength.
// Binding validation is available without contacting a backend.
//
// Example:
//
//	err := (storage.SignOptions{ContentLength: -1}).ValidateBinding()
//	fmt.Println(errors.Is(err, storage.ErrInvalidSignOptions)) // true
var ErrInvalidSignOptions = errors.New("storage invalid sign options")

// ObjectInfo describes an object without opening its contents. Keys are logical
// names, Size counts bytes, and ModifiedAt is supplied by the backend.
//
// Example:
//
//	info, err := files.Stat(ctx, "reports/a.pdf")
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Key, info.Size)
type ObjectInfo struct {
	// Key identifies the object with a portable storage key.
	//
	// Example:
	//
	//	info := storage.ObjectInfo{Key: "reports/a.pdf"}
	//	fmt.Println(info.Key) // reports/a.pdf
	Key string

	// Size is the object length in bytes.
	//
	// Example:
	//
	//	info := storage.ObjectInfo{Size: 1024}
	//	fmt.Println(info.Size) // 1024
	Size int64

	// ModifiedAt is the object's most recent modification time.
	//
	// Example:
	//
	//	info := storage.ObjectInfo{ModifiedAt: time.Unix(0, 0).UTC()}
	//	fmt.Println(info.ModifiedAt.Format(time.RFC3339)) // 1970-01-01T00:00:00Z
	ModifiedAt time.Time
}

// Object combines metadata with an open body owned by its caller. Opening does
// not read all bytes; callers must close Contents after consumption or failure.
//
// Example:
//
//	object, err := files.Open(ctx, "report.pdf")
//	if err != nil {
//		return err
//	}
//	_, err = io.Copy(destination, object.Contents)
//	return errors.Join(err, object.Contents.Close())
type Object struct {
	// ObjectInfo describes the opened object's metadata.
	//
	// Example:
	//
	//	object := storage.Object{ObjectInfo: storage.ObjectInfo{Key: "report.pdf"}}
	//	fmt.Println(object.ObjectInfo.Key) // report.pdf
	ObjectInfo

	// Contents is the object body. Callers must close it. It may also
	// implement [io.Seeker]; the memory and filesystem drivers do. Helpers
	// such as [Storage.Download] use that to answer range and conditional
	// requests. Seeking is optional: the conformance suite does not require
	// it, so check with a type assertion and never assume it.
	//
	// Example:
	//
	//	object := storage.Object{Contents: io.NopCloser(strings.NewReader("report"))}
	//	_, err := io.Copy(destination, object.Contents)
	//	return errors.Join(err, object.Contents.Close())
	Contents io.ReadCloser
}

// ListOptions selects a lexical prefix and one driver-defined page. Cursors are
// opaque continuations for the same driver and prefix, not snapshot tokens.
//
// Example:
//
//	page, err := files.List(ctx, storage.ListOptions{Prefix: "reports/", Limit: 20})
//	if err != nil {
//		return err
//	}
//	fmt.Println(len(page.Objects))
type ListOptions struct {
	// Prefix limits the listing to matching keys.
	//
	// Example:
	//
	//	options := storage.ListOptions{Prefix: "reports/"}
	//	page, err := files.List(ctx, options)
	//	_ = page // Check err before using the returned objects.
	//	return err
	Prefix string

	// Cursor continues a prior page from the same driver and prefix.
	//
	// Example:
	//
	//	options := storage.ListOptions{Prefix: "reports/", Cursor: page.NextCursor}
	//	next, err := files.List(ctx, options)
	//	_ = next
	//	return err
	Cursor string

	// Limit caps each page's returned objects, including pages fetched by
	// [Storage.Walk]; it is not a traversal-wide limit. Values at or below zero
	// use a driver default. Use Walk(...).Take(n) to limit consumed objects.
	//
	// Example:
	//
	//	options := storage.ListOptions{Limit: 20}
	//	page, err := files.List(ctx, options)
	//	_ = page
	//	return err
	Limit int
}

// Page is an owned page of metadata. Mutating its outer Objects slice does not
// change storage. A continuation exists only when NextCursor is nonempty.
//
// Example:
//
//	page, err := files.List(ctx, storage.ListOptions{Prefix: "reports/"})
//	if err != nil {
//		return err
//	}
//	for _, info := range page.Objects {
//		fmt.Println(info.Key)
//	}
type Page struct {
	// Objects contains the objects selected for this page.
	//
	// Example:
	//
	//	page := storage.Page{Objects: []storage.ObjectInfo{{Key: "report.pdf"}}}
	//	fmt.Println(page.Objects[0].Key) // report.pdf
	Objects []ObjectInfo

	// NextCursor continues this listing, or is empty when no further page exists.
	//
	// Example:
	//
	//	if page.NextCursor != "" {
	//		next, err := files.List(ctx, storage.ListOptions{Prefix: "reports/", Cursor: page.NextCursor})
	//		_ = next
	//		return err
	//	}
	NextCursor string
}

// SignOptions grants one GET or PUT for a positive TTL, subject to backend
// limits. Signed URLs are bearer credentials: share them only with authorized
// recipients, never through ordinary logs or telemetry.
//
// Example:
//
//	options := storage.SignOptions{
//		Method: http.MethodPut, TTL: 10 * time.Minute,
//		ContentType: "image/png", ContentLength: 1024,
//	}
//	signed, err := files.SignURL(ctx, "avatars/42.png", options)
//	_ = signed // Return only to the authorized uploader.
//	return err
type SignOptions struct {
	// Method is the HTTP method the URL grants. Empty selects http.MethodGet;
	// http.MethodPut is the only other supported value.
	//
	// Example:
	//
	//	options := storage.SignOptions{Method: http.MethodPut, TTL: time.Minute}
	//	_, err := files.SignURL(ctx, "upload.png", options)
	//	return err
	Method string

	// TTL is how long the URL stays valid. It must be positive.
	//
	// Example:
	//
	//	options := storage.SignOptions{TTL: 15 * time.Minute}
	//	_, err := files.SignURL(ctx, "report.pdf", options)
	//	return err
	TTL time.Duration

	// ContentType binds the Content-Type of a PUT: a driver that supports it signs the
	// value into the URL, so an upload with any other Content-Type is rejected by the
	// backend. Empty leaves the type unbound. It is valid only with http.MethodPut.
	//
	// Example:
	//
	//	options := storage.SignOptions{Method: http.MethodPut, ContentType: "image/png"}
	//	return options.ValidateBinding()
	ContentType string

	// ContentLength binds the exact body size in bytes of a PUT. Zero leaves the size
	// unbound; negative values are invalid. It is valid only with http.MethodPut. Backends
	// such as S3 can presign only an exact length, not a maximum; ranges need a different
	// mechanism (S3 presigned POST policies), which this API does not offer.
	//
	// For user uploads, callers should set both ContentType and ContentLength, or the URL
	// holder chooses the type and size of the stored object.
	//
	// Example:
	//
	//	options := storage.SignOptions{Method: http.MethodPut, ContentLength: 1024}
	//	return options.ValidateBinding()
	ContentLength int64
}

// ValidateBinding checks ContentType and ContentLength against Method: they are valid only
// for PUT (empty Method means GET), and ContentLength must not be negative. It returns an
// error wrapping [ErrInvalidSignOptions]. Drivers call it after the method check so every
// driver reports the same error.
// This method checks bindings only, not TTL, key syntax, or backend support.
//
// Example:
//
//	options := storage.SignOptions{Method: http.MethodPut, ContentLength: 1024}
//	if err := options.ValidateBinding(); err != nil {
//		return err
//	}
func (options SignOptions) ValidateBinding() error {
	if options.ContentLength < 0 {
		return fmt.Errorf("content length %d must not be negative: %w", options.ContentLength, ErrInvalidSignOptions)
	}

	if options.Method != http.MethodPut && (options.ContentType != "" || options.ContentLength != 0) {
		return fmt.Errorf("content type and length apply only to PUT: %w", ErrInvalidSignOptions)
	}

	return nil
}

// Driver stores and retrieves portable logical objects. Direct callers must supply keys and
// prefixes accepted by ValidateKey and ValidatePrefix; drivers reject others with an error
// wrapping ErrInvalidKey. Implementations must return errors wrapping ErrNotFound when Open
// or Stat cannot find a valid key.
//
// A key that exists only as the leading path of other keys, such as "dir" when only "dir/a"
// is stored, is not an object: Open and Stat report it missing and Delete succeeds without
// touching anything. Storing both "a" and "a/b" is not portable, because a filesystem cannot
// hold both, so callers should not rely on it.
//
// Every operation fails with an error wrapping the context's error when ctx is already
// canceled, and then changes nothing.
//
// Example:
//
//	var driver storage.Driver = memorystorage.New()
//	info, err := driver.Put(ctx, "report.txt", strings.NewReader("report"))
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Size) // 6
type Driver interface {
	// Open returns an object body that the caller must close. Missing objects return an error
	// wrapping ErrNotFound.
	//
	// Example:
	//
	//	object, err := driver.Open(ctx, "report.pdf")
	//	if err != nil {
	//		return err
	//	}
	//	_, err = io.Copy(destination, object.Contents)
	//	return errors.Join(err, object.Contents.Close())
	Open(context.Context, string) (Object, error)

	// Put replaces an object with the complete contents from the reader and returns the stored
	// object's metadata. When the reader fails, the previous object, if any, is left intact and
	// no new object appears.
	// The caller retains ownership of the reader; drivers must not close it.
	//
	// Example:
	//
	//	info, err := driver.Put(ctx, "report.txt", strings.NewReader("report"))
	//	_ = info
	//	return err
	Put(context.Context, string, io.Reader) (ObjectInfo, error)

	// Stat returns object metadata without opening its contents. Missing objects return an error
	// wrapping ErrNotFound.
	//
	// Example:
	//
	//	info, err := driver.Stat(ctx, "report.pdf")
	//	if err != nil {
	//		return err
	//	}
	//	fmt.Println(info.Size)
	Stat(context.Context, string) (ObjectInfo, error)

	// Delete removes an object and succeeds if the object is absent.
	//
	// Example:
	//
	//	if err := driver.Delete(ctx, "obsolete/report.pdf"); err != nil {
	//		return err
	//	}
	Delete(context.Context, string) error

	// List returns a page of object metadata for a prefix. Prefix is a plain string prefix of
	// full keys, so "reports" also matches "reports-2024/a". Objects carry full keys and are
	// ordered lexicographically by key. A Limit at or below zero selects a driver default, and
	// a driver may return fewer objects than requested. NextCursor is empty exactly when no
	// further objects match, even if the final page is full.
	//
	// Example:
	//
	//	page, err := driver.List(ctx, storage.ListOptions{Prefix: "reports/", Limit: 20})
	//	_ = page
	//	return err
	List(context.Context, ListOptions) (Page, error)

	// SignURL returns a URL that grants options.Method on key without further credentials until
	// options.TTL elapses. GET returns the object body; PUT replaces the object with the same
	// semantics as Put. Signing does not check existence: a GET URL for a missing key fails
	// when it is fetched.
	//
	// Validation is deterministic for every driver and runs in this order: a canceled ctx
	// returns an error wrapping the context's error; an invalid key returns ErrInvalidKey; a
	// TTL at or below zero, or above the driver's maximum, returns ErrInvalidTTL; a method
	// other than GET or PUT (empty means GET) returns ErrUnsupported; a ContentType or
	// ContentLength on a GET, or a negative ContentLength, returns ErrInvalidSignOptions (see
	// [SignOptions.ValidateBinding]); a driver without signing support returns ErrUnsupported.
	//
	// A driver that can bind ContentType and ContentLength into the signature does so, so the
	// backend rejects uploads that differ. Callers should set both for user uploads.
	//
	// A driver that wraps another must sign the key it would actually store, for example the
	// prefixed one.
	//
	// Example:
	//
	//	signed, err := driver.SignURL(ctx, "report.pdf", storage.SignOptions{TTL: time.Minute})
	//	_ = signed // A bearer credential, not a diagnostic value.
	//	return err
	SignURL(context.Context, string, SignOptions) (*url.URL, error)
}

// Storage validates portable requests before delegating to a driver. It preserves driver
// error identities and does not classify provider errors.
// It borrows the driver and adds no lifecycle ownership or background workers.
//
// Example:
//
//	files := storage.New(memorystorage.New())
//	info, err := files.PutBytes(ctx, "report.txt", []byte("report"))
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Size) // 6
type Storage struct {
	// driver stores the objects.
	driver Driver

	// config holds the telemetry settings.
	config settings

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[clientop.Instruments]
}

// New creates a Storage that delegates to driver. Construction policies configure its
// telemetry: every operation (open, put, stat, delete, list, sign) emits one
// client span and the arkoren.storage.operations and
// arkoren.storage.operation.duration metrics when the telemetry is enabled,
// and costs nothing otherwise. Keys, prefixes, cursors and signed URLs are
// never recorded. The duration of open is the time to the response, and of put
// the time to consume the reader; neither includes reading an opened body.
//
// Example:
//
//	files := storage.New(driver, func(store *storage.Builder) {
//		store.Telemetry(app.Telemetry())
//		store.Name("uploads")
//	})
//	_ = files.Driver()
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
func New(driver Driver, configure ...BuilderFunc) *Storage {
	return &Storage{
		driver:      driver,
		config:      newSettings(configure...),
		instruments: telemetry.NewInstruments(clientop.Build(operationNames)),
	}
}

// Driver returns the shared underlying driver. Direct calls bypass facade
// instrumentation and notifications, but retain the driver's validation rules.
//
// Example:
//
//	files := storage.New(memorystorage.New())
//	info, err := files.Driver().Stat(ctx, "report.pdf")
//	_ = info
//	return err
func (storage *Storage) Driver() Driver {
	return storage.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() Driver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver.
// The result is shared, not copied; false means no matching driver was found.
//
// Example:
//
//	files := storage.New(memorystorage.New())
//	if driver, ok := files.DriverAs[*memorystorage.Driver](); ok {
//		return driver.Delete(ctx, "obsolete.pdf")
//	}
func (storage *Storage) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](storage.Driver())
}

// Open returns an object body that the caller must close. Its ObjectInfo describes that body.
// Missing-object error identity is supplied by the driver and preserved unchanged.
// Cancellation precedes key validation. The facade never closes a returned body.
//
// Example:
//
//	object, err := files.Open(ctx, "report.pdf")
//	if err != nil {
//		return err
//	}
//	_, err = io.Copy(destination, object.Contents)
//	return errors.Join(err, object.Contents.Close())
func (storage *Storage) Open(ctx context.Context, key string) (object Object, err error) {
	ctx, op := storage.begin(ctx, operationOpen)
	defer func() {
		end(ctx, op, err)
	}()

	if err := validateObjectKey(ctx, key); err != nil {
		return Object{}, err
	}

	return storage.driver.Open(ctx, key)
}

// Put consumes contents once and replaces key only when the complete object is current.
// On success it publishes [ObjectWritten] when [Builder.Events] is set.
// It does not close contents; the caller retains the reader's lifetime.
//
// Example:
//
//	info, err := files.Put(ctx, "report.txt", strings.NewReader("report"))
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Size) // 6
func (storage *Storage) Put(ctx context.Context, key string, contents io.Reader) (info ObjectInfo, err error) {
	ctx, op := storage.begin(ctx, operationPut)
	defer func() {
		if err == nil {
			op.SetAttrs(telemetry.Int64(semconv.StorageObjectSize, info.Size))
		}

		end(ctx, op, err)

		if err == nil {
			storage.config.Events.Emit(ctx, ObjectWritten, ObjectWrittenEvent{Store: storage.name(), Object: info})
		}
	}()

	if err := validateObjectKey(ctx, key); err != nil {
		return ObjectInfo{}, err
	}

	return storage.driver.Put(ctx, key, contents)
}

// PutBytes replaces key with contents without forcing drivers to receive an allocated object.
// The bytes are consumed synchronously; do not mutate them during the call.
//
// Example:
//
//	info, err := files.PutBytes(ctx, "report.txt", []byte("report"))
//	_ = info
//	return err
func (storage *Storage) PutBytes(ctx context.Context, key string, contents []byte) (ObjectInfo, error) {
	return storage.Put(ctx, key, bytes.NewReader(contents))
}

// Stat returns object metadata. Missing-object error identity is supplied by the driver and
// preserved unchanged.
//
// Example:
//
//	info, err := files.Stat(ctx, "report.pdf")
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Size)
func (storage *Storage) Stat(ctx context.Context, key string) (info ObjectInfo, err error) {
	ctx, op := storage.begin(ctx, operationStat)
	defer func() {
		end(ctx, op, err)
	}()

	if err := validateObjectKey(ctx, key); err != nil {
		return ObjectInfo{}, err
	}

	return storage.driver.Stat(ctx, key)
}

// Delete removes key and succeeds when key is absent. On success it publishes
// [ObjectDeleted] when [Builder.Events] is set, also for an absent key.
// Cancellation and validation failures publish nothing.
//
// Example:
//
//	if err := files.Delete(ctx, "obsolete/report.pdf"); err != nil {
//		return err
//	}
func (storage *Storage) Delete(ctx context.Context, key string) (err error) {
	ctx, op := storage.begin(ctx, operationDelete)
	defer func() {
		end(ctx, op, err)

		if err == nil {
			storage.config.Events.Emit(ctx, ObjectDeleted, ObjectDeletedEvent{Store: storage.name(), Key: key})
		}
	}()

	if err := validateObjectKey(ctx, key); err != nil {
		return err
	}

	return storage.driver.Delete(ctx, key)
}

// List returns a page for options. Cursors are opaque and valid only for the same driver and prefix.
// Objects remain lexically ordered within a page, but concurrent mutations do
// not provide a multi-page snapshot. Cancellation precedes prefix validation.
//
// Example:
//
//	page, err := files.List(ctx, storage.ListOptions{Prefix: "reports/", Limit: 20})
//	if err != nil {
//		return err
//	}
//	for _, info := range page.Objects {
//		fmt.Println(info.Key)
//	}
func (storage *Storage) List(ctx context.Context, options ListOptions) (page Page, err error) {
	ctx, op := storage.begin(ctx, operationList)
	defer func() {
		end(ctx, op, err)
	}()

	if err := ctx.Err(); err != nil {
		return Page{}, err
	}

	if err := ValidatePrefix(options.Prefix); err != nil {
		return Page{}, err
	}

	return storage.driver.List(ctx, options)
}

// SignURL returns the driver's signed URL. An empty options.Method selects GET.
// The facade checks cancellation, key syntax, and a positive TTL before calling
// the driver. The driver checks its maximum TTL before method and binding rules,
// preserving the complete validation order documented on [Driver.SignURL].
// Drivers without signing support return an error wrapping [ErrUnsupported]
// only after validating the request. Signing neither opens nor writes the object.
// Authorize the caller before granting or returning this bearer credential.
//
// Example:
//
//	signed, err := files.SignURL(ctx, "reports/a.pdf", storage.SignOptions{TTL: 15 * time.Minute})
//	if err != nil {
//		return err
//	}
//	http.Redirect(writer, request, signed.String(), http.StatusSeeOther)
func (storage *Storage) SignURL(ctx context.Context, key string, options SignOptions) (signed *url.URL, err error) {
	ctx, op := storage.begin(ctx, operationSign)
	defer func() {
		end(ctx, op, err)
	}()

	if options.Method == "" {
		options.Method = http.MethodGet
	}

	if err := validateObjectKey(ctx, key); err != nil {
		return nil, err
	}

	if options.TTL <= 0 {
		return nil, fmt.Errorf("sign %q: ttl %s must be positive: %w", key, options.TTL, ErrInvalidTTL)
	}

	return storage.driver.SignURL(ctx, key, options)
}

// Sign is [Storage.SignURL] returning the URL as a string.
//
// Example:
//
//	link, err := files.Sign(ctx, "reports/a.pdf", storage.SignOptions{TTL: time.Hour})
//	if err != nil {
//		return err
//	}
//	http.Redirect(writer, request, link, http.StatusSeeOther)
func (storage *Storage) Sign(ctx context.Context, key string, options SignOptions) (string, error) {
	signed, err := storage.SignURL(ctx, key, options)

	if err != nil {
		return "", err
	}

	return signed.String(), nil
}

// maxKeyLength bounds logical key syntax using S3's byte limit. Filesystem path
// and component limits remain backend constraints, not normalization rules.
const maxKeyLength = 1024

// validateObjectKey applies the shared admission order for facade object calls.
// Cancellation wins over malformed keys without reading a body or calling a
// driver; the portable key rules themselves remain in ValidateKey.
func validateObjectKey(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return ValidateKey(key)
}

// ValidateKey checks a relative slash-separated key of at most 1024 UTF-8 bytes.
// It rejects empty/dot segments, control characters, Windows filename metacharacters,
// trailing dots/spaces, and reserved device names case-insensitively. Device
// basenames followed by extensions are also excluded for portability to Windows
// versions where they remain device aliases; Windows 11 permits some such names.
// Names are checked literally, never cleaned, escaped, renamed, or case-folded
// for storage. A rejection wraps [ErrInvalidKey]. Existing rejected keys need an
// application-owned migration through the native backend, not automatic repair.
//
// Example:
//
//	if err := storage.ValidateKey("reports/annual.pdf"); err != nil {
//		return err
//	}
func ValidateKey(key string) error {
	if len(key) > maxKeyLength || !utf8.ValidString(key) {
		return fmt.Errorf("invalid storage key %q: %w", key, ErrInvalidKey)
	}

	if strings.ContainsAny(key, `\:<>"|?*`) || strings.ContainsFunc(key, unicode.IsControl) {
		return fmt.Errorf("invalid storage key %q: %w", key, ErrInvalidKey)
	}

	for segment := range strings.SplitSeq(key, "/") {
		if err := validateKeySegment(segment); err != nil {
			return fmt.Errorf("invalid storage key %q: %w", key, err)
		}
	}

	return nil
}

// validateKeySegment excludes path aliases and device names without changing
// valid segment bytes. Whole-key UTF-8, length, and character checks run first.
func validateKeySegment(segment string) error {
	if segment == "" || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
		return ErrInvalidKey
	}

	if windowsDeviceName(segment) {
		return ErrInvalidKey
	}

	return nil
}

// windowsDeviceName recognizes documented Win32 DOS aliases, including their
// pre-Windows-11 extension forms and the superscript port digits ¹, ², and ³.
// Console handles CONIN$/CONOUT$ are exact names, not arbitrary prefixes. COM0,
// COM10, LPT0, and similar ordinary filenames are not blanket-rejected.
// See https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file
// and Go's Windows filepath implementation for the version-dependent aliases.
func windowsDeviceName(segment string) bool {
	if strings.EqualFold(segment, "CONIN$") || strings.EqualFold(segment, "CONOUT$") {
		return true
	}

	base, _, _ := strings.Cut(segment, ".")
	base = strings.TrimRight(base, " ")

	if slices.ContainsFunc([]string{"CON", "PRN", "AUX", "NUL"}, func(device string) bool {
		return strings.EqualFold(base, device)
	}) {
		return true
	}

	if len(base) < 4 {
		return false
	}

	port := strings.EqualFold(base[:3], "COM") || strings.EqualFold(base[:3], "LPT")
	digit := base[3:]

	return port && utf8.RuneCountInString(digit) == 1 && strings.ContainsAny(digit, "123456789¹²³")
}

// ValidatePrefix checks that prefix is empty or a portable key with one optional final slash.
// It applies the same device-name and character restrictions as [ValidateKey],
// preserving literal prefix bytes instead of cleaning or renaming them.
// These restrictions also apply to native driver prefixes configured through
// this validator; migrate affected backend names deliberately before upgrading.
//
// Example:
//
//	if err := storage.ValidatePrefix("reports/"); err != nil {
//		return err
//	}
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}

	key := strings.TrimSuffix(prefix, "/")

	if key == "" || strings.HasSuffix(key, "/") {
		return fmt.Errorf("invalid storage prefix %q: %w", prefix, ErrInvalidKey)
	}

	if err := ValidateKey(key); err != nil {
		return fmt.Errorf("invalid storage prefix %q: %w", prefix, ErrInvalidKey)
	}

	return nil
}
