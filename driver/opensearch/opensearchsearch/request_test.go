package opensearchsearch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"arkoren.dev/driver/opensearch/opensearchsearch"
)

// protocolDriver supplies a real isolated HTTP endpoint through the public
// constructor and retires its owned native client before closing that endpoint.
func protocolDriver(t *testing.T, handler http.HandlerFunc) *opensearchsearch.Driver {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	driver, err := opensearchsearch.New(server.URL, func(engine *opensearchsearch.Builder) {
		engine.Client(func(config *opensearchapi.Config) {
			config.Client.DisableRetry = true
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
