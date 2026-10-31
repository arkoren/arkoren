package storagetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/storage"
)

// config holds the settings produced by [Option] values.
type config struct {
	// withoutSignURL is set by [WithoutSignURL].
	withoutSignURL bool
}

// Option declares a capability of the driver under test. Options describe
// what the driver supports, never divergent behavior: every storage driver
// must implement the rest of the contract.
//
// Example:
//
//	options := []storagetest.Option{storagetest.WithoutSignURL()}
//	storagetest.Run(t, newDriver, options...)
type Option func(*config)

// WithoutSignURL declares that the driver cannot sign URLs. The suite then
// checks that SignURL returns [storage.ErrUnsupported] for valid input, and
// still returns [storage.ErrInvalidKey] and [storage.ErrInvalidTTL] for
// invalid ones, instead of running the subtests that fetch signed URLs.
//
// Example:
//
//	storagetest.Run(t, func(t *testing.T) storage.Driver {
//		return memorystorage.New()
//	}, storagetest.WithoutSignURL())
func WithoutSignURL() Option {
	return func(settings *config) {
		settings.withoutSignURL = true
	}
}

// suite carries the state shared by the subtests of one [Run] call.
type suite struct {
	// newDriver creates the driver under test.
	newDriver func(t *testing.T) storage.Driver

	// withoutSignURL reports that the driver does not support SignURL.
	withoutSignURL bool
}

// Run executes the storage conformance suite against drivers produced by
// newDriver. The factory is called once per subtest. Run marks every subtest
// parallel and gives each its own key prefix, so a shared backend works.
// Its portable-key checks include Windows device/metacharacter restrictions;
// cancellation checks also combine an ended context with malformed input.
// The factory owns native lifecycle cleanup through t.Cleanup.
//
// Example:
//
//	func TestStorage(t *testing.T) {
//		t.Parallel()
//		storagetest.Run(t, func(t *testing.T) storage.Driver {
//			return memorystorage.New()
//		}, storagetest.WithoutSignURL())
//	}
func Run(t *testing.T, newDriver func(t *testing.T) storage.Driver, opts ...Option) {
	t.Helper()

	var settings config

	for _, opt := range opts {
		opt(&settings)
	}

	s := suite{newDriver: newDriver, withoutSignURL: settings.withoutSignURL}

	for _, group := range []struct {
		// name labels the conformance group in test output.
		name string
		// fn runs the group with its shared suite configuration.
		fn func(*testing.T, suite)
	}{
		{"Objects", objects},
		{"Delete", deletion},
		{"Keys", keys},
		{"List", listing},
		{"SignURL", signing},
		{"Cancellation", cancellation},
		{"Concurrency", concurrency},
	} {
		t.Run(group.name, func(t *testing.T) {
			group.fn(t, s)
		})
	}
}

// env is what one subtest receives: a driver and a private key prefix.
type env struct {
	// driver is the driver under test.
	driver storage.Driver

	// prefix is unique to the subtest and ends with a slash.
	prefix string
}

// key returns name below the subtest's prefix.
func (env env) key(name string) string {
	return env.prefix + name
}

// sub runs fn as a parallel subtest with a fresh driver and a unique prefix,
// and deletes every object under the prefix afterwards.
func (suite suite) sub(t *testing.T, name string, fn func(t *testing.T, e env)) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		t.Parallel()

		e := env{
			driver: suite.newDriver(t),
			prefix: "conformance-" + strings.ToLower(rand.Text()) + "/",
		}
		t.Cleanup(func() {
			cleanup(t, e)
		})

		fn(t, e)
	})
}

// cleanup deletes every object under the subtest prefix.
func cleanup(t *testing.T, e env) {
	ctx := context.WithoutCancel(t.Context())
	cursor := ""

	for {
		page, err := e.driver.List(ctx, storage.ListOptions{Prefix: e.prefix, Cursor: cursor})

		if err != nil {
			t.Errorf("cleanup List: %v", err)

			return
		}

		for _, object := range page.Objects {
			if err := e.driver.Delete(ctx, object.Key); err != nil {
				t.Errorf("cleanup Delete(%q): %v", object.Key, err)
			}
		}

		if page.NextCursor == "" {
			return
		}

		cursor = page.NextCursor
	}
}

