package opensearchsearch_test

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
	"arkoren.dev/driver/opensearch/opensearchsearch"
)

// TestSyncFailsWhenKeywordBecomesSearchable verifies that OpenSearch's refusal
// to change a field's type surfaces as a Sync error naming the remedy, and
// leaves the stored documents and the old definition in place.
func TestSyncFailsWhenKeywordBecomesSearchable(t *testing.T) {
	t.Parallel()

	engine, index, driver := integrationEngine(t)

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if err := engine.Put(t.Context(), index, item{
		ID:    "a",
		Title: "red",
		Value: "blue",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	changed := search.NewIndex(index.Name(), func(item item) string {
		return item.ID
	}, func(index *search.IndexBuilder) {
		index.Searchable("title", "value")
	})

	err := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(changed)
	}).Sync(t.Context())

	if err == nil || !strings.Contains(err.Error(), "drop the index") {
		t.Fatalf("Sync error = %v, want the drop-the-index advice", err)
	}

	if got := matching(t, engine, search.NewQuery(index, "red")); len(got) != 1 {
		t.Fatalf("documents after the failed Sync = %v, want a", got)
	}

	if err := engine.Check(t.Context()); err != nil {
		t.Fatalf("Check of the old definition after the failed Sync: %v", err)
	}
}

// TestSyncReindexesExistingDocuments verifies that a field that becomes
// filterable after documents were put also filters those documents.
func TestSyncReindexesExistingDocuments(t *testing.T) {
	t.Parallel()

	_, index, driver := integrationEngine(t)
	before := search.NewIndex(index.Name(), func(item item) string {
		return item.ID
	}, func(index *search.IndexBuilder) {
		index.Searchable("title")
	})

	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(before)
	})

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if err := engine.Put(t.Context(), before, item{
		ID:    "a",
		Title: "red",
		Value: "blue",
	}, item{
		ID:    "b",
		Title: "red",
		Value: "green",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	after := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := after.Sync(t.Context()); err != nil {
		t.Fatalf("Sync with value filterable: %v", err)
	}

	if err := after.Check(t.Context()); err != nil {
		t.Fatalf("Check after Sync: %v", err)
	}

	if got := matching(t, after, search.NewQuery(index, "", func(query *search.QueryBuilder) {
		query.Where("value", "blue")
	})); len(got) != 1 || got[0] != "a" {
		t.Fatalf("value = blue after Sync = %v, want [a]", got)
	}
}

// mappingEndpoint models only the exported mapping/reindex HTTP protocol. Tests
// control backend availability, but observe outcomes through Driver methods.
type mappingEndpoint struct {
	// mutex protects metadata shared by native health and application requests.
	mutex sync.Mutex
	// meta retains the native mapping metadata across HTTP requests.
	meta map[string]any
	// reindexAllowed controls a transient native reindex outage.
	reindexAllowed atomic.Bool
}

// ServeHTTP handles the native mapping protocol and transient reindex failures.
// Seed-health requests receive a successful response without changing metadata.
func (mappingEndpoint *mappingEndpoint) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	mappingEndpoint.mutex.Lock()
	defer mappingEndpoint.mutex.Unlock()

	body := any(map[string]any{"acknowledged": true})

	switch request.Method + " " + request.URL.Path {
	case "PUT /docs":
		writer.WriteHeader(http.StatusBadRequest)
		body = map[string]any{"error": map[string]string{"type": "resource_already_exists_exception"}}
	case "GET /docs/_mapping":
		body = map[string]any{"docs": map[string]any{"mappings": map[string]any{"_meta": mappingEndpoint.meta}}}
	case "PUT /docs/_mapping":
		var mapping map[string]any

		if err := json.UnmarshalRead(request.Body, &mapping); err != nil {
			http.Error(writer, "invalid mapping JSON", http.StatusBadRequest)

			return
		}

		if value, ok := mapping["_meta"].(map[string]any); ok {
			mappingEndpoint.meta = value
		}
	case "POST /docs/_update_by_query":
		if !mappingEndpoint.reindexAllowed.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}
	}

	// A disconnected test client cannot consume this response; no resources
	// depend on reporting that write failure back into the driver under test.
	_ = json.MarshalWrite(writer, body)
}

// TestFailedReindexRemainsCheckableAndRetryable models the exported mapping
// protocol and observes completion exclusively through Driver.Sync and Check.
func TestFailedReindexRemainsCheckableAndRetryable(t *testing.T) {
	t.Parallel()

	endpoint := &mappingEndpoint{meta: map[string]any{"arkoren_schema": map[string]any{"Name": "docs", "Searchable": []string{"title"}}}}
	driver := protocolDriver(t, endpoint.ServeHTTP)
	schemas := []search.Schema{{Name: "docs", Searchable: []string{"title"}, Filterable: []string{"category"}}}

	if err := driver.Sync(t.Context(), schemas); err == nil {
		t.Fatal("Sync accepted a failed reindex")
	}

	if err := driver.Check(t.Context(), schemas); !errors.Is(err, search.ErrSchemaChanged) {
		t.Fatalf("Check after failure=%v; want ErrSchemaChanged", err)
	}

	endpoint.reindexAllowed.Store(true)

	if err := driver.Sync(t.Context(), schemas); err != nil {
		t.Fatal(err)
	}

	if err := driver.Check(t.Context(), schemas); err != nil {
		t.Fatal(err)
	}
}

