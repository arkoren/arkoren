package memorysearch_test

import (
	"testing"

	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
	"arkoren.dev/core/service/search/searchtest"
)

// TestConformance runs the search conformance suite against the memory
// driver.
func TestConformance(t *testing.T) {
	t.Parallel()

	searchtest.Run(t, func(*testing.T) search.Driver {
		return memorysearch.New()
	})
}
