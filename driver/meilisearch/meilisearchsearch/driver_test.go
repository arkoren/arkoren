package meilisearchsearch_test

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/searchtest"
	"arkoren.dev/driver/meilisearch/meilisearchsearch"
)

// serverURL and masterKey are the address and master key of the Meilisearch
// server started by compose.yaml in the module directory.
const (
	serverURL = "http://127.0.0.1:17700"
	masterKey = "arkoren-test-master-key-0123456789"
)

// newDriver returns a started driver for the compose server whose indexes
// are named with prefix.
func newDriver(t *testing.T, prefix string) *meilisearchsearch.Driver {
	t.Helper()

	driver, err := meilisearchsearch.New(serverURL, func(engine *meilisearchsearch.Builder) {
		engine.APIKey(masterKey)
		engine.IndexPrefix(prefix)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v (is the server of compose.yaml running?)", err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(t.Context())
	})

	return driver
}

// TestConformance runs the search conformance suite against a Meilisearch
// server.
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs the Meilisearch server of compose.yaml")
	}

	searchtest.Run(t, func(t *testing.T) search.Driver {
		return newDriver(t, "")
	})
}

// note is the document type of the integration tests that go beyond the
// conformance suite.
type note struct {
	// ID supplies the stable key used in expected search results.
	ID string `json:"id"`

	// Title supplies text for word, exclusion, and relevance checks.
	Title string `json:"title"`

	// Kind is an optional category; nil omits it for missing-value checks.
	Kind *string `json:"kind,omitempty"`

	// Size is an optional numeric sort key; nil leaves the field absent.
	Size *float64 `json:"size,omitempty"`
}

// noteFixture is an engine with one synced, uniquely named index of notes.
type noteFixture struct {
	// engine shares the configured driver and owns the registered note index.
	engine *search.Engine

	// index is the isolated definition used for writes and typed queries.
	index search.Index[note]
}

// newNoteFixture returns a noteFixture on a server whose indexes are named
// with prefix, and drops the index when the test ends.
func newNoteFixture(t *testing.T, prefix string) noteFixture {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test: needs the Meilisearch server of compose.yaml")
	}

	index := search.NewIndex("notes_"+strings.ToLower(rand.Text())[:12], func(note note) string {
		return note.ID
	}, func(index *search.IndexBuilder) {
		index.Searchable("title")
		index.Filterable("kind", "size")
		index.Sortable("size")
	})
	engine := search.New(newDriver(t, prefix), func(engine *search.Builder) {
		engine.Indexes(index)
	})
	t.Cleanup(func() {
		_ = engine.Drop(t.Context(), index) // Best-effort test cleanup.
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	return noteFixture{
		engine: engine,
		index:  index,
	}
}

// ids runs query and returns the IDs of the first page of 100 hits.
func (fixture noteFixture) ids(t *testing.T, query search.Query[note]) []string {
	t.Helper()

	page, err := fixture.engine.Search(t.Context(), query, pagination.PageParams{
		Page:    1,
		PerPage: 100,
	})

	if err != nil {
		t.Fatal(err)
	}

	ids := make([]string, len(page.Items))

	for i, hit := range page.Items {
		ids[i] = hit.ID
	}

	return ids
}

// wantIDs fails the test unless got is exactly want.
func wantIDs(t *testing.T, got []string, want ...string) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

// str returns a pointer to a string.
func str(value string) *string {
	return &value
}

// num returns a pointer to a number.
func num(value float64) *float64 {
	return &value
}

// TestIndexPrefixSeparatesIndexes checks that drivers with different
// prefixes do not see each other's indexes.
func TestIndexPrefixSeparatesIndexes(t *testing.T) {
	t.Parallel()

	fx := newNoteFixture(t, "one_")

	other := search.New(newDriver(t, "two_"), func(engine *search.Builder) {
		engine.Indexes(fx.index)
	})

	if err := other.Check(t.Context()); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Check under another prefix = %v, want ErrNotFound", err)
	}

	if err := fx.engine.Check(t.Context()); err != nil {
		t.Fatalf("Check under the prefix = %v", err)
	}
}