// TestCanceledEmptySyncFails checks cancellation even when there are no
// registered schemas and therefore no HTTP operation to enforce it indirectly.
func TestCanceledEmptySyncFails(t *testing.T) {
	t.Parallel()

	driver := protocolDriver(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK) // SDK seed probes are independent of this canceled operation.
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Sync(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync error=%v; want context.Canceled", err)
	}
}

// TestCanceledEmptyCheckFails exercises cancellation without any schema HTTP
// requests, so success cannot accidentally depend on a backend checking ctx.
func TestCanceledEmptyCheckFails(t *testing.T) {
	t.Parallel()

	driver := protocolDriver(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Check(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Check error=%v; want context.Canceled", err)
	}
}

// reindexDriver connects to the actual disposable backend with one controllable
// failure at the native RoundTripper boundary. Only update-by-query is rejected;
// health, mappings, metadata, indexing and searches always reach OpenSearch.
func reindexDriver(t *testing.T, blocked *atomic.Bool) *opensearchsearch.Driver {
	t.Helper()

	if testing.Short() {
		t.Skip("needs the OpenSearch service of compose.yaml")
	}

	base := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(base.CloseIdleConnections)
	transport := lifecycleTransport(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/_update_by_query") && blocked.Load() {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}

		return base.RoundTrip(request)
	})

	driver, err := opensearchsearch.New(cmp.Or(os.Getenv("OPENSEARCH_URL"), testAddress), func(engine *opensearchsearch.Builder) {
		engine.IndexPrefix("arkoren_retry_")
		engine.Client(func(config *opensearchapi.Config) {
			config.Client.Transport = transport
			config.Client.DisableRetry = true
			config.Client.DiscoverNodesInterval = -1
			config.Client.NodeStatsInterval = -1
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	return driver
}

// TestScalarReindexFailureKeepsCheckpointAndRetryableKinds verifies a failed
// native reindex leaves the old completed checkpoint, then a retry indexes typed
// scalars captured before their field was declared filterable or sortable.
func TestScalarReindexFailureKeepsCheckpointAndRetryableKinds(t *testing.T) {
	t.Parallel()

	blocked := &atomic.Bool{}
	blocked.Store(true)
	driver := reindexDriver(t, blocked)
	index := search.NewIndex("it_"+strings.ToLower(rand.Text())[:16], func(document map[string]any) string {
		return document["id"].(string)
	}, func(index *search.IndexBuilder) {
		index.Searchable("title")
		index.Filterable("value")
		index.Sortable("value")
	})
	t.Cleanup(func() {
		if err := driver.Drop(context.Background(), index.Schema()); err != nil {
			t.Error(err)
		}
	})

	before := index.Schema()
	before.Filterable, before.Sortable = nil, nil

	if err := driver.Sync(t.Context(), []search.Schema{before}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Put(t.Context(), before, []search.Document{
		{ID: "number", Source: []byte(`{"id":"number","title":"doc","value":15}`)},
		{ID: "string", Source: []byte(`{"id":"string","title":"doc","value":"true"}`)},
		{ID: "boolean", Source: []byte(`{"id":"boolean","title":"doc","value":true}`)},
	}); err != nil {
		t.Fatal(err)
	}

	native := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := native.Sync(t.Context()); err == nil {
		t.Fatal("Sync accepted the injected native reindex failure")
	}

	if err := native.Check(t.Context()); !errors.Is(err, search.ErrSchemaChanged) {
		t.Fatalf("Check after failed reindex=%v; want ErrSchemaChanged", err)
	}

	if err := driver.Check(t.Context(), []search.Schema{before}); err != nil {
		t.Fatalf("old checkpoint after failed reindex: %v", err)
	}

	blocked.Store(false)

	if err := native.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := native.Check(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := native.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	checkReindexedScalars(t, native, index)
}

// checkReindexedScalars compares the post-retry native index with memory using
// exact expected IDs. It covers all three scalar kinds and negated memberships,
// including the original coercion regressions after update-by-query, not just Put.
func checkReindexedScalars(t *testing.T, native *search.Engine, index search.Index[map[string]any]) {
	t.Helper()

	memory := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(index)
	})

	if err := memory.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := memory.Put(t.Context(), index,
		map[string]any{"id": "number", "title": "doc", "value": 15},
		map[string]any{"id": "string", "title": "doc", "value": "true"},
		map[string]any{"id": "boolean", "title": "doc", "value": true},
	); err != nil {
		t.Fatal(err)
	}

	engines := []*search.Engine{memory, native}

	requireScalarMatches(t, engines, scalarQuery(index, search.OpEqual, 15), []string{"number"})
	requireScalarMatches(t, engines, scalarQuery(index, search.OpEqual, "TRUE"), []string{"string"})
	requireScalarMatches(t, engines, scalarQuery(index, search.OpEqual, true), []string{"boolean"})
	requireScalarMatches(t, engines, scalarQuery(index, search.OpNotIn, 16), []string{"number"})
	requireScalarMatches(t, engines, scalarQuery(index, search.OpNotIn, "absent"), []string{"string"})
	requireScalarMatches(t, engines, scalarQuery(index, search.OpNotIn, false), []string{"boolean"})
}
