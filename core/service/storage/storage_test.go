package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/memorystorage"
)

// driver records storage calls and supplies configured objects, URLs, and errors.
type driver struct {
	// calls counts operations observed by this fixture.
	calls int
	// ctx retains the last context passed to a storage operation.
	ctx context.Context
	// key records the object key passed to the last operation.
	key string
	// contents records the bytes consumed by the last write.
	contents []byte
	// options records the listing options passed to the driver.
	options storage.ListOptions
	// sign records the signing options passed to the driver.
	sign storage.SignOptions
	// signed is the URL returned by the signing stub.
	signed *url.URL
	// object is the object returned by reads and metadata lookups.
	object storage.Object
	// err is the failure returned by the test driver.
	err error
}

// Open records an object-open request.
func (driver *driver) Open(ctx context.Context, key string) (storage.Object, error) {
	driver.calls++
	driver.ctx = ctx
	driver.key = key

	return driver.object, driver.err
}

// Put records an object-upload request.
func (driver *driver) Put(ctx context.Context, key string, contents io.Reader) (storage.ObjectInfo, error) {
	driver.calls++
	driver.ctx = ctx
	driver.key = key

	data, err := io.ReadAll(contents)

	if err != nil {
		return storage.ObjectInfo{}, err
	}

	driver.contents = data

	return storage.ObjectInfo{Key: key, Size: int64(len(data))}, driver.err
}

// Stat records an object-stat request.
func (driver *driver) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	driver.calls++
	driver.ctx = ctx
	driver.key = key

	return storage.ObjectInfo{Key: key}, driver.err
}

// Delete records an object-delete request.
func (driver *driver) Delete(ctx context.Context, key string) error {
	driver.calls++
	driver.ctx = ctx
	driver.key = key

	return driver.err
}

// List records an object-list request.
func (driver *driver) List(ctx context.Context, options storage.ListOptions) (storage.Page, error) {
	driver.calls++
	driver.ctx = ctx
	driver.options = options

	return storage.Page{}, driver.err
}

// SignURL records a signing request and returns the configured URL.
func (driver *driver) SignURL(ctx context.Context, key string, options storage.SignOptions) (*url.URL, error) {
	driver.calls++
	driver.ctx = ctx
	driver.key = key
	driver.sign = options

	if options.Method != http.MethodGet && options.Method != http.MethodPut {
		return nil, storage.ErrUnsupported
	}

	if err := options.ValidateBinding(); err != nil {
		return nil, err
	}

	return driver.signed, driver.err
}

// closer wraps a seekable body and records whether its caller closes it.
type closer struct {
	// closed records whether the caller released the object body.
	closed bool
	// Reader retains seeking support while Close records caller cleanup.
	*bytes.Reader
}

// Close records that the object was closed.
func (closer *closer) Close() error {
	closer.closed = true

	return nil
}

// TestFacadeDelegatesOperations verifies facade operation delegation.
// contextKey keys test context values.
type contextKey struct{}

// TestFacadeDelegatesOperations checks arguments and returned objects pass through unchanged.
func TestFacadeDelegatesOperations(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), contextKey{}, "value")
	driver := &driver{}
	store := storage.New(driver)

	if store.Driver() != driver {
		t.Fatal("Driver() did not return supplied driver")
	}

	if _, err := store.Put(ctx, "reports/a.pdf", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatal(err)
	}

	if driver.ctx != ctx || driver.key != "reports/a.pdf" || string(driver.contents) != "data" {
		t.Fatalf("Put delegation = %#v", driver)
	}

	if _, err := store.Stat(ctx, "reports/a.pdf"); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, "reports/a.pdf"); err != nil {
		t.Fatal(err)
	}

	options := storage.ListOptions{Prefix: "reports/", Cursor: "opaque", Limit: 2}

	if _, err := store.List(ctx, options); err != nil {
		t.Fatal(err)
	}

	if driver.options != options {
		t.Fatalf("List options = %#v, want %#v", driver.options, options)
	}
}

// TestPutBytesPreservesBinaryData verifies binary data is retained.
func TestPutBytesPreservesBinaryData(t *testing.T) {
	t.Parallel()

	driver := &driver{}

	_, err := storage.New(driver).PutBytes(context.Background(), "binary", []byte{0x00, 0xff, 'a'})

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(driver.contents, []byte{0x00, 0xff, 'a'}) {
		t.Fatalf("contents = %x", driver.contents)
	}
}