// must fails the test immediately when err is not nil.
func must(t *testing.T, op string, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
}

// put stores contents under key and returns the reported metadata.
func put(t *testing.T, e env, key string, contents string) storage.ObjectInfo {
	t.Helper()

	info, err := e.driver.Put(t.Context(), key, strings.NewReader(contents))

	must(t, "Put("+key+")", err)

	return info
}

// read opens key, reads its body to the end and closes it.
func read(t *testing.T, e env, key string) (storage.Object, string) {
	t.Helper()

	object, err := e.driver.Open(t.Context(), key)

	must(t, "Open("+key+")", err)
	defer object.Contents.Close()

	body, err := io.ReadAll(object.Contents)

	must(t, "read body", err)

	return object, string(body)
}

// wantBody asserts that key holds want.
func wantBody(t *testing.T, e env, key, want string) {
	t.Helper()

	if _, got := read(t, e, key); got != want {
		t.Errorf("body of %q = %q, want %q", key, truncate(got), truncate(want))
	}
}

// truncate shortens long values for failure messages.
func truncate(s string) string {
	const limit = 40

	if len(s) <= limit {
		return s
	}

	return s[:limit] + "..."
}

// wantNotFound asserts that Open and Stat report key as missing.
func wantNotFound(t *testing.T, e env, key string) {
	t.Helper()

	if _, err := e.driver.Open(t.Context(), key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Open(%q) error = %v, want ErrNotFound", key, err)
	}

	if _, err := e.driver.Stat(t.Context(), key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Stat(%q) error = %v, want ErrNotFound", key, err)
	}
}

// wantInfo asserts the key and size of reported metadata.
func wantInfo(t *testing.T, op string, info storage.ObjectInfo, key string, size int) {
	t.Helper()

	if info.Key != key || info.Size != int64(size) {
		t.Errorf("%s = {Key: %q, Size: %d}, want {Key: %q, Size: %d}", op, info.Key, info.Size, key, size)
	}

	if skew := time.Since(info.ModifiedAt); skew > time.Hour || skew < -time.Hour {
		t.Errorf("%s ModifiedAt = %s, want about now", op, info.ModifiedAt)
	}
}