// wrappedTransport represents a valid application-installed default transport
// that intentionally does not expose the native *http.Transport concrete type.
type wrappedTransport struct {
	// RoundTripper retains the application's existing network implementation.
	http.RoundTripper
}

// TestNewSupportsWrappedDefaultTransport uses the public request path to verify
// construction accepts an application wrapper without asserting its concrete type.
func TestNewSupportsWrappedDefaultTransport(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = wrappedTransport{RoundTripper: previous}
	t.Cleanup(func() {
		http.DefaultTransport = previous
	})

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	driver, err := meilisearchsearch.New(server.URL)

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestBuilderPoliciesRunOnceInOrder checks delegation does not replay policies
// and a retained draft cannot change the published authorization or prefix.
func TestBuilderPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	requests := make(chan [2]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- [2]string{request.URL.Path, request.Header.Get("Authorization")}
		writer.WriteHeader(http.StatusNotFound)

		if _, err := writer.Write([]byte(`{"code":"index_not_found","message":"missing"}`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	var retained *meilisearchsearch.Builder
	calls := 0
	driver, err := meilisearchsearch.New(server.URL, func(engine *meilisearchsearch.Builder) {
		calls++
		retained = engine

		if engine.APIKey("first") != engine || engine.IndexPrefix("first_") != engine {
			t.Fatal("settings did not return the draft")
		}
	}, func(engine *meilisearchsearch.Builder) {
		calls++

		if engine != retained {
			t.Fatal("callbacks received different drafts")
		}

		engine.APIKey("final")
		engine.IndexPrefix("shop_")
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	retained.APIKey("changed").IndexPrefix("changed_")

	if err := driver.Drop(t.Context(), search.Schema{
		Name: "products",
	}); err != nil {
		t.Fatal(err)
	}

	request := <-requests

	if calls != 2 || request != [2]string{"/indexes/shop_products", "Bearer final"} {
		t.Fatalf("calls=%d request=%q", calls, request)
	}
}

// TestNewFromBuilderUsesBorrowedClient checks the supplied HTTP client is kept
// and policies run only once; both APIKey and IndexPrefix apply to this wrapper.
func TestNewFromBuilderUsesBorrowedClient(t *testing.T) {
	t.Parallel()

	client := &http.Client{}
	calls := 0
	driver, err := meilisearchsearch.NewFrom(client, "https://search.example", func(engine *meilisearchsearch.Builder) {
		calls++
		engine.APIKey("key")
		engine.IndexPrefix("shop_")
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.Client() != client || calls != 1 {
		t.Fatal("NewFrom changed the client or replayed policies")
	}
}

// TestBuilderPoliciesRunOnInvalidHost verifies callbacks precede ordinary host
// validation and run exactly once even when construction returns an error.
func TestBuilderPoliciesRunOnInvalidHost(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := meilisearchsearch.New("", func(engine *meilisearchsearch.Builder) {
		calls++
		engine.IndexPrefix("shop_")
	})

	if calls != 1 || err == nil {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

// TestBuilderNilPolicyPanics checks a supplied nil entry is misuse rather than
// an omitted policy, including when required host validation would fail.
func TestBuilderNilPolicyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = meilisearchsearch.New("", nil)
}

// TestBuilderPanicPropagates checks callbacks fail before host validation with
// their original panic, and no later callback executes.
func TestBuilderPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := new(int)
	defer func() {
		if recover() != marker {
			t.Fatal("constructor changed the callback panic")
		}
	}()

	_, _ = meilisearchsearch.New("", func(*meilisearchsearch.Builder) {
		panic(marker)
	}, func(*meilisearchsearch.Builder) {
		t.Fatal("callback ran after panic")
	})
}

// TestNewFromNilPolicyPanics checks wrapper policies have the same direct nil
// callback semantics without touching a native client or making a request.
func TestNewFromNilPolicyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = meilisearchsearch.NewFrom(nil, "", nil)
}

// TestBuilderEmptyPoliciesKeepDefaults checks nil and empty slice expansions
// preserve the ordinary zero authentication and prefix policy on fresh drafts.
func TestBuilderEmptyPoliciesKeepDefaults(t *testing.T) {
	t.Parallel()

	for _, configure := range [][]func(*meilisearchsearch.Builder){nil, {}} {
		driver, err := meilisearchsearch.New("http://localhost:7700", configure...)

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

// offlineDriver returns a driver for an address nothing listens on, for tests
// of calls that must fail before sending a request.
func offlineDriver(t *testing.T) *meilisearchsearch.Driver {
	t.Helper()

	driver, err := meilisearchsearch.New("http://127.0.0.1:1")

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// versionServer answers every request with the given Meilisearch version and
// records the Authorization header of the last one.
func versionServer(t *testing.T, version string, authorization *string) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if authorization != nil {
			*authorization = request.Header.Get("Authorization")
		}

		_ = json.MarshalWrite(writer, map[string]string{"pkgVersion": version})
	}))
	t.Cleanup(server.Close)

	return server.URL
}

// TestNewRejectsInvalidHosts checks that the host must be an absolute http or
// https URL.
func TestNewRejectsInvalidHosts(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"", "localhost:7700", "/indexes", "ftp://localhost", "http://", "http://%zz"} {
		if _, err := meilisearchsearch.New(host); err == nil {
			t.Errorf("New(%q) succeeded, want an error", host)
		}
	}
}

// TestNewRejectsHostQueryOrFragment protects endpoint joining: neither component
// is a path prefix and appending an endpoint would target a different resource.
func TestNewRejectsHostQueryOrFragment(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"https://search.example/?key=value", "https://search.example/#section"} {
		if _, err := meilisearchsearch.New(host); err == nil {
			t.Fatalf("New(%q) accepted a query or fragment", host)
		}
	}
}

// TestNewRejectsInvalidIndexPrefixes checks the characters and length a
// Meilisearch uid allows.
func TestNewRejectsInvalidIndexPrefixes(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{"has space", "slash/", "dot.", "café", strings.Repeat("x", 101)} {
		if _, err := meilisearchsearch.New("http://localhost:7700", func(engine *meilisearchsearch.Builder) {
			engine.IndexPrefix(prefix)
		}); err == nil {
			t.Errorf("New with prefix %q succeeded, want an error", prefix)
		}
	}

	if _, err := meilisearchsearch.New("http://localhost:7700", func(engine *meilisearchsearch.Builder) {
		engine.IndexPrefix("Shop_prod-1_")
	}); err != nil {
		t.Fatalf("New with a valid prefix: %v", err)
	}
}

// TestStartSendsTheAPIKey checks the bearer token of requests.
func TestStartSendsTheAPIKey(t *testing.T) {
	t.Parallel()

	var authorization string
	driver, err := meilisearchsearch.New(versionServer(t, "1.55.0", &authorization), func(engine *meilisearchsearch.Builder) {
		engine.APIKey("key-1")
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if authorization != "Bearer key-1" {
		t.Fatalf("Authorization = %q, want a bearer token", authorization)
	}
}

// TestStartRejectsOldServers checks the minimum version.
func TestStartRejectsOldServers(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"1.14.9", "0.30.0"} {
		driver, err := meilisearchsearch.New(versionServer(t, version, nil))

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "too old") {
			t.Errorf("Start against %s = %v, want a too old error", version, err)
		}
	}
}

// TestStartAcceptsNewServers checks that the minimum itself and later major
// versions pass.
func TestStartAcceptsNewServers(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"1.15.0", "1.55.0-rc.1", "2.0.0"} {
		driver, err := meilisearchsearch.New(versionServer(t, version, nil))

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Start(t.Context()); err != nil {
			t.Errorf("Start against %s = %v, want no error", version, err)
		}
	}
}

// TestRequestFailuresCarryTheMeilisearchCode checks that the error document
// of a failed request is kept in the returned error.
func TestRequestFailuresCarryTheMeilisearchCode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"message":"The provided API key is invalid.","code":"invalid_api_key"}`))
	}))
	t.Cleanup(server.Close)

	driver, err := meilisearchsearch.New(server.URL)

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Ping(t.Context())

	if err == nil || !strings.Contains(err.Error(), "invalid_api_key") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("Ping error = %v, want the code and status", err)
	}
}
