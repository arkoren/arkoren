package meilisearchsearch_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/meilisearch/meilisearchsearch"
)

// TestSyncRefusesAnIndexWithAnotherPrimaryKey checks that an index the driver
// did not create is not taken over.
func TestSyncRefusesAnIndexWithAnotherPrimaryKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
		}

		_, _ = writer.Write([]byte(`{"uid":"foreign","primaryKey":"id"}`))
	}))
	t.Cleanup(server.Close)

	driver, err := meilisearchsearch.New(server.URL)

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Sync(t.Context(), []search.Schema{{
		Name:       "foreign",
		Searchable: []string{"title"},
	}})

	if err == nil || !strings.Contains(err.Error(), `primary key "id"`) {
		t.Fatalf("Sync error = %v, want a primary key error", err)
	}
}

// TestCheckRejectsForeignPrimaryKey verifies matching search settings cannot
// make an externally created index compatible with the driver's document keys.
func TestCheckRejectsForeignPrimaryKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := `{"primaryKey":"id"}`

		if strings.HasSuffix(request.URL.Path, "/settings") {
			body = `{"searchableAttributes":["title"],"rankingRules":["sort","words","typo","proximity","attribute","exactness","arkoren_order:asc"],"typoTolerance":{"enabled":false},"pagination":{"maxTotalHits":1000}}`
		}

		if _, err := writer.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	driver, err := meilisearchsearch.New(server.URL)

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Check(t.Context(), []search.Schema{{Name: "docs", Searchable: []string{"title"}}})

	if !errors.Is(err, search.ErrSchemaChanged) {
		t.Fatalf("Check error=%v; want ErrSchemaChanged", err)
	}
}

// TestSyncRejectsForeignIndexCreatedConcurrently exercises the public task
// protocol when another creator wins and selects an incompatible primary key.
func TestSyncRejectsForeignIndexCreatedConcurrently(t *testing.T) {
	t.Parallel()

	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := `{"status":"succeeded"}`

		switch request.URL.Path {
		case "/indexes/docs":
			reads++
			body = `{"primaryKey":"id"}`

			if reads == 1 {
				writer.WriteHeader(http.StatusNotFound)
				body = `{"code":"index_not_found"}`
			}
		case "/indexes":
			body = `{"taskUid":1}`
		case "/tasks/1":
			body = `{"status":"failed","error":{"code":"index_already_exists"}}`
		case "/indexes/docs/settings":
			body = `{"taskUid":2}`
		}

		if _, err := writer.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	driver, err := meilisearchsearch.New(server.URL)

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Sync(t.Context(), []search.Schema{{Name: "docs", Searchable: []string{"title"}}})

	if !errors.Is(err, search.ErrSchemaChanged) {
		t.Fatalf("Sync error=%v; want ErrSchemaChanged", err)
	}
}

// TestSyncReportsSettingsReadFailure preserves a failed observation rather than
// silently converting it into a mutating settings request with a success result.
func TestSyncReportsSettingsReadFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := `{"primaryKey":"arkoren_id"}`

		if strings.HasSuffix(request.URL.Path, "/settings") {
			body = `{"taskUid":1}`

			if request.Method == http.MethodGet {
				writer.WriteHeader(http.StatusServiceUnavailable)
				body = `{"code":"temporarily_unavailable"}`
			}
		}

		if strings.HasPrefix(request.URL.Path, "/tasks/") {
			body = `{"status":"succeeded"}`
		}

		if _, err := writer.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	driver, err := meilisearchsearch.New(server.URL)

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Sync(t.Context(), []search.Schema{{Name: "docs", Searchable: []string{"title"}}})

	if err == nil || !strings.Contains(err.Error(), "temporarily_unavailable") {
		t.Fatalf("Sync error=%v; want original settings-read failure", err)
	}
}