// objects covers storing and reading objects.
func objects(t *testing.T, s suite) {
	s.sub(t, "PutThenOpen", func(t *testing.T, e env) {
		key := e.key("a.txt")
		info := put(t, e, key, "hello")

		wantInfo(t, "Put", info, key, 5)

		object, body := read(t, e, key)

		wantInfo(t, "Open", object.ObjectInfo, key, 5)

		if body != "hello" {
			t.Errorf("body = %q, want %q", body, "hello")
		}
	})
	s.sub(t, "StatMatchesPut", func(t *testing.T, e env) {
		key := e.key("a.txt")
		put(t, e, key, "hello")

		info, err := e.driver.Stat(t.Context(), key)

		must(t, "Stat", err)
		wantInfo(t, "Stat", info, key, 5)
	})
	s.sub(t, "EmptyObject", func(t *testing.T, e env) {
		key := e.key("empty")

		wantInfo(t, "Put", put(t, e, key, ""), key, 0)
		wantBody(t, e, key, "")
	})
	s.sub(t, "BinaryAndLargeContents", func(t *testing.T, e env) {
		want := bytes.Repeat([]byte{0, 1, 0xff, '\n', 0x80}, 300_000)
		key := e.key("big.bin")

		_, err := e.driver.Put(t.Context(), key, bytes.NewReader(want))

		must(t, "Put", err)

		_, got := read(t, e, key)

		if got != string(want) {
			t.Errorf("round trip of %d bytes returned %d different bytes", len(want), len(got))
		}
	})
	s.sub(t, "PutReplacesObject", func(t *testing.T, e env) {
		key := e.key("a.txt")
		first := put(t, e, key, "first version")
		second := put(t, e, key, "v2")

		wantInfo(t, "second Put", second, key, 2)
		wantBody(t, e, key, "v2")

		if second.ModifiedAt.Before(first.ModifiedAt) {
			t.Errorf("ModifiedAt went backwards: %s then %s", first.ModifiedAt, second.ModifiedAt)
		}
	})
	s.sub(t, "NestedKeys", func(t *testing.T, e env) {
		for _, name := range []string{"x/y/z.txt", "x/y.txt", "x.txt"} {
			put(t, e, e.key(name), name)
		}

		wantBody(t, e, e.key("x/y/z.txt"), "x/y/z.txt")
		wantBody(t, e, e.key("x/y.txt"), "x/y.txt")
		wantBody(t, e, e.key("x.txt"), "x.txt")
	})
	s.sub(t, "PortableKeyCharacters", func(t *testing.T, e env) {
		for _, name := range []string{
			"with space.txt", "plus+sign", "percent%20", "unicode-é世", "a=b&c#e",
			"COM0", "COM10", "LPT0", "LPT10", "COM¹0", "LPT⁴", "console.txt", ".hidden",
		} {
			key := e.key(name)
			put(t, e, key, name)

			wantBody(t, e, key, name)
		}
	})
	s.sub(t, "MissingObjectIsNotFound", func(t *testing.T, e env) {
		wantNotFound(t, e, e.key("missing"))
	})
	s.sub(t, "PrefixOfObjectIsNotAnObject", func(t *testing.T, e env) {
		put(t, e, e.key("dir/file"), "x")

		wantNotFound(t, e, e.key("dir"))
	})
	s.sub(t, "FailedPutKeepsPreviousObject", func(t *testing.T, e env) {
		key := e.key("a.txt")
		put(t, e, key, "old")

		failing := io.MultiReader(strings.NewReader("partial"), errReader{})

		if _, err := e.driver.Put(t.Context(), key, failing); err == nil {
			t.Fatal("Put with failing reader succeeded, want error")
		}

		wantBody(t, e, key, "old")
	})
	s.sub(t, "FailedPutCreatesNothing", func(t *testing.T, e env) {
		key := e.key("new.txt")
		failing := io.MultiReader(strings.NewReader("partial"), errReader{})

		if _, err := e.driver.Put(t.Context(), key, failing); err == nil {
			t.Fatal("Put with failing reader succeeded, want error")
		}

		wantNotFound(t, e, key)
	})
}

// errReader fails every read.
type errReader struct{}

// Read always returns an error.
func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("storagetest: reader failure")
}

// deletion covers Delete.
func deletion(t *testing.T, s suite) {
	s.sub(t, "RemovesObject", func(t *testing.T, e env) {
		key := e.key("a.txt")
		put(t, e, key, "x")

		must(t, "Delete", e.driver.Delete(t.Context(), key))
		wantNotFound(t, e, key)
	})
	s.sub(t, "MissingObjectSucceeds", func(t *testing.T, e env) {
		must(t, "Delete", e.driver.Delete(t.Context(), e.key("missing")))
	})
	s.sub(t, "LeavesOtherObjects", func(t *testing.T, e env) {
		put(t, e, e.key("a"), "a")
		put(t, e, e.key("b"), "b")

		must(t, "Delete", e.driver.Delete(t.Context(), e.key("a")))
		wantBody(t, e, e.key("b"), "b")
	})
	s.sub(t, "PrefixOfObjectSucceedsAndKeepsObject", func(t *testing.T, e env) {
		put(t, e, e.key("dir/file"), "x")

		must(t, "Delete", e.driver.Delete(t.Context(), e.key("dir")))
		wantBody(t, e, e.key("dir/file"), "x")
	})
}

