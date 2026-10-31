package index_test

import (
	"strings"
	"testing"

	opensearchsearch "arkoren.dev/driver/opensearch/opensearchsearch/internal/index"
)

// TestMappingByFieldRole verifies the mapping of a text-only, a text and
// keyword, a keyword, and a sortable number field, and that the schema is stored.
func TestMappingByFieldRole(t *testing.T) {
	t.Parallel()

	got := compact(t, opensearchsearch.MappingBody(testSchema))

	for _, want := range []string{
		`"description":{"analyzer":"arkoren_text","type":"text"}`,
		`"title":{"analyzer":"arkoren_text","fields":{"keyword":{"normalizer":"arkoren_keyword","type":"keyword"}},"type":"text"}`,
		`"category":{"normalizer":"arkoren_keyword","type":"keyword"}`,
		`"price":{"coerce":false,"type":"double"}`,
		`"price":{"type":"boolean"}`,
		`"source":{"index":false,"type":"text"}`,
		`"dynamic":false`,
		`"_meta":{"arkoren_schema":{"Name":"products"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mapping lacks %s\n%s", want, got)
		}
	}
}