// TestFacadeRejectsInvalidKeys verifies invalid keys are rejected before delegation.
func TestFacadeRejectsInvalidKeys(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"", "/root", "a//b", "a/./b", "a/../b", "a\\b", "a\x00b", string([]byte{0xff})} {
		driver := &driver{}

		_, err := storage.New(driver).Stat(context.Background(), key)

		if !errors.Is(err, storage.ErrInvalidKey) || driver.calls != 0 {
			t.Fatalf("Stat(%q) err = %v, calls = %d", key, err, driver.calls)
		}
	}
}

// TestFacadeRejectsInvalidPrefixes verifies invalid prefixes are rejected before delegation.
func TestFacadeRejectsInvalidPrefixes(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{"/root", "a//", "a/./", "a/../", "a\\", "a\x00", string([]byte{0xff})} {
		driver := &driver{}

		_, err := storage.New(driver).List(context.Background(), storage.ListOptions{Prefix: prefix})

		if !errors.Is(err, storage.ErrInvalidKey) || driver.calls != 0 {
			t.Fatalf("List(%q) err = %v, calls = %d", prefix, err, driver.calls)
		}
	}
}

// TestOpenLeavesCloseToCaller verifies callers own object body closure.
func TestOpenLeavesCloseToCaller(t *testing.T) {
	t.Parallel()

	contents := &closer{Reader: bytes.NewReader([]byte("data"))}
	store := storage.New(&driver{object: storage.Object{Contents: contents}})

	object, err := store.Open(context.Background(), "data")

	if err != nil {
		t.Fatal(err)
	}

	if err := object.Contents.Close(); err != nil {
		t.Fatal(err)
	}

	if !contents.closed {
		t.Fatal("Open closed the object body before returning it")
	}
}

// TestNotFoundPassesThrough verifies missing-object errors are preserved.
func TestNotFoundPassesThrough(t *testing.T) {
	t.Parallel()

	driver := &driver{err: storage.ErrNotFound}
	store := storage.New(driver)

	if _, err := store.Open(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open error = %v", err)
	}

	if _, err := store.Stat(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat error = %v", err)
	}
}

// TestFacadeDoesNotClassifyProviderErrors verifies the facade preserves unknown
// provider errors without classifying them as missing objects.
func TestFacadeDoesNotClassifyProviderErrors(t *testing.T) {
	t.Parallel()

	providerError := errors.New("provider reports object missing")
	store := storage.New(&driver{err: providerError})

	if _, err := store.Open(context.Background(), "missing"); !errors.Is(err, providerError) || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open error = %v", err)
	}

	if _, err := store.Stat(context.Background(), "missing"); !errors.Is(err, providerError) || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat error = %v", err)
	}
}

// TestValidateKeyRejectsNonPortableKeys checks path syntax forbidden by the portable contract.
func TestValidateKeyRejectsNonPortableKeys(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"a/.. ", "a/...", "C:/x", "file:stream", "line\nbreak", "a/b."} {
		if err := storage.ValidateKey(key); err == nil {
			t.Fatalf("key %q accepted", key)
		}
	}
}

// checkFacadeSignRejects fails the test unless signing key with options is
// rejected with want before the driver is called.
func checkFacadeSignRejects(t *testing.T, key string, options storage.SignOptions, want error) {
	t.Helper()

	driver := &driver{}

	_, err := storage.New(driver).SignURL(context.Background(), key, options)

	if !errors.Is(err, want) || driver.calls != 0 {
		t.Fatalf("SignURL err = %v, calls = %d, want %v without calls", err, driver.calls, want)
	}
}

// TestFacadeSignRejectsKey verifies signing validates the key, TTL and method
// in order before delegation.
func TestFacadeSignRejectsKey(t *testing.T) {
	t.Parallel()

	checkFacadeSignRejects(t, "a//b", storage.SignOptions{TTL: time.Minute}, storage.ErrInvalidKey)
}

// TestFacadeSignRejectsKeyBeforeTtl verifies signing validates the key, TTL and method
// in order before delegation.
func TestFacadeSignRejectsKeyBeforeTtl(t *testing.T) {
	t.Parallel()

	checkFacadeSignRejects(t, "", storage.SignOptions{}, storage.ErrInvalidKey)
}