// invalidKeys cover path aliases, Win32 filename syntax, and documented device
// names, including aliases with extensions on older supported Windows versions.
var invalidKeys = []string{
	"", "/abs", "a//b", "a/../b", "a/./b", "a\\b", "trailing/", "a:b", "bad\x00key", "dot.",
	`a<b`, `a>b`, `a"b`, "a|b", "a?b", "a*b",
	"NUL", "nested/con", "AUX/file", "PRN.txt", "NUL.tar.gz", "NUL .txt",
	"COM1", "lpt9.txt", "COM¹", "LPT²", "lpt³.log", "CONIN$", "conout$",
}

// keys covers rejection of keys outside the portable syntax.
func keys(t *testing.T, s suite) {
	s.sub(t, "InvalidKeysAreRejected", func(t *testing.T, e env) {
		ctx := t.Context()

		for _, key := range invalidKeys {
			if _, err := e.driver.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Put(%q) error = %v, want ErrInvalidKey", key, err)
			}

			if _, err := e.driver.Open(ctx, key); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Open(%q) error = %v, want ErrInvalidKey", key, err)
			}

			if _, err := e.driver.Stat(ctx, key); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Stat(%q) error = %v, want ErrInvalidKey", key, err)
			}

			if err := e.driver.Delete(ctx, key); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Delete(%q) error = %v, want ErrInvalidKey", key, err)
			}
		}
	})
	s.sub(t, "InvalidPrefixIsRejected", func(t *testing.T, e env) {
		for _, prefix := range []string{"/abs", "a//", "a/../b", "a\\b", "reports?/", "nested/CON/"} {
			_, err := e.driver.List(t.Context(), storage.ListOptions{Prefix: prefix})

			if !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("List(prefix %q) error = %v, want ErrInvalidKey", prefix, err)
			}
		}
	})
}

// listKeys returns the keys of a page.
func listKeys(page storage.Page) []string {
	keys := make([]string, 0, len(page.Objects))

	for _, object := range page.Objects {
		keys = append(keys, object.Key)
	}

	return keys
}

// putAll stores one object per name under the subtest prefix.
func putAll(t *testing.T, e env, names ...string) {
	t.Helper()

	for _, name := range names {
		put(t, e, e.key(name), name)
	}
}

// walk follows cursors from first until the listing ends and returns every
// page's keys.
func walk(t *testing.T, e env, options storage.ListOptions) [][]string {
	t.Helper()

	var pages [][]string

	for range 100 {
		page, err := e.driver.List(t.Context(), options)

		must(t, "List", err)

		pages = append(pages, listKeys(page))

		if page.NextCursor == "" {
			return pages
		}

		options.Cursor = page.NextCursor
	}

	t.Fatal("List did not terminate after 100 pages")

	return nil
}

