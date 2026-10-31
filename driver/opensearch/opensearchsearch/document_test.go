package opensearchsearch_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/service/search"
)

// TestPutToUnsyncedIndexDoesNotCreateIt verifies that Put fails with
// ErrNotFound and leaves no index behind that OpenSearch would have created
// with a dynamic mapping.
func TestPutToUnsyncedIndexDoesNotCreateIt(t *testing.T) {
	t.Parallel()

	engine, index, driver := integrationEngine(t)

	err := engine.Put(t.Context(), index, item{
		ID:    "a",
		Title: "x",
	})

	if !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Put error = %v, want ErrNotFound", err)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodHead, "/arkoren_extra_"+index.Name(), nil)

	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	response, err := driver.Client().Client.Request(request)

	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("index exists after a failed Put, status %d", response.StatusCode)
	}
}

// TestPutReportsTheFailingDocument verifies that a value the mapping cannot
// hold fails the Put with the document's ID.
func TestPutReportsTheFailingDocument(t *testing.T) {
	t.Parallel()

	engine, index, _ := integrationEngine(t)

	if err := engine.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	err := engine.Put(t.Context(), index, item{
		ID:    "good",
		Title: "fine",
	}, item{
		ID:    "bad",
		Title: "broken",
		Value: strings.Repeat("x", 32767),
	})

	if err == nil || !strings.Contains(err.Error(), "document bad") {
		t.Fatalf("Put error = %v, want it to name document bad", err)
	}
}

// TestCanceledEmptyDeleteFails checks cancellation before the empty bulk path.
// The raw Driver contract applies even though the facade skips empty deletions.
func TestCanceledEmptyDeleteFails(t *testing.T) {
	t.Parallel()

	driver := protocolDriver(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Delete(ctx, search.Schema{Name: "docs"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete error=%v; want context.Canceled", err)
	}
}

// TestEmptyDeleteChecksMissingIndex preserves the raw contract's missing-index
// result independently of how many document IDs were supplied.
func TestEmptyDeleteChecksMissingIndex(t *testing.T) {
	t.Parallel()

	driver := protocolDriver(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/docs" {
			writer.WriteHeader(http.StatusNotFound)
		}
	})

	if err := driver.Delete(t.Context(), search.Schema{Name: "docs"}, nil); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Delete error=%v; want ErrNotFound", err)
	}
}