// TestFacadeSignRejectsZeroTtl verifies signing validates the key, TTL and method
// in order before delegation.
func TestFacadeSignRejectsZeroTtl(t *testing.T) {
	t.Parallel()

	checkFacadeSignRejects(t, "a", storage.SignOptions{}, storage.ErrInvalidTTL)
}

// TestFacadeSignRejectsNegativeTtl verifies signing validates the key, TTL and method
// in order before delegation.
func TestFacadeSignRejectsNegativeTtl(t *testing.T) {
	t.Parallel()

	checkFacadeSignRejects(t, "a", storage.SignOptions{TTL: -1}, storage.ErrInvalidTTL)
}

// TestFacadeSignRejectsTtlBeforeMethod verifies signing validates the key, TTL and method
// in order before delegation.
func TestFacadeSignRejectsTtlBeforeMethod(t *testing.T) {
	t.Parallel()

	checkFacadeSignRejects(t, "a", storage.SignOptions{Method: http.MethodDelete}, storage.ErrInvalidTTL)
}

// TestFacadeSignRejectsMethod verifies the facade preserves the driver's method
// rejection after the driver's backend-specific TTL validation boundary.
func TestFacadeSignRejectsMethod(t *testing.T) {
	t.Parallel()

	_, err := storage.New(&driver{}).SignURL(t.Context(), "a", storage.SignOptions{
		Method: http.MethodDelete, TTL: time.Minute,
	})

	if !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("SignURL = %v, want ErrUnsupported", err)
	}
}

// TestFacadeSignRejectsBindingOnGet verifies a content type on a GET URL is rejected
// by the driver after its backend-specific TTL validation.
func TestFacadeSignRejectsBindingOnGet(t *testing.T) {
	t.Parallel()

	_, err := storage.New(&driver{}).SignURL(t.Context(), "a", storage.SignOptions{
		TTL: time.Minute, ContentType: "image/png",
	})

	if !errors.Is(err, storage.ErrInvalidSignOptions) {
		t.Fatalf("SignURL = %v, want ErrInvalidSignOptions", err)
	}
}

// TestFacadeSignDelegates verifies method defaulting, delegation, error pass-through and the
// string form of Sign.
func TestFacadeSignDelegates(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), contextKey{}, "value")
	signed := &url.URL{Scheme: "https", Host: "example.com", Path: "/a.pdf", RawQuery: "sig=1"}
	driver := &driver{signed: signed}
	store := storage.New(driver)

	got, err := store.SignURL(ctx, "reports/a.pdf", storage.SignOptions{TTL: time.Minute})

	if err != nil {
		t.Fatal(err)
	}

	want := storage.SignOptions{Method: http.MethodGet, TTL: time.Minute}

	if got != signed || driver.ctx != ctx || driver.key != "reports/a.pdf" || driver.sign != want {
		t.Fatalf("SignURL delegation = %#v, url %v", driver, got)
	}

	put := storage.SignOptions{Method: http.MethodPut, TTL: time.Hour}

	text, err := store.Sign(ctx, "reports/a.pdf", put)

	if err != nil {
		t.Fatal(err)
	}

	if text != signed.String() || driver.sign != put {
		t.Fatalf("Sign = %q with options %#v", text, driver.sign)
	}

	driver.err = storage.ErrUnsupported

	if _, err := store.Sign(ctx, "reports/a.pdf", put); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("Sign driver error = %v", err)
	}
}

// TestFacadeCancellationPrecedesInvalidInput keeps cancellation authoritative at
// every facade entry, even when the request also contains malformed input.
func TestFacadeCancellationPrecedesInvalidInput(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	store := storage.New(&driver{})
	operations := []func() error{
		func() error {
			_, err := store.Open(ctx, "../invalid")

			return err
		},
		func() error {
			_, err := store.Put(ctx, "../invalid", strings.NewReader("data"))

			return err
		},
		func() error {
			_, err := store.Stat(ctx, "../invalid")

			return err
		},
		func() error {
			return store.Delete(ctx, "../invalid")
		},
		func() error {
			_, err := store.List(ctx, storage.ListOptions{Prefix: "../invalid"})

			return err
		},
		func() error {
			_, err := store.SignURL(ctx, "../invalid", storage.SignOptions{})

			return err
		},
	}

	for _, operation := range operations {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled operation = %v, want context.Canceled", err)
		}
	}
}