// listing covers List.
func listing(t *testing.T, s suite) {
	s.sub(t, "EmptyPrefixListing", func(t *testing.T, e env) {
		page, err := e.driver.List(t.Context(), storage.ListOptions{Limit: 1})

		must(t, "List", err)

		if len(page.Objects) > 1 {
			t.Errorf("List(limit 1) returned %d objects", len(page.Objects))
		}
	})
	s.sub(t, "NoMatchIsEmptyPage", func(t *testing.T, e env) {
		page, err := e.driver.List(t.Context(), storage.ListOptions{Prefix: e.prefix})

		must(t, "List", err)

		if len(page.Objects) != 0 || page.NextCursor != "" {
			t.Errorf("page = %+v, want empty", page)
		}
	})
	s.sub(t, "ReturnsFullKeysInLexicalOrder", func(t *testing.T, e env) {
		putAll(t, e, "b", "a/2", "a/1", "c")

		page, err := e.driver.List(t.Context(), storage.ListOptions{Prefix: e.prefix})

		must(t, "List", err)

		want := []string{e.key("a/1"), e.key("a/2"), e.key("b"), e.key("c")}

		if got := listKeys(page); !slices.Equal(got, want) {
			t.Errorf("keys = %v, want %v", got, want)
		}

		if page.NextCursor != "" {
			t.Errorf("NextCursor = %q, want empty", page.NextCursor)
		}
	})
	s.sub(t, "ObjectInfoDescribesObjects", func(t *testing.T, e env) {
		putAll(t, e, "a")

		page, err := e.driver.List(t.Context(), storage.ListOptions{Prefix: e.prefix})

		must(t, "List", err)

		if len(page.Objects) != 1 {
			t.Fatalf("List returned %d objects, want 1", len(page.Objects))
		}

		wantInfo(t, "List", page.Objects[0], e.key("a"), 1)
	})
	s.sub(t, "PrefixMatchesByStringPrefix", func(t *testing.T, e env) {
		putAll(t, e, "reports/a", "reports/b", "reportsX", "other/c")

		cases := map[string][]string{
			e.key("reports/"): {"reports/a", "reports/b"},
			e.key("reports"):  {"reports/a", "reports/b", "reportsX"},
			e.key("other/c"):  {"other/c"},
			e.key("none"):     {},
		}

		for prefix, names := range cases {
			page, err := e.driver.List(t.Context(), storage.ListOptions{Prefix: prefix})

			must(t, "List", err)

			want := make([]string, 0, len(names))

			for _, name := range names {
				want = append(want, e.key(name))
			}

			if got := listKeys(page); !slices.Equal(got, want) {
				t.Errorf("List(prefix %q) = %v, want %v", prefix, got, want)
			}
		}
	})
	s.sub(t, "PaginatesWithCursors", func(t *testing.T, e env) {
		putAll(t, e, "1", "2", "3", "4", "5")

		pages := walk(t, e, storage.ListOptions{Prefix: e.prefix, Limit: 2})

		want := [][]string{{e.key("1"), e.key("2")}, {e.key("3"), e.key("4")}, {e.key("5")}}

		if !slices.EqualFunc(pages, want, slices.Equal) {
			t.Errorf("pages = %v, want %v", pages, want)
		}
	})
	s.sub(t, "FinalFullPageHasNoCursor", func(t *testing.T, e env) {
		putAll(t, e, "1", "2", "3", "4")

		pages := walk(t, e, storage.ListOptions{Prefix: e.prefix, Limit: 2})

		if len(pages) != 2 {
			t.Errorf("got %d pages, want 2: %v", len(pages), pages)
		}
	})
	s.sub(t, "NonPositiveLimitUsesDefault", func(t *testing.T, e env) {
		putAll(t, e, "1", "2", "3")

		for _, limit := range []int{0, -1} {
			page, err := e.driver.List(t.Context(), storage.ListOptions{Prefix: e.prefix, Limit: limit})

			must(t, "List", err)

			if len(page.Objects) != 3 {
				t.Errorf("List(limit %d) returned %d objects, want 3", limit, len(page.Objects))
			}
		}
	})
	s.sub(t, "DefaultLimitPaginatesLargeListings", func(t *testing.T, e env) {
		const total = 1100
		var wg sync.WaitGroup
		sem := make(chan struct{}, 16)

		for i := range total {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() {
					<-sem
				}()
				name := e.key(fmt.Sprintf("n%04d", i))

				if _, err := e.driver.Put(t.Context(), name, strings.NewReader("x")); err != nil {
					t.Errorf("Put(%q): %v", name, err)
				}
			})
		}

		wg.Wait()

		seen := 0

		for _, page := range walk(t, e, storage.ListOptions{Prefix: e.prefix}) {
			seen += len(page)
		}

		if seen != total {
			t.Errorf("listing returned %d objects across pages, want %d", seen, total)
		}
	})
	s.sub(t, "ListedObjectsReflectDelete", func(t *testing.T, e env) {
		putAll(t, e, "a", "b")

		must(t, "Delete", e.driver.Delete(t.Context(), e.key("a")))

		page, err := e.driver.List(t.Context(), storage.ListOptions{Prefix: e.prefix})

		must(t, "List", err)

		if got, want := listKeys(page), []string{e.key("b")}; !slices.Equal(got, want) {
			t.Errorf("keys = %v, want %v", got, want)
		}
	})
}

