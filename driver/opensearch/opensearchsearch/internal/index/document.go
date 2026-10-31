package index

import (
	"encoding/json/v2"
	"fmt"
	"strings"

	"arkoren.dev/core/service/search"
)

// Document is the physical _source stored in OpenSearch. Original keeps
// the caller's JSON verbatim, including whitespace and application field names.
// Separate scalar containers prevent native cross-kind coercion. They capture
// scalar fields even before declaration, so Sync's ordinary update-by-query can
// index newly declared fields without rewriting this shape. Empty/dotted keys
// cannot be schema fields and are retained only in Original, because OpenSearch
// otherwise interprets dots as object paths even when dynamic mapping is off.
type Document struct {
	// Original is an unindexed JSON string, returned as Document.Source on reads.
	Original string `json:"source"`

	// Strings contains only JSON strings; searchable roles use text mappings.
	Strings map[string]string `json:"strings,omitempty"`

	// Numbers contains JSON numbers normalized to float64, as in memorysearch.
	Numbers map[string]float64 `json:"numbers,omitempty"`

	// Booleans contains only JSON booleans, never their textual representations.
	Booleans map[string]bool `json:"booleans,omitempty"`
}

// EncodeDocument separates scalar indexing from original source retention.
// Null, arrays and objects remain in Original but have no scalar index entry.
// Invalid JSON fails before the bulk write and names the external document ID;
// the caller's bytes and field containers are never mutated or retained.
func EncodeDocument(document search.Document) ([]byte, error) {
	var fields map[string]any

	if err := json.Unmarshal(document.Source, &fields); err != nil {
		return nil, fmt.Errorf("opensearch: document %s: decode source: %w", document.ID, err)
	}

	indexed := Document{
		Original: string(document.Source),
		Strings:  make(map[string]string),
		Numbers:  make(map[string]float64),
		Booleans: make(map[string]bool),
	}

	for field, value := range fields {
		if field == "" || strings.Contains(field, ".") {
			continue
		}

		switch value := value.(type) {
		case string:
			indexed.Strings[field] = value
		case float64:
			indexed.Numbers[field] = value
		case bool:
			indexed.Booleans[field] = value
		}
	}

	encoded, err := json.Marshal(indexed)

	if err != nil {
		return nil, fmt.Errorf("opensearch: document %s: encode index fields: %w", document.ID, err)
	}

	return encoded, nil
}