// limitedSigner supplies a deterministic backend TTL limit without a network.
// Remaining validation comes from the memory driver's full signing contract.
type limitedSigner struct {
	// Driver supplies storage operations and unsupported-signing behavior.
	*memorystorage.Driver
}

// SignURL checks the backend maximum before method and binding restrictions,
// after honoring the contract's context and key precedence.
func (signer limitedSigner) SignURL(ctx context.Context, key string, options storage.SignOptions) (*url.URL, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}

	if options.TTL > time.Hour {
		return nil, storage.ErrInvalidTTL
	}

	return signer.Driver.SignURL(ctx, key, options)
}

// TestFacadeBackendTTLPrecedesMethod preserves the Driver's validation order
// when both a backend-specific limit and a portable method rule are violated.
func TestFacadeBackendTTLPrecedesMethod(t *testing.T) {
	t.Parallel()

	store := storage.New(limitedSigner{Driver: memorystorage.New()})

	_, err := store.SignURL(t.Context(), "report.pdf", storage.SignOptions{
		TTL:    2 * time.Hour,
		Method: http.MethodDelete,
	})

	if !errors.Is(err, storage.ErrInvalidTTL) {
		t.Fatalf("SignURL = %v, want backend ErrInvalidTTL before ErrUnsupported", err)
	}
}

// TestFacadeRejectsWindowsDeviceKeys prevents logical objects from becoming
// device accesses on supported Windows filesystems, including nested segments.
func TestFacadeRejectsWindowsDeviceKeys(t *testing.T) {
	t.Parallel()

	store := storage.New(memorystorage.New())

	for _, key := range []string{"NUL", "reports/con", "AUX/report.txt", "com1", "LPT9", "COM¹", "lpt²", "CONIN$", "conout$"} {
		_, err := store.PutBytes(t.Context(), key, []byte("report"))

		if !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("PutBytes(%q) = %v, want ErrInvalidKey", key, err)
		}
	}
}

// TestFacadeBackendTTLPrecedesBinding keeps a backend limit ahead of otherwise
// invalid GET upload bindings, matching the raw Driver's documented precedence.
func TestFacadeBackendTTLPrecedesBinding(t *testing.T) {
	t.Parallel()

	store := storage.New(limitedSigner{Driver: memorystorage.New()})

	_, err := store.SignURL(t.Context(), "report.pdf", storage.SignOptions{
		TTL:         2 * time.Hour,
		ContentType: "image/png",
	})

	if !errors.Is(err, storage.ErrInvalidTTL) {
		t.Fatalf("SignURL = %v, want backend ErrInvalidTTL before binding error", err)
	}
}

// ExampleStorage_Open executes the README's owned-body recipe entirely in
// memory, checking both read and close outcomes before using the bytes.
func ExampleStorage_Open() {
	ctx := context.Background()
	files := storage.New(memorystorage.New())

	if _, err := files.PutBytes(ctx, "reports/a.txt", []byte("report")); err != nil {
		fmt.Println(err)

		return
	}

	object, err := files.Open(ctx, "reports/a.txt")

	if err != nil {
		fmt.Println(err)

		return
	}

	contents, err := io.ReadAll(object.Contents)

	err = errors.Join(err, object.Contents.Close())

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(string(contents))
	// Output: report
}

// ExampleStorage_SignURL demonstrates unsupported signing without changing
// capability behavior or needing a provider credential.
func ExampleStorage_SignURL() {
	files := storage.New(memorystorage.New())

	_, err := files.SignURL(context.Background(), "report.pdf", storage.SignOptions{TTL: time.Minute})
	fmt.Println(errors.Is(err, storage.ErrUnsupported))
	// Output: true
}

// ExampleValidateKey shows literal key validation without performing object I/O.
func ExampleValidateKey() {
	fmt.Println(storage.ValidateKey("reports/a.pdf") == nil)
	fmt.Println(errors.Is(storage.ValidateKey("NUL"), storage.ErrInvalidKey))
	// Output:
	// true
	// true
}

// ExampleSignOptions_ValidateBinding checks bindings independently of backend
// limits; it does not claim that a successful check validates the whole request.
func ExampleSignOptions_ValidateBinding() {
	options := storage.SignOptions{Method: http.MethodPut, ContentLength: 1024}
	fmt.Println(options.ValidateBinding() == nil)

	options.Method = http.MethodGet
	fmt.Println(errors.Is(options.ValidateBinding(), storage.ErrInvalidSignOptions))
	// Output:
	// true
	// true
}