// signedRequest sends body (nil for none) to the signed URL with method and
// returns the response status and body.
func signedRequest(t *testing.T, method string, link string, body string) (int, string) {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), method, link, strings.NewReader(body))

	must(t, method+" request", err)

	response, err := http.DefaultClient.Do(request)

	must(t, method+" "+link, err)
	defer response.Body.Close()

	got, err := io.ReadAll(response.Body)

	must(t, "read response", err)

	return response.StatusCode, string(got)
}

// signing covers SignURL.
func signing(t *testing.T, s suite) {
	s.sub(t, "InvalidInputKeepsItsOwnError", func(t *testing.T, e env) {
		ctx := t.Context()
		valid := storage.SignOptions{TTL: time.Minute}

		if _, err := e.driver.SignURL(ctx, "", valid); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("SignURL(empty key) error = %v, want ErrInvalidKey", err)
		}

		for _, ttl := range []time.Duration{0, -1} {
			_, err := e.driver.SignURL(ctx, e.key("a"), storage.SignOptions{TTL: ttl})

			if !errors.Is(err, storage.ErrInvalidTTL) {
				t.Errorf("SignURL(ttl %d) error = %v, want ErrInvalidTTL", ttl, err)
			}
		}

		_, err := e.driver.SignURL(ctx, e.key("a"), storage.SignOptions{
			Method: http.MethodDelete, TTL: time.Minute,
		})

		if !errors.Is(err, storage.ErrUnsupported) {
			t.Errorf("SignURL(DELETE) error = %v, want ErrUnsupported", err)
		}

		for name, options := range map[string]storage.SignOptions{
			"GET with ContentType":   {TTL: time.Minute, ContentType: "image/png"},
			"GET with ContentLength": {Method: http.MethodGet, TTL: time.Minute, ContentLength: 1},
			"PUT negative length":    {Method: http.MethodPut, TTL: time.Minute, ContentLength: -1},
		} {
			if _, err := e.driver.SignURL(ctx, e.key("a"), options); !errors.Is(err, storage.ErrInvalidSignOptions) {
				t.Errorf("SignURL(%s) error = %v, want ErrInvalidSignOptions", name, err)
			}
		}
	})

	if s.withoutSignURL {
		s.sub(t, "ValidInputIsUnsupported", func(t *testing.T, e env) {
			_, err := e.driver.SignURL(t.Context(), e.key("a"), storage.SignOptions{TTL: time.Minute})

			if !errors.Is(err, storage.ErrUnsupported) {
				t.Errorf("SignURL error = %v, want ErrUnsupported", err)
			}
		})

		return
	}

	s.sub(t, "SignedPutReplacesObject", func(t *testing.T, e env) {
		key := e.key("upload.txt")
		put(t, e, key, "old")

		link, err := e.driver.SignURL(t.Context(), key, storage.SignOptions{
			Method: http.MethodPut, TTL: time.Minute,
		})

		must(t, "SignURL", err)

		if status, body := signedRequest(t, http.MethodPut, link.String(), "uploaded"); status/100 != 2 {
			t.Fatalf("signed PUT status = %d, body %q", status, truncate(body))
		}

		wantBody(t, e, key, "uploaded")
	})
	s.sub(t, "SignedGetReturnsBody", func(t *testing.T, e env) {
		key := e.key("download.txt")
		put(t, e, key, "contents")

		for _, method := range []string{"", http.MethodGet} {
			link, err := e.driver.SignURL(t.Context(), key, storage.SignOptions{Method: method, TTL: time.Minute})

			must(t, "SignURL("+method+")", err)

			if status, body := signedRequest(t, http.MethodGet, link.String(), ""); status != http.StatusOK || body != "contents" {
				t.Errorf("signed GET (method %q) = %d %q, want 200 %q", method, status, truncate(body), "contents")
			}
		}
	})
	s.sub(t, "SignedURLExpires", func(t *testing.T, e env) {
		key := e.key("short.txt")
		put(t, e, key, "contents")

		link, err := e.driver.SignURL(t.Context(), key, storage.SignOptions{TTL: time.Second})

		must(t, "SignURL", err)

		time.Sleep(2 * time.Second)

		if status, _ := signedRequest(t, http.MethodGet, link.String(), ""); status/100 == 2 {
			t.Errorf("signed GET after expiry status = %d, want non-2xx", status)
		}
	})
}

