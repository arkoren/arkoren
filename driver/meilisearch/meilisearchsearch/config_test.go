package meilisearchsearch_test

import (
	"fmt"
	"strings"
	"testing"

	"arkoren.dev/driver/meilisearch/meilisearchsearch"
)

// TestConfigNeverPrintsTheAPIKey checks the redaction of every way a Config
// is formatted.
func TestConfigNeverPrintsTheAPIKey(t *testing.T) {
	t.Parallel()

	config := meilisearchsearch.Config{
		APIKey:      "s3cret",
		IndexPrefix: "p_",
	}

	for _, text := range []string{fmt.Sprint(config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config), config.LogValue().String()} {
		if strings.Contains(text, "s3cret") || !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, "p_") {
			t.Errorf("formatted config = %q, want the key redacted and the prefix shown", text)
		}
	}
}