// cancellation covers operations on an already canceled context.
func cancellation(t *testing.T, s suite) {
	s.sub(t, "CanceledContextPrecedesInvalidInput", canceledInvalidInput)
	s.sub(t, "OperationsFailWithContextError", func(t *testing.T, e env) {
		key := e.key("a")
		put(t, e, key, "old")

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		ops := map[string]func() error{
			"Open": func() error {
				_, err := e.driver.Open(ctx, key)

				return err
			},
			"Put": func() error {
				_, err := e.driver.Put(ctx, key, strings.NewReader("new"))

				return err
			},
			"Stat": func() error {
				_, err := e.driver.Stat(ctx, key)

				return err
			},
			"Delete": func() error {
				return e.driver.Delete(ctx, key)
			},
			"List": func() error {
				_, err := e.driver.List(ctx, storage.ListOptions{Prefix: e.prefix})

				return err
			},
			"SignURL": func() error {
				_, err := e.driver.SignURL(ctx, key, storage.SignOptions{TTL: time.Minute})

				return err
			},
		}

		for name, op := range ops {
			if err := op(); !errors.Is(err, context.Canceled) {
				t.Errorf("%s error = %v, want context.Canceled", name, err)
			}
		}

		wantBody(t, e, key, "old")
	})
}

// canceledInvalidInput checks the contract's cancellation precedence independently
// of key, prefix, TTL, method, and binding validation. It performs no valid writes.
func canceledInvalidInput(t *testing.T, e env) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	operations := []func() error{
		func() error {
			_, err := e.driver.Open(ctx, "../invalid")

			return err
		},
		func() error {
			_, err := e.driver.Put(ctx, "../invalid", strings.NewReader("data"))

			return err
		},
		func() error {
			_, err := e.driver.Stat(ctx, "../invalid")

			return err
		},
		func() error {
			return e.driver.Delete(ctx, "../invalid")
		},
		func() error {
			_, err := e.driver.List(ctx, storage.ListOptions{Prefix: "../invalid"})

			return err
		},
		func() error {
			_, err := e.driver.SignURL(ctx, "../invalid", storage.SignOptions{})

			return err
		},
	}

	for _, operation := range operations {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled operation = %v, want context.Canceled", err)
		}
	}
}

// concurrency covers simultaneous writers.
func concurrency(t *testing.T, s suite) {
	const writers = 8

	s.sub(t, "ConcurrentPutsToDistinctKeys", func(t *testing.T, e env) {
		var wg sync.WaitGroup

		for i := range writers {
			wg.Go(func() {
				contents := strings.Repeat(strconv.Itoa(i), 1000)

				if _, err := e.driver.Put(t.Context(), e.key("k"+strconv.Itoa(i)), strings.NewReader(contents)); err != nil {
					t.Errorf("Put: %v", err)
				}
			})
		}

		wg.Wait()

		for i := range writers {
			wantBody(t, e, e.key("k"+strconv.Itoa(i)), strings.Repeat(strconv.Itoa(i), 1000))
		}
	})
	s.sub(t, "ConcurrentPutsToOneKeyAreNotTorn", func(t *testing.T, e env) {
		key := e.key("shared")
		var wg sync.WaitGroup

		for i := range writers {
			wg.Go(func() {
				contents := strings.Repeat(strconv.Itoa(i), 64*1024)

				if _, err := e.driver.Put(t.Context(), key, strings.NewReader(contents)); err != nil {
					t.Errorf("Put: %v", err)
				}
			})
		}

		wg.Wait()

		_, body := read(t, e, key)

		if len(body) != 64*1024 || strings.Trim(body, body[:1]) != "" {
			t.Errorf("object is torn: %d bytes, starts %q", len(body), truncate(body))
		}
	})
}
